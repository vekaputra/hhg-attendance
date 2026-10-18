package main

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Data entry pages: employees, organisations and shifts, leave, holidays. An invalid form re-renders
// its page with status 400 and a message and saves nothing; a saved one redirects back to the page.

// maxForm caps every POST body; the forms are a few short fields.
const maxForm = 16 << 10

const badName = "name must be 1 to 100 characters"

var employeeIDRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

// dayNames are the weekday checkboxes; ISO weekday 1 (Monday) is first.
var dayNames = [...]string{"Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"}

func (srv *server) adminRoutes(mux *http.ServeMux) {
	post := func(pattern string, h http.HandlerFunc) {
		mux.HandleFunc("POST "+pattern, func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, maxForm)
			h(w, r)
		})
	}
	mux.HandleFunc("GET /employees", func(w http.ResponseWriter, r *http.Request) { srv.showEmployees(w, r, http.StatusOK, "") })
	post("/employees", srv.saveEmployee)
	post("/employees/{id}", srv.saveEmployee)
	post("/employees/{id}/deactivate", srv.deactivateEmployee)
	post("/employees/{id}/reactivate", srv.reactivateEmployee)
	mux.HandleFunc("GET /orgs", func(w http.ResponseWriter, r *http.Request) { srv.showOrgs(w, r, http.StatusOK, "") })
	post("/orgs", srv.saveOrg)
	post("/orgs/{id}/shifts", srv.saveShift)
	mux.HandleFunc("GET /leave", func(w http.ResponseWriter, r *http.Request) { srv.showLeave(w, r, http.StatusOK, "") })
	post("/leave", srv.addLeave)
	post("/leave/{id}/delete", srv.deleteLeave)
	mux.HandleFunc("GET /holidays", func(w http.ResponseWriter, r *http.Request) { srv.showHolidays(w, r, http.StatusOK, "") })
	post("/holidays", srv.addHoliday)
	post("/holidays/{day}/delete", srv.deleteHoliday)
	mux.HandleFunc("GET /roster", srv.showRoster)
	post("/roster", srv.setRoster)
}

// today is the current attendance day, which turns over at dayStartHour.
func (srv *server) today() time.Time { return attendanceDate(srv.now().In(srv.loc)) }

func validName(s string) bool {
	n := utf8.RuneCountInString(s)
	return n >= 1 && n <= 100
}

// timeWindow checks a start and an end as clockFmt that differ, and returns them zero-padded ("8:00" is "08:00")
// so they sort as text. The window must fit in one attendance day: it can't start before dayStartHour, and if it
// runs overnight it can't end after dayStartHour, or its taps would split across two days.
func timeWindow(start, end string) (string, string, error) {
	s, err := time.Parse(clockFmt, start)
	if err != nil {
		return "", "", errors.New("bad start time")
	}
	e, err := time.Parse(clockFmt, end)
	if err != nil {
		return "", "", errors.New("bad end time")
	}
	if s.Equal(e) {
		return "", "", errors.New("start and end must differ")
	}
	dayStart := time.Date(0, 1, 1, dayStartHour, 0, 0, 0, time.UTC) // time.Parse(clockFmt) dates are year 0
	if s.Before(dayStart) {
		return "", "", fmt.Errorf("start must be %s or later: the attendance day begins then", dayStart.Format(clockFmt))
	}
	if e.Before(s) && e.After(dayStart) {
		return "", "", fmt.Errorf("a window that runs past midnight must end by %s, when the attendance day ends", dayStart.Format(clockFmt))
	}
	return s.Format(clockFmt), e.Format(clockFmt), nil
}

// orgByID finds the organisation whose ID is the decimal string v.
func orgByID(orgs []Org, v string) (Org, bool) {
	i := slices.IndexFunc(orgs, func(o Org) bool { return strconv.FormatInt(o.ID, 10) == v })
	if i < 0 {
		return Org{}, false
	}
	return orgs[i], true
}

// employeeByID finds the employee with the given ID.
func employeeByID(emps []Employee, id string) (Employee, bool) {
	i := slices.IndexFunc(emps, func(e Employee) bool { return e.ID == id })
	if i < 0 {
		return Employee{}, false
	}
	return emps[i], true
}

// Employees

type employeesPage struct {
	pageBase
	Error     string
	Employees []Employee
	Orgs      []Org
}

