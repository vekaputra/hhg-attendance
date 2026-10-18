package main

import (
	"fmt"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

var loginTmpl = template.Must(template.ParseFiles("templates/login.html"))

func postCreds(h http.HandlerFunc, path, user, pass string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	form := url.Values{"username": {user}, "password": {pass}}
	r := httptest.NewRequest("POST", path, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, c := range cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	h(w, r)
	return w
}

func getWithCookies(h http.Handler, path string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", path, nil)
	for _, c := range cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func sessionFrom(t *testing.T, w *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookie {
			return c
		}
	}
	t.Fatal("no session cookie set")
	return nil
}

func TestUserPassword(t *testing.T) {
	st := newTestStore(t)
	if err := st.SetUser("ana", "correct horse"); err != nil {
		t.Fatal(err)
	}
	check := func(user, pass string, want bool) {
		t.Helper()
		if got, err := st.CheckUser(user, pass); err != nil || got != want {
			t.Errorf("CheckUser(%q, %q) = %v, %v; want %v", user, pass, got, err, want)
		}
	}
	check("ana", "correct horse", true)
	check("ana", "wrong", false)
	check("nobody", "correct horse", false)

	if err := st.SetUser("ana", "short"); err == nil {
		t.Error("SetUser accepted a password under 10 characters")
	}
	check("ana", "correct horse", true) // the failed reset changed nothing

	if err := st.SetUser("ana", "another password"); err != nil {
		t.Fatal(err)
	}
	check("ana", "correct horse", false)
	check("ana", "another password", true)
}

func TestUserAddFromInput(t *testing.T) {
	st := newTestStore(t)
	if err := runAddUser(st, "ana", strings.NewReader("correct horse\r\nignored\n")); err != nil {
		t.Fatal(err)
	}
	if ok, err := st.CheckUser("ana", "correct horse"); err != nil || !ok {
		t.Errorf("CheckUser = %v, %v; want true (line ending trimmed)", ok, err)
	}
	if err := runAddUser(st, "bob", strings.NewReader("short\n")); err == nil {
		t.Error("runAddUser accepted a short password")
	}
}

func TestLoginFlow(t *testing.T) {
	st := newTestStore(t)
	if err := st.SetUser("ana", "correct horse"); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	getLogin, post, logout := loginHandlers(st, &limiter{}, clock, loginTmpl)
	protected := requireLogin(st, clock, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "hello ", currentUser(r))
	}))

	if w := getWithCookies(getLogin, "/login"); w.Code != 200 || !strings.Contains(w.Body.String(), `name="password"`) {
		t.Errorf("GET /login = %d, form missing", w.Code)
	}

	w := postCreds(post, "/login", "ana", "wrong")
	if w.Code != 401 || !strings.Contains(w.Body.String(), "Wrong username or password") {
		t.Errorf("wrong password = %d %q, want 401 with message", w.Code, w.Body)
	}

	w = postCreds(post, "/login", "ana", "correct horse")
	if w.Code != 303 || w.Header().Get("Location") != "/calendar" {
		t.Fatalf("login = %d to %q, want 303 to /calendar", w.Code, w.Header().Get("Location"))
	}
	c := sessionFrom(t, w)
	if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode || c.Path != "/" || c.MaxAge != int(sessionTTL.Seconds()) {
		t.Errorf("cookie = %+v", c)
	}

	if w := getWithCookies(protected, "/calendar", c); w.Code != 200 || w.Body.String() != "hello ana" {
		t.Errorf("with session = %d %q, want 200 hello ana", w.Code, w.Body)
	}
	if w := getWithCookies(protected, "/calendar"); w.Code != 303 || w.Header().Get("Location") != "/login" {
		t.Errorf("without session = %d to %q, want 303 to /login", w.Code, w.Header().Get("Location"))
	}

	w = postCreds(logout, "/logout", "", "", c)
	if w.Code != 303 || w.Header().Get("Location") != "/login" || sessionFrom(t, w).MaxAge >= 0 {
		t.Errorf("logout = %d to %q, cookie %+v; want 303 to /login clearing the cookie", w.Code, w.Header().Get("Location"), sessionFrom(t, w))
	}
	if w := getWithCookies(protected, "/calendar", c); w.Code != 303 {
		t.Errorf("after logout = %d, want 303", w.Code)
	}
}

func TestLoginLockout(t *testing.T) {
	st := newTestStore(t)
	if err := st.SetUser("ana", "correct horse"); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)
	_, post, _ := loginHandlers(st, &limiter{}, func() time.Time { return now }, loginTmpl)

	for range maxFailures {
		if w := postCreds(post, "/login", "ana", "wrong"); w.Code != 401 {
			t.Fatalf("wrong password = %d, want 401", w.Code)
		}
		now = now.Add(time.Second)
	}
	if w := postCreds(post, "/login", "ana", "correct horse"); w.Code != 401 {
		t.Errorf("locked user with correct password = %d, want 401", w.Code)
	}
	now = now.Add(lockout + time.Minute)
	if w := postCreds(post, "/login", "ana", "correct horse"); w.Code != 303 {
		t.Errorf("after lockout = %d, want 303", w.Code)
	}
}

func TestSessionExpiry(t *testing.T) {
	st := newTestStore(t)
	now := time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)
	token, err := st.NewSession("ana", now)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name  string
		token string
		at    time.Time
		want  string
	}{
		{"valid", token, now.Add(sessionTTL - time.Second), "ana"},
		{"expired", token, now.Add(sessionTTL + time.Second), ""},
		{"unknown token", "nope", now, ""},
	} {
		if got, err := st.SessionUser(c.token, c.at); err != nil || got != c.want {
			t.Errorf("%s: SessionUser = %q, %v; want %q", c.name, got, err, c.want)
		}
	}
}
