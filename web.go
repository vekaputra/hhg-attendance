package main

import (
	"bytes"
	"cmp"
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
func clock(t time.Time) string { return t.Format(clockFmt) }

// mins rounds up, so a tap 40s late reads "1m late" and never "0m late".
func mins(d time.Duration) string { return fmt.Sprintf("%dm", int((d+time.Minute-1)/time.Minute)) }

// monthFmt is the month in URLs and forms.
const monthFmt = "2006-01"

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
	pages.HandleFunc("GET /person", srv.person)
	pages.HandleFunc("POST /corrections", srv.correct)
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
	w.Header().Set("Cache-Control", "no-store") // never replay a page from the browser's cache, e.g. after logout
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
	day := attendanceDate(srv.now().In(srv.loc))
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
	page := calendarPage{View: view, Month: day.Format(monthFmt)}
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

type personPage struct {
	pageBase
	Employees     []Employee
	Emp           Employee // zero when there are no employees
	Month         string   // YYYY-MM
	Title         string
	Prev, Next    navLink
	Row           *Row // nil when Emp was not active in the month
	DaysWithHours int
	Weeks         [][]*Result // Monday-first; nil cells are the blanks around the month
}

func personURL(empID string, month time.Time) string {
	return "/person?" + url.Values{"emp": {empID}, "month": {month.Format(monthFmt)}}.Encode()
}

// person shows one employee's month: totals, each day, and a form to correct a day.
// It defaults to the first employee and to the month of the current attendance day.
func (srv *server) person(w http.ResponseWriter, r *http.Request) {
	emps, err := srv.st.Employees(0)
	if err != nil {
		serverError(w, "employees", err)
		return
	}
	q := r.URL.Query()
	page := personPage{Employees: emps}
	if v := q.Get("emp"); v != "" {
		i := slices.IndexFunc(emps, func(e Employee) bool { return e.ID == v })
		if i < 0 {
			http.Error(w, "unknown employee", http.StatusBadRequest)
			return
		}
		page.Emp = emps[i]
	} else if len(emps) > 0 {
		page.Emp = emps[0]
	}
	y, m, _ := attendanceDate(srv.now().In(srv.loc)).Date()
	first := time.Date(y, m, 1, 0, 0, 0, 0, srv.loc)
	if v := q.Get("month"); v != "" {
		if first, err = time.ParseInLocation(monthFmt, v, srv.loc); err != nil {
			http.Error(w, "bad month", http.StatusBadRequest)
			return
		}
	}
	page.Month, page.Title = first.Format(monthFmt), first.Format("January 2006")

	if page.Emp.ID != "" {
		prev, next := first.AddDate(0, -1, 0), first.AddDate(0, 1, 0)
		page.Prev = navLink{Label: prev.Format("January"), URL: personURL(page.Emp.ID, prev)}
		page.Next = navLink{Label: next.Format("January"), URL: personURL(page.Emp.ID, next)}
		rows, err := srv.st.Report(0, page.Emp.ID, first, next.AddDate(0, 0, -1), srv.now())
		if err != nil {
			serverError(w, "report", err)
			return
		}
		if len(rows) > 0 {
			page.Row = &rows[0]
			for _, d := range page.Row.Days {
				if d.HasHours {
					page.DaysWithHours++
				}
			}
			page.Weeks = personWeeks(page.Row.Days)
		}
	}
	if page.pageBase, err = srv.base(r); err != nil {
		serverError(w, "device contacts", err)
		return
	}
	srv.render(w, http.StatusOK, "person.html", page)
}

// personWeeks lays a month's days out Monday-first; days must be non-empty.
func personWeeks(days []Result) [][]*Result {
	cells := make([]*Result, isoWeekday(days[0].Date)-1)
	for i := range days {
		cells = append(cells, &days[i])
	}
	for len(cells)%7 != 0 {
		cells = append(cells, nil)
	}
	return slices.Collect(slices.Chunk(cells, 7))
}

// attendanceTime puts a clockFmt value on an attendance day; a time before the day starts is the next calendar day.
// An empty value is the zero time.
func attendanceTime(day time.Time, v string) (time.Time, error) {
	if v == "" {
		return time.Time{}, nil
	}
	c, err := time.Parse(clockFmt, v)
	if err != nil {
		return time.Time{}, err
	}
	t := time.Date(day.Year(), day.Month(), day.Day(), c.Hour(), c.Minute(), 0, 0, day.Location())
	if c.Hour() < dayStartHour {
		t = t.AddDate(0, 0, 1)
	}
	return t, nil
}

// correct saves a manager's fix to one day's check-in and/or check-out and goes back to that month.
// A field left empty keeps the tap-derived time; with both empty the day's correction is removed.
func (srv *server) correct(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	emps, err := srv.st.Employees(0)
	if err != nil {
		serverError(w, "employees", err)
		return
	}
	id := r.PostFormValue("emp")
	if !slices.ContainsFunc(emps, func(e Employee) bool { return e.ID == id }) {
		http.Error(w, "unknown employee", http.StatusBadRequest)
		return
	}
	day, err := time.ParseInLocation(dateFmt, r.PostFormValue("day"), srv.loc)
	if err != nil {
		http.Error(w, "bad day", http.StatusBadRequest)
		return
	}
	in, err := attendanceTime(day, r.PostFormValue("in"))
	if err != nil {
		http.Error(w, "bad check-in time", http.StatusBadRequest)
		return
	}
	out, err := attendanceTime(day, r.PostFormValue("out"))
	if err != nil {
		http.Error(w, "bad check-out time", http.StatusBadRequest)
		return
	}

	// Judge the result of the edit: a field left empty keeps the day's current value.
	rows, err := srv.st.Report(0, id, day, day, srv.now())
	if err != nil {
		serverError(w, "report", err)
		return
	}
	if len(rows) == 0 {
		http.Error(w, "employee was not active that day", http.StatusBadRequest)
		return
	}
	cur := rows[0].Days[0]
	effIn, effOut := cmp.Or(in, cur.In), cmp.Or(out, cur.Out)
	if !effIn.IsZero() && !effOut.IsZero() && !effOut.After(effIn) {
		http.Error(w, "check-out must be after check-in", http.StatusBadRequest)
		return
	}

	err = srv.st.SaveCorrection(Correction{EmployeeID: id, Day: day, In: in, Out: out, By: currentUser(r), At: srv.now()})
	if err != nil {
		serverError(w, "save correction", err)
		return
	}
	http.Redirect(w, r, personURL(id, day), http.StatusSeeOther)
}