func (srv *server) showEmployees(w http.ResponseWriter, r *http.Request, status int, msg string) {
	emps, err := srv.st.Employees(0)
	if err != nil {
		serverError(w, "employees", err)
		return
	}
	orgs, err := srv.st.Orgs()
	if err != nil {
		serverError(w, "orgs", err)
		return
	}
	page := employeesPage{Error: msg, Employees: emps, Orgs: orgs}
	if page.pageBase, err = srv.base(r); err != nil {
		serverError(w, "device contacts", err)
		return
	}
	srv.render(w, status, "employees.html", page)
}

// saveEmployee adds the employee, or edits the name and organisation of the one in the path.
func (srv *server) saveEmployee(w http.ResponseWriter, r *http.Request) {
	reject := func(msg string) { srv.showEmployees(w, r, http.StatusBadRequest, msg) }
	id := r.PathValue("id")
	editing := id != ""
	if !editing {
		if id = r.PostFormValue("id"); !employeeIDRE.MatchString(id) {
			reject("bad employee ID: use 1 to 32 letters, digits, - or _")
			return
		}
	}
	emps, err := srv.st.Employees(0)
	if err != nil {
		serverError(w, "employees", err)
		return
	}
	_, exists := employeeByID(emps, id)
	if editing && !exists {
		reject("unknown employee")
		return
	}
	name := strings.TrimSpace(r.PostFormValue("name"))
	if !validName(name) {
		reject(badName)
		return
	}
	orgs, err := srv.st.Orgs()
	if err != nil {
		serverError(w, "orgs", err)
		return
	}
	org, ok := orgByID(orgs, r.PostFormValue("org"))
	if !ok {
		reject("unknown organisation")
		return
	}
	if !editing && exists {
		reject("employee " + id + " already exists")
		return
	}
	if editing {
		err = srv.st.UpdateEmployee(id, name, org.ID)
	} else {
		err = srv.st.AddEmployee(Employee{ID: id, Name: name, OrgID: org.ID, ActiveFrom: srv.today().Format(dateFmt)})
	}
	if err != nil {
		serverError(w, "save employee", err)
		return
	}
	http.Redirect(w, r, "/employees", http.StatusSeeOther)
}

// deactivateEmployee makes today the employee's last active day.
func (srv *server) deactivateEmployee(w http.ResponseWriter, r *http.Request) {
	reject := func(msg string) { srv.showEmployees(w, r, http.StatusBadRequest, msg) }
	emps, err := srv.st.Employees(0)
	if err != nil {
		serverError(w, "employees", err)
		return
	}
	id := r.PathValue("id")
	emp, ok := employeeByID(emps, id)
	if !ok {
		reject("unknown employee")
		return
	}
	if emp.ActiveUntil != "" {
		reject(id + " is already deactivated")
		return
	}
	if err := srv.st.DeactivateEmployee(id, srv.today()); err != nil {
		serverError(w, "deactivate employee", err)
		return
	}
	http.Redirect(w, r, "/employees", http.StatusSeeOther)
}

// reactivateEmployee clears a deactivated employee's last active day.
func (srv *server) reactivateEmployee(w http.ResponseWriter, r *http.Request) {
	reject := func(msg string) { srv.showEmployees(w, r, http.StatusBadRequest, msg) }
	emps, err := srv.st.Employees(0)
	if err != nil {
		serverError(w, "employees", err)
		return
	}
	id := r.PathValue("id")
	emp, ok := employeeByID(emps, id)
	if !ok {
		reject("unknown employee")
		return
	}
	if emp.ActiveUntil == "" {
		reject(id + " is not deactivated")
		return
	}
	if err := srv.st.ReactivateEmployee(id); err != nil {
		serverError(w, "reactivate employee", err)
		return
	}
	http.Redirect(w, r, "/employees", http.StatusSeeOther)
}

// Organisations and shifts

type orgsPage struct {
	pageBase
	Error string
	Orgs  []orgView
	New   orgView // the blank form that adds an organisation
}

type orgView struct {
	Org
	Days     []dayBox
	Shifts   []Shift
	NewShift Shift // the blank form that adds a shift to this organisation
}

type dayBox struct {
	N    int
	Name string
	On   bool
}

func dayBoxes(weekdays string) []dayBox {
	boxes := make([]dayBox, len(dayNames))
	for i, name := range dayNames {
		boxes[i] = dayBox{i + 1, name, strings.Contains(weekdays, strconv.Itoa(i+1))}
	}
	return boxes
}

