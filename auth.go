package main

import (
	"bufio"
	"context"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	pbkdf2Iter    = 600_000
	sessionCookie = "session"
	sessionTTL    = 7 * 24 * time.Hour
	maxFailures   = 5
	lockout       = 15 * time.Minute

	minPasswordLen = 10
	wrongLogin     = "Wrong username or password"
)

var usernameRE = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)

// dummySalt lets CheckUser spend a derivation on unknown users, so timing doesn't reveal which exist.
var dummySalt = make([]byte, 16)

func derive(password string, salt []byte) ([]byte, error) {
	return pbkdf2.Key(sha256.New, password, salt, pbkdf2Iter, 32)
}

// SetUser creates the user or resets their password, ending all their sessions.
func (s *Store) SetUser(name, password string) error {
	if !usernameRE.MatchString(name) {
		return fmt.Errorf("username must match %s", usernameRE)
	}
	if utf8.RuneCountInString(password) < minPasswordLen {
		return fmt.Errorf("password must be at least %d characters", minPasswordLen)
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	hash, err := derive(password, salt)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO users (name, salt, hash) VALUES (?, ?, ?)
		ON CONFLICT (name) DO UPDATE SET salt = excluded.salt, hash = excluded.hash`,
		name, hex.EncodeToString(salt), hex.EncodeToString(hash)); err != nil {
		return err
	}
	// A reset must also cut off whoever holds a cookie from the old password.
	if _, err := tx.Exec(`DELETE FROM sessions WHERE user_name = ?`, name); err != nil {
		return err
	}
	return tx.Commit()
}

// CheckUser reports whether password matches the user's, in constant time.
func (s *Store) CheckUser(name, password string) (bool, error) {
	var saltHex, hashHex string
	err := s.db.QueryRow(`SELECT salt, hash FROM users WHERE name = ?`, name).Scan(&saltHex, &hashHex)
	if errors.Is(err, sql.ErrNoRows) {
		_, err = derive(password, dummySalt)
		return false, err
	}
	if err != nil {
		return false, err
	}
	salt, err := hex.DecodeString(saltHex)
	if err != nil {
		return false, err
	}
	want, err := hex.DecodeString(hashHex)
	if err != nil {
		return false, err
	}
	got, err := derive(password, salt)
	if err != nil {
		return false, err
	}
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// hashToken is what the sessions table stores, so a database leak doesn't yield usable cookies.
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// NewSession starts a session for user and returns the cookie token.
func (s *Store) NewSession(user string, now time.Time) (string, error) {
	token := rand.Text()
	_, err := s.db.Exec(`INSERT INTO sessions (token_hash, user_name, expires_at) VALUES (?, ?, ?)`,
		hashToken(token), user, now.Add(sessionTTL).In(s.loc).Format(stampFmt))
	if err != nil {
		return "", err
	}
	return token, nil
}

// SessionUser returns the session's user, or "" if the session is missing or expired.
func (s *Store) SessionUser(token string, now time.Time) (string, error) {
	var user string
	err := s.db.QueryRow(`SELECT user_name FROM sessions WHERE token_hash = ? AND expires_at > ?`,
		hashToken(token), now.In(s.loc).Format(stampFmt)).Scan(&user)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return user, err
}

func (s *Store) DeleteSession(token string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE token_hash = ?`, hashToken(token))
	return err
}

// limiter locks a username out after maxFailures login attempts within lockout. The zero value is ready to use.
type limiter struct {
	mu    sync.Mutex
	fails map[string][]time.Time
}

// try counts a login attempt for user and reports whether it may proceed. It records the attempt
// itself, before the caller's slow password check, so parallel requests can't all pass a separate
// "locked" test. A successful login should call reset.
func (l *limiter) try(user string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.fails == nil {
		l.fails = map[string][]time.Time{}
	}
	// Names are only pruned when retried, so drop stale ones once the map is big.
	if len(l.fails) > 1000 {
		for u, ts := range l.fails {
			if now.Sub(ts[len(ts)-1]) >= lockout {
				delete(l.fails, u)
			}
		}
	}
	recent := l.fails[user][:0]
	for _, t := range l.fails[user] {
		if now.Sub(t) < lockout {
			recent = append(recent, t)
		}
	}
	if len(recent) >= maxFailures {
		l.fails[user] = recent
		return false
	}
	l.fails[user] = append(recent, now)
	return true
}

func (l *limiter) reset(user string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.fails, user)
}

func setSessionCookie(w http.ResponseWriter, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: value, Path: "/", MaxAge: maxAge,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
	})
}

func serverError(w http.ResponseWriter, what string, err error) {
	slog.Error(what, "err", err)
	http.Error(w, "Internal error", http.StatusInternalServerError)
}

// loginHandlers returns the login form, the login submit, and logout. tmpl must define "login.html".
func loginHandlers(st *Store, lim *limiter, now func() time.Time, tmpl *template.Template) (get, post, logout http.HandlerFunc) {
	render := func(w http.ResponseWriter, status int, msg string) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(status)
		if err := tmpl.ExecuteTemplate(w, "login.html", struct{ Error string }{msg}); err != nil {
			slog.Error("render login", "err", err)
		}
	}
	get = func(w http.ResponseWriter, r *http.Request) { render(w, http.StatusOK, "") }
	post = func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
		user, pass := r.PostFormValue("username"), r.PostFormValue("password")
		t := now()
		// Malformed names never reach the limiter or the database, so attackers can't grow either.
		if !usernameRE.MatchString(user) || !lim.try(user, t) {
			render(w, http.StatusUnauthorized, wrongLogin)
			return
		}
		ok, err := st.CheckUser(user, pass)
		if err != nil {
			serverError(w, "check user", err)
			return
		}
		if !ok {
			render(w, http.StatusUnauthorized, wrongLogin)
			return
		}
		token, err := st.NewSession(user, t)
		if err != nil {
			serverError(w, "new session", err)
			return
		}
		lim.reset(user)
		setSessionCookie(w, token, int(sessionTTL.Seconds()))
		http.Redirect(w, r, "/calendar", http.StatusSeeOther)
	}
	logout = func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(sessionCookie); err == nil {
			if err := st.DeleteSession(c.Value); err != nil {
				serverError(w, "delete session", err)
				return
			}
		}
		setSessionCookie(w, "", -1)
		http.Redirect(w, r, "/login", http.StatusSeeOther)
	}
	return get, post, logout
}

type userKey struct{}

// requireLogin redirects to /login unless the request has a live session, and otherwise exposes the user via currentUser.
func requireLogin(st *Store, now func() time.Time, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var user string
		c, err := r.Cookie(sessionCookie)
		if err == nil {
			if user, err = st.SessionUser(c.Value, now()); err != nil {
				serverError(w, "session user", err)
				return
			}
		}
		if user == "" {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey{}, user)))
	})
}

// currentUser returns the logged-in user set by requireLogin, or "".
func currentUser(r *http.Request) string {
	user, _ := r.Context().Value(userKey{}).(string)
	return user
}

// runAddUser sets name's password from the first line of in.
func runAddUser(st *Store, name string, in io.Reader) error {
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && err != io.EOF {
		return err
	}
	return st.SetUser(name, strings.TrimRight(line, "\r\n"))
}
