package main

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strconv"
	"time"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static/*
var staticFS embed.FS

// contactWarnAfter is how long a device may stay silent before every page warns about it.
const contactWarnAfter = 2 * time.Minute

// deviceWarnings returns a message for each allowed device that is silent or has never been heard from, in allowed order.
func deviceWarnings(contacts map[string]time.Time, allowed []string, now time.Time) []string {
	var msgs []string
	for _, id := range allowed {
		last, ok := contacts[id]
		silent := now.Sub(last)
		switch {
		case !ok:
			msgs = append(msgs, id+": no contact yet")
		case silent <= contactWarnAfter:
		case silent >= time.Hour:
			msgs = append(msgs, fmt.Sprintf("%s: no contact for %dh", id, int(silent.Hours())))
		default:
			msgs = append(msgs, fmt.Sprintf("%s: no contact for %dm", id, int(silent.Minutes())))
		}
	}
	return msgs
}

func hours(d time.Duration) string {
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}
func mins(d time.Duration) string { return fmt.Sprintf("%dm", int(d.Minutes())) }
func clock(t time.Time) string    { return t.Format(clockFmt) }

// server holds what every handler needs. tmpl has one set per page, each with the layout,
// because pages all define "content"; login.html stands alone.
type server struct {
	st   *Store
	loc  *time.Location
	now  func() time.Time
	tmpl map[string]*template.Template
}

// parseTemplates parses the embedded templates once at startup; a broken template is a bug, so it panics.
func parseTemplates() map[string]*template.Template {
	funcs := template.FuncMap{"hours": hours, "mins": mins, "clock": clock}
	files, err := fs.Glob(templateFS, "templates/*.html")
	if err != nil {
		panic(err)
	}
	tmpl := map[string]*template.Template{}
	for _, file := range files {
		name := path.Base(file)
		set := []string{"templates/layout.html", file}
		switch name {
		case "layout.html":
			continue
		case "login.html":
			set = set[1:]
		}
		tmpl[name] = template.Must(template.New(name).Funcs(funcs).ParseFS(templateFS, set...))
	}
	return tmpl
}

// newServer builds the web app. Only the login routes and static files are public; any other
// route, registered or not, sits behind a session.
func newServer(st *Store, loc *time.Location, now func() time.Time) http.Handler {
	srv := &server{st: st, loc: loc, now: now, tmpl: parseTemplates()}
	getLogin, postLogin, logout := loginHandlers(st, &limiter{}, now, srv.tmpl["login.html"])

	pages := http.NewServeMux()
	pages.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/calendar", http.StatusSeeOther)
	})
	pages.HandleFunc("GET /calendar", srv.calendar)
	pages.HandleFunc("POST /logout", logout)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /login", getLogin)
	mux.HandleFunc("POST /login", postLogin)
	mux.Handle("GET /static/", http.FileServerFS(staticFS))
	mux.Handle("/", requireLogin(st, now, pages))
	return http.NewCrossOriginProtection().Handler(mux)
}

// pageBase is what the layout shows on every page; each page's data embeds it.
type pageBase struct {
	User     string
	Warnings []string
}

func (srv *server) base(r *http.Request) (pageBase, error) {
	contacts, err := srv.st.DeviceContacts()
	if err != nil {
		return pageBase{}, err
	}
	return pageBase{User: currentUser(r), Warnings: deviceWarnings(contacts, allowedDeviceIDs, srv.now())}, nil
}

// render writes the page inside the layout. It renders to a buffer first so a template error becomes a clean 500.
func (srv *server) render(w http.ResponseWriter, status int, page string, data any) {
	var buf bytes.Buffer
	if err := srv.tmpl[page].ExecuteTemplate(&buf, "layout.html", data); err != nil {
		serverError(w, "render "+page, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	buf.WriteTo(w)
}

type navLink struct {
	Label, URL string
	On         bool
}

// monthCell is one square of the month grid; Date is zero for the blanks around the month.
type monthCell struct {
	Date    time.Time
	Entries []entry
}

// entry is one person's problem day in the month grid.
type entry struct {
	Employee Employee
	Result
}

type calendarPage struct {
	pageBase
	View       string // "month" or "week"
	Title      string
	Month      string // at's month (YYYY-MM), for the person links
	Tabs       []navLink
	Views      []navLink
	Prev, Next navLink

	Weeks [][]monthCell // month view
	Dates []time.Time   // week view column days
	Rows  []Row         // week view
}

// calendar shows one organisation as a month grid or a people × days week.
func (srv *server) calendar(w http.ResponseWriter, r *http.Request) {
	orgs, err := srv.st.Orgs()
	if err != nil {
		serverError(w, "orgs", err)
		return
	}
	q := r.URL.Query()
	org := orgs[0]
	if v := q.Get("org"); v != "" {
		i := slices.IndexFunc(orgs, func(o Org) bool { return strconv.FormatInt(o.ID, 10) == v })
		if i < 0 {
			http.Error(w, "unknown org", http.StatusBadRequest)
			return
		}
		org = orgs[i]
	}
	view := q.Get("view")
	switch view {
	case "":
		view = "month"
	case "month", "week":
	default:
		http.Error(w, "unknown view", http.StatusBadRequest)
		return
	}
	y, m, d := srv.now().In(srv.loc).Date()
	day := time.Date(y, m, d, 0, 0, 0, 0, srv.loc)
	if v := q.Get("at"); v != "" {
		if day, err = time.ParseInLocation(dateFmt, v, srv.loc); err != nil {
			http.Error(w, "bad date", http.StatusBadRequest)
			return
		}
	}

	link := func(label string, o Org, v string, at time.Time, on bool) navLink {
		qs := url.Values{"org": {strconv.FormatInt(o.ID, 10)}, "view": {v}, "at": {at.Format(dateFmt)}}
		return navLink{label, "/calendar?" + qs.Encode(), on}
	}
	page := calendarPage{View: view, Month: day.Format("2006-01")}
	for _, o := range orgs {
		page.Tabs = append(page.Tabs, link(o.Name, o, view, day, o.ID == org.ID))
	}
	page.Views = []navLink{link("Month grid", org, "month", day, view == "month"), link("People × days", org, "week", day, view == "week")}

	var from, to, prev, next time.Time
	var prevLabel, nextLabel string
	if view == "week" {
		from = day.AddDate(0, 0, 1-isoWeekday(day))
		to = from.AddDate(0, 0, 6)
		prev, next = from.AddDate(0, 0, -7), from.AddDate(0, 0, 7)
		prevLabel, nextLabel = "previous week", "next week"
		page.Title = from.Format("Mon 2 Jan") + " – " + to.Format("Mon 2 Jan 2006")
		for i := range 7 {
			page.Dates = append(page.Dates, from.AddDate(0, 0, i))
		}
	} else {
		from = time.Date(day.Year(), day.Month(), 1, 0, 0, 0, 0, srv.loc)
		to = from.AddDate(0, 1, -1)
		prev, next = from.AddDate(0, -1, 0), from.AddDate(0, 1, 0)
		prevLabel, nextLabel = prev.Format("January"), next.Format("January")
		page.Title = from.Format("January 2006")
	}
	page.Prev, page.Next = link(prevLabel, org, view, prev, false), link(nextLabel, org, view, next, false)

	rows, err := srv.st.Report(org.ID, "", from, to, srv.now())
	if err != nil {
		serverError(w, "report", err)
		return
	}
	if view == "week" {
		page.Rows = rows
	} else {
		page.Weeks = monthWeeks(from, rows)
	}
	if page.pageBase, err = srv.base(r); err != nil {
		serverError(w, "device contacts", err)
		return
	}
	srv.render(w, http.StatusOK, "calendar.html", page)
}

// monthWeeks lays the month out Monday-first, listing in each day the late, absent or flagged results.
// rows must cover first through the month's last day.
func monthWeeks(first time.Time, rows []Row) [][]monthCell {
	lead := isoWeekday(first) - 1
	cells := make([]monthCell, lead)
	for d := first; d.Month() == first.Month(); d = d.AddDate(0, 0, 1) {
		cells = append(cells, monthCell{Date: d})
	}
	for _, row := range rows {
		for i, res := range row.Days {
			if res.Status == StatusLate || res.Status == StatusAbsent || len(res.Flags) > 0 {
				cells[lead+i].Entries = append(cells[lead+i].Entries, entry{row.Employee, res})
			}
		}
	}
	for len(cells)%7 != 0 {
		cells = append(cells, monthCell{})
	}
	return slices.Collect(slices.Chunk(cells, 7))
}