func (srv *server) showOrgs(w http.ResponseWriter, r *http.Request, status int, msg string) {
	orgs, err := srv.st.Orgs()
	if err != nil {
		serverError(w, "orgs", err)
		return
	}
	page := orgsPage{Error: msg, New: orgView{Days: dayBoxes("")}}
	for _, o := range orgs {
		v := orgView{Org: o, Days: dayBoxes(o.Weekdays), NewShift: Shift{OrgID: o.ID}}
		if v.Shifts, err = srv.st.Shifts(o.ID); err != nil {
			serverError(w, "shifts", err)
			return
		}
		page.Orgs = append(page.Orgs, v)
	}
	if page.pageBase, err = srv.base(r); err != nil {
		serverError(w, "device contacts", err)
		return
	}
	srv.render(w, status, "orgs.html", page)
}

// saveOrg adds the organisation, or updates it when id is set. A roster organisation keeps no times or weekdays.
func (srv *server) saveOrg(w http.ResponseWriter, r *http.Request) {
	reject := func(msg string) { srv.showOrgs(w, r, http.StatusBadRequest, msg) }
	orgs, err := srv.st.Orgs()
	if err != nil {
		serverError(w, "orgs", err)
		return
	}
	org := Org{Name: strings.TrimSpace(r.PostFormValue("name")), Kind: r.PostFormValue("kind")}
	var cur Org // the organisation being updated
	if v := r.PostFormValue("id"); v != "" {
		var ok bool
		cur, ok = orgByID(orgs, v)
		if !ok {
			reject("unknown organisation")
			return
		}
		org.ID = cur.ID
	}
	if !validName(org.Name) {
		reject(badName)
		return
	}
	if slices.ContainsFunc(orgs, func(o Org) bool { return o.Name == org.Name && o.ID != org.ID }) {
		reject("an organisation with that name already exists")
		return
	}
	if org.Kind != "fixed" && org.Kind != "roster" {
		reject("kind must be fixed or roster")
		return
	}
	if org.ID != 0 && org.Kind != cur.Kind { // it would re-evaluate every past day
		reject("an organisation's kind can't be changed")
		return
	}
	if org.GraceMinutes, err = strconv.Atoi(r.PostFormValue("grace")); err != nil || org.GraceMinutes < 0 || org.GraceMinutes > 240 {
		reject("grace must be 0 to 240 minutes")
		return
	}
	if org.Kind == "fixed" {
		if org.Start, org.End, err = timeWindow(r.PostFormValue("start"), r.PostFormValue("end")); err != nil {
			reject(err.Error())
			return
		}
		for i := range dayNames {
			if d := strconv.Itoa(i + 1); slices.Contains(r.PostForm["weekdays"], d) { // PostFormValue has parsed the form
				org.Weekdays += d
			}
		}
		if org.Weekdays == "" {
			reject("tick at least one weekday")
			return
		}
	}
	if _, err := srv.st.SaveOrg(org); err != nil {
		serverError(w, "save org", err)
		return
	}
	http.Redirect(w, r, "/orgs", http.StatusSeeOther)
}

// saveShift adds a shift to a roster organisation, or updates it when shift_id is set.
func (srv *server) saveShift(w http.ResponseWriter, r *http.Request) {
	reject := func(msg string) { srv.showOrgs(w, r, http.StatusBadRequest, msg) }
	orgs, err := srv.st.Orgs()
	if err != nil {
		serverError(w, "orgs", err)
		return
	}
	org, ok := orgByID(orgs, r.PathValue("id"))
	if !ok {
		reject("unknown organisation")
		return
	}
	if org.Kind != "roster" {
		reject("only roster organisations have shifts")
		return
	}
	sh := Shift{OrgID: org.ID, Name: strings.TrimSpace(r.PostFormValue("name"))}
	if !validName(sh.Name) {
		reject(badName)
		return
	}
	if sh.Start, sh.End, err = timeWindow(r.PostFormValue("start"), r.PostFormValue("end")); err != nil {
		reject(err.Error())
		return
	}
	if v := r.PostFormValue("shift_id"); v != "" {
		shifts, err := srv.st.Shifts(org.ID)
		if err != nil {
			serverError(w, "shifts", err)
			return
		}
		i := slices.IndexFunc(shifts, func(s Shift) bool { return strconv.FormatInt(s.ID, 10) == v })
		if i < 0 {
			reject("unknown shift")
			return
		}
		sh.ID = shifts[i].ID
	}
	if _, err := srv.st.SaveShift(sh); err != nil {
		serverError(w, "save shift", err)
		return
	}
	http.Redirect(w, r, "/orgs", http.StatusSeeOther)
}

// Leave

type leavePage struct {
	pageBase
	Error     string
	Employees []Employee
	Leaves    []leaveRow
}

type leaveRow struct {
	Leave
	Name string // the employee's name
}

// leaveWindow is the range of days the leave page lists; leave must overlap it to be seen and deleted.
func (srv *server) leaveWindow() (from, to time.Time) {
	today := srv.today()
	return today.AddDate(0, 0, -90), today.AddDate(0, 0, 365)
}

// showLeave lists the leave overlapping the 90 days before today through the next 365 days.
func (srv *server) showLeave(w http.ResponseWriter, r *http.Request, status int, msg string) {
	emps, err := srv.st.Employees(0)
	if err != nil {
		serverError(w, "employees", err)
		return
	}
	leaves, err := srv.st.Leaves(srv.leaveWindow())
	if err != nil {
		serverError(w, "leave", err)
		return
	}
	names := map[string]string{}
	for _, e := range emps {
		names[e.ID] = e.Name
	}
	page := leavePage{Error: msg, Employees: emps}
	for _, l := range leaves {
		page.Leaves = append(page.Leaves, leaveRow{l, names[l.EmployeeID]})
	}
	if page.pageBase, err = srv.base(r); err != nil {
		serverError(w, "device contacts", err)
		return
	}
	srv.render(w, status, "leave.html", page)
}

func (srv *server) addLeave(w http.ResponseWriter, r *http.Request) {
	reject := func(msg string) { srv.showLeave(w, r, http.StatusBadRequest, msg) }
	emps, err := srv.st.Employees(0)
	if err != nil {
		serverError(w, "employees", err)
		return
	}
	emp := r.PostFormValue("emp")
	if !slices.ContainsFunc(emps, func(e Employee) bool { return e.ID == emp }) {
		reject("unknown employee")
		return
	}
	from, err := time.ParseInLocation(dateFmt, r.PostFormValue("from"), srv.loc)
	if err != nil {
		reject("bad from date")
		return
	}
	to, err := time.ParseInLocation(dateFmt, r.PostFormValue("to"), srv.loc)
	if err != nil {
		reject("bad to date")
		return
	}
	if to.Before(from) {
		reject("leave cannot end before it starts")
		return
	}
	if lo, hi := srv.leaveWindow(); to.Before(lo) || from.After(hi) {
		reject(fmt.Sprintf("leave must fall at least partly between %s and %s, the days this page lists", lo.Format(dateFmt), hi.Format(dateFmt)))
		return
	}
	if err := srv.st.AddLeave(emp, from, to); err != nil {
		serverError(w, "add leave", err)
		return
	}
	http.Redirect(w, r, "/leave", http.StatusSeeOther)
}

// deleteLeave is idempotent: deleting leave that is already gone redirects like any other delete.
func (srv *server) deleteLeave(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad leave id", http.StatusBadRequest)
		return
	}
	if err := srv.st.DeleteLeave(id); err != nil {
		serverError(w, "delete leave", err)
		return
	}
	http.Redirect(w, r, "/leave", http.StatusSeeOther)
}

// Holidays

type holidaysPage struct {
	pageBase
	Error string
	Days  []string // dateFmt, ascending
}

// holidayWindow is the range of days the holidays page lists: 1 January last year through 31 December next year.
func (srv *server) holidayWindow() (from, to time.Time) {
	year := srv.today().Year()
	return time.Date(year-1, 1, 1, 0, 0, 0, 0, srv.loc), time.Date(year+1, 12, 31, 0, 0, 0, 0, srv.loc)
}

func (srv *server) showHolidays(w http.ResponseWriter, r *http.Request, status int, msg string) {
	days, err := srv.st.Holidays(srv.holidayWindow())
	if err != nil {
		serverError(w, "holidays", err)
		return
	}
	page := holidaysPage{Error: msg, Days: slices.Sorted(maps.Keys(days))}
	if page.pageBase, err = srv.base(r); err != nil {
		serverError(w, "device contacts", err)
		return
	}
	srv.render(w, status, "holidays.html", page)
}

func (srv *server) addHoliday(w http.ResponseWriter, r *http.Request) {
	reject := func(msg string) { srv.showHolidays(w, r, http.StatusBadRequest, msg) }
	day, err := time.ParseInLocation(dateFmt, r.PostFormValue("day"), srv.loc)
	if err != nil {
		reject("bad day")
		return
	}
	if lo, hi := srv.holidayWindow(); day.Before(lo) || day.After(hi) {
		reject(fmt.Sprintf("a holiday must be between %s and %s, the days this page lists", lo.Format(dateFmt), hi.Format(dateFmt)))
		return
	}
	have, err := srv.st.Holidays(day, day)
	if err != nil {
		serverError(w, "holidays", err)
		return
	}
	if len(have) > 0 {
		reject("that day is already a holiday")
		return
	}
	if err := srv.st.AddHoliday(day); err != nil {
		serverError(w, "add holiday", err)
		return
	}
	http.Redirect(w, r, "/holidays", http.StatusSeeOther)
}

// deleteHoliday is idempotent, like deleteLeave.
func (srv *server) deleteHoliday(w http.ResponseWriter, r *http.Request) {
	day, err := time.ParseInLocation(dateFmt, r.PathValue("day"), srv.loc)
	if err != nil {
		http.Error(w, "bad day", http.StatusBadRequest)
		return
	}
	if err := srv.st.DeleteHoliday(day); err != nil {
		serverError(w, "delete holiday", err)
		return
	}
	http.Redirect(w, r, "/holidays", http.StatusSeeOther)
}

// Roster

type rosterPage struct {
	pageBase
	Tabs       []navLink
	Title      string
	Prev, Next navLink
	Dates      []time.Time // Monday to Sunday
	Rows       []rosterRow
}

type rosterRow struct {
	Employee Employee
	Cells    []rosterCell // one per day
}

// rosterCell is one employee-day: a select of Off and the organisation's shifts, or a dash outside the employee's active period.
type rosterCell struct {
	Emp, Day string // Day is dateFmt
	Active   bool
	Options  []rosterOption
	Title    string // who last changed the day, and when
	Error    string // why the last change was refused
}

type rosterOption struct {
	Value              int64 // a shift ID, 0 for off, -1 for the old times
	Label              string
	Selected, Disabled bool
}

func (e Employee) activeOn(day string) bool {
	return day >= e.ActiveFrom && (e.ActiveUntil == "" || day <= e.ActiveUntil)
}

func shiftLabel(name, start, end string) string { return name + " · " + start + "–" + end }

// newRosterCell shows what is stored for the day. The shift is matched by name and times, because a roster day keeps
// the times it was set with; if the shift has since been edited, the stored times are shown as a disabled extra option.
func newRosterCell(emp Employee, day string, shifts []Shift, stored RosterDay) rosterCell {
	c := rosterCell{Emp: emp.ID, Day: day, Active: emp.activeOn(day)}
	if !c.Active {
		return c
	}
	matched := stored.Shift == "" // off
	c.Options = []rosterOption{{Label: "Off", Selected: matched}}
	for _, sh := range shifts {
		sel := !matched && sh.Name == stored.Shift && sh.Start == stored.Start && sh.End == stored.End
		matched = matched || sel
		c.Options = append(c.Options, rosterOption{Value: sh.ID, Label: shiftLabel(sh.Name, sh.Start, sh.End), Selected: sel})
	}
	if !matched {
		c.Options = append(c.Options, rosterOption{Value: -1, Label: shiftLabel(stored.Shift, stored.Start, stored.End) + " (old times)", Selected: true, Disabled: true})
	}
	if at, err := time.Parse(stampFmt, stored.ChangedAt); err == nil {
		c.Title = "changed by " + stored.ChangedBy + " on " + at.Format(dateFmt) + " at " + clock(at)
	}
	return c
}

// showRoster is the week grid for one roster organisation. The week is the one containing ?week, default next week.
func (srv *server) showRoster(w http.ResponseWriter, r *http.Request) {
	orgs, err := srv.st.Orgs()
	if err != nil {
		serverError(w, "orgs", err)
		return
	}
	orgs = slices.DeleteFunc(orgs, func(o Org) bool { return o.Kind != "roster" })
	if len(orgs) == 0 {
		http.Error(w, "no roster organisation", http.StatusNotFound)
		return
	}
	q := r.URL.Query()
	org := orgs[0]
	if v := q.Get("org"); v != "" {
		var ok bool
		if org, ok = orgByID(orgs, v); !ok {
			http.Error(w, "unknown roster organisation", http.StatusBadRequest)
			return
		}
	}
	day := srv.today().AddDate(0, 0, 7)
	if v := q.Get("week"); v != "" {
		if day, err = time.ParseInLocation(dateFmt, v, srv.loc); err != nil {
			http.Error(w, "bad week", http.StatusBadRequest)
			return
		}
	}
	mon := day.AddDate(0, 0, 1-isoWeekday(day))
	sun := mon.AddDate(0, 0, 6)

	link := func(label string, o Org, week time.Time, on bool) navLink {
		qs := url.Values{"org": {strconv.FormatInt(o.ID, 10)}, "week": {week.Format(dateFmt)}}
		return navLink{label, "/roster?" + qs.Encode(), on}
	}
	page := rosterPage{Title: mon.Format("Mon 2 Jan") + " – " + sun.Format("Mon 2 Jan 2006")}
	for _, o := range orgs {
		page.Tabs = append(page.Tabs, link(o.Name, o, mon, o.ID == org.ID))
	}
	page.Prev, page.Next = link("previous week", org, mon.AddDate(0, 0, -7), false), link("next week", org, mon.AddDate(0, 0, 7), false)
	for i := range 7 {
		page.Dates = append(page.Dates, mon.AddDate(0, 0, i))
	}

	emps, err := srv.st.Employees(org.ID)
	if err != nil {
		serverError(w, "employees", err)
		return
	}
	shifts, err := srv.st.Shifts(org.ID)
	if err != nil {
		serverError(w, "shifts", err)
		return
	}
	for _, e := range emps {
		if e.ActiveFrom > sun.Format(dateFmt) || (e.ActiveUntil != "" && e.ActiveUntil < mon.Format(dateFmt)) {
			continue
		}
		stored, err := srv.st.Roster(e.ID, mon, sun)
		if err != nil {
			serverError(w, "roster", err)
			return
		}
		row := rosterRow{Employee: e}
		for _, d := range page.Dates {
			ds := d.Format(dateFmt)
			row.Cells = append(row.Cells, newRosterCell(e, ds, shifts, stored[ds]))
		}
		page.Rows = append(page.Rows, row)
	}
	if page.pageBase, err = srv.base(r); err != nil {
		serverError(w, "device contacts", err)
		return
	}
	srv.render(w, http.StatusOK, "roster.html", page)
}

// setRoster sets one employee's shift for one day and answers with the re-rendered cell, which htmx swaps in.
// A refused change answers 400 with the cell as stored and the reason, except an unknown employee or a bad day,
// which no page can send and so get a plain message.
func (srv *server) setRoster(w http.ResponseWriter, r *http.Request) {
	emps, err := srv.st.Employees(0)
	if err != nil {
		serverError(w, "employees", err)
		return
	}
	i := slices.IndexFunc(emps, func(e Employee) bool { return e.ID == r.PostFormValue("emp") })
	if i < 0 {
		http.Error(w, "unknown employee", http.StatusBadRequest)
		return
	}
	emp := emps[i]
	orgs, err := srv.st.Orgs()
	if err != nil {
		serverError(w, "orgs", err)
		return
	}
	if !slices.ContainsFunc(orgs, func(o Org) bool { return o.ID == emp.OrgID && o.Kind == "roster" }) {
		http.Error(w, emp.ID+" is not on a roster", http.StatusBadRequest)
		return
	}
	day, err := time.ParseInLocation(dateFmt, r.PostFormValue("day"), srv.loc)
	if err != nil {
		http.Error(w, "bad day", http.StatusBadRequest)
		return
	}
	ds := day.Format(dateFmt)
	shifts, err := srv.st.Shifts(emp.OrgID)
	if err != nil {
		serverError(w, "shifts", err)
		return
	}

	show := func(status int, msg string) {
		stored, err := srv.st.Roster(emp.ID, day, day)
		if err != nil {
			serverError(w, "roster", err)
			return
		}
		cell := newRosterCell(emp, ds, shifts, stored[ds])
		cell.Error = msg
		var buf bytes.Buffer
		if err := srv.tmpl["roster.html"].ExecuteTemplate(&buf, "cell", cell); err != nil {
			serverError(w, "render cell", err)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(status)
		buf.WriteTo(w)
	}
	if !emp.activeOn(ds) {
		show(http.StatusBadRequest, "employee was not active that day")
		return
	}
	shiftID, err := strconv.ParseInt(r.PostFormValue("shift"), 10, 64)
	if err != nil || (shiftID != 0 && !slices.ContainsFunc(shifts, func(s Shift) bool { return s.ID == shiftID })) {
		show(http.StatusBadRequest, "unknown shift")
		return
	}
	if err := srv.st.SetRoster(emp.ID, day, shiftID, currentUser(r), srv.now()); err != nil {
		serverError(w, "set roster", err)
		return
	}
	show(http.StatusOK, "")
}
