package main

import (
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

type partTimePage struct {
	pageBase
	Error      string
	Month      string        // YYYY-MM of the shown period
	Title      string        // "26 Oct – 25 Nov 2026"
	Months     []monthOption // dropdown, oldest first
	Days       []time.Time   // first..last
	Rows       []ptRow
	Total      ptTotal
	Wage       int64
	Breaks     []breakRow // this period's breaks, for the list
	PartTimers []Employee // for the break form's employee dropdown
	NewBreak   breakForm  // the break dialog's values
	Reopen     string     // id of the dialog to reopen with Error inside it; "" shows Error above the cards
}

// breakForm is the add-break dialog's fields, as submitted.
type breakForm struct{ Emp, StartDate, StartTime, EndDate, EndTime string }

type monthOption struct {
	Value, Label string
	Selected     bool
}

type ptRow struct {
	Emp               Employee
	Cells             []ptCell // one per period day
	DaysWorked, Hours int      // days with Hours > 0, sum of Hours
	Pay               int64
	Unsure            bool // any ? or … day
}

// ptCell is one grid square; Active is false outside the employee's active period, and the cell is left blank.
type ptCell struct {
	Active  bool
	Day     ptDay
	In, Out string // the fill-in dialog's values, clockFmt or empty
}

type ptTotal struct {
	DaysWorked, Hours int
	Pay               int64
	Unsure            bool
}

// breakRow is one line of the break list. Start and End read "00:30 (+1)" when they fall on the next calendar day.
type breakRow struct {
	ID              int64
	Emp             Employee
	Day, Start, End string
	By              string
}

// Mark is what the grid cell shows.
func (d ptDay) Mark() string {
	switch d.State {
	case ptHours:
		return strconv.Itoa(d.Hours)
	case ptMissing:
		return "?"
	case ptOpen:
		return "…"
	}
	return "–"
}

// Detail is the cell's hover and tap text, one line per fact.
func (d ptDay) Detail() []string {
	var lines []string
	if d.State == ptHours {
		var ss []string
		for _, s := range d.Sessions {
			ss = append(ss, clock(s.Start)+" → "+clock(s.End))
		}
		if len(ss) == 0 { // the breaks cover the whole day
			ss = []string{clock(d.In) + " → " + clock(d.Out)}
		}
		lines = append(lines, strings.Join(ss, ", "))
		worked := "worked " + hours(d.Worked)
		if d.Break > 0 {
			worked += " · break " + hours(d.Break)
		}
		lines = append(lines, worked)
		if d.Capped {
			lines = append(lines, "(capped at "+strconv.Itoa(maxPaidHours)+")")
		}
	} else {
		known := func(t time.Time) string {
			if t.IsZero() {
				return "?"
			}
			return clock(t)
		}
		lines = append(lines, known(d.In)+" → "+known(d.Out))
		if d.State == ptOpen {
			lines = append(lines, "day not over yet")
		}
	}
	if d.Edited {
		lines = append(lines, "manager edit")
	}
	return lines
}

// DetailText is Detail for a title attribute.
func (d ptDay) DetailText() string { return strings.Join(d.Detail(), "\n") }

// periodTitle is "26 Oct – 25 Nov 2026", with both years when the period spans two.
func periodTitle(first, last time.Time) string {
	if first.Year() != last.Year() {
		return first.Format("2 Jan 2006") + " – " + last.Format("2 Jan 2006")
	}
	return first.Format("2 Jan") + " – " + last.Format("2 Jan 2006")
}

// breakClock is t's clock time, marked "(+1)" when it is on the calendar day after day.
func breakClock(day, t time.Time) string {
	if t.Format(dateFmt) != day.Format(dateFmt) {
		return clock(t) + " (+1)"
	}
	return clock(t)
}

// partTime shows one pay period's part-timers: a summary of days, hours and pay, the daily grid, and the breaks.
func (srv *server) partTime(w http.ResponseWriter, r *http.Request) {
	month, ok := srv.payMonthParam(w, r.URL.Query().Get("month"))
	if !ok {
		return
	}
	page, err := srv.partTimeData(r, month, srv.today())
	if err != nil {
		serverError(w, "part-time", err)
		return
	}
	srv.render(w, http.StatusOK, "parttime.html", page)
}

// payMonthParam parses a period's month, empty meaning the default one. A bad value has been answered with 400.
func (srv *server) payMonthParam(w http.ResponseWriter, v string) (time.Time, bool) {
	if v == "" {
		return defaultPayMonth(srv.today()), true
	}
	month, err := time.ParseInLocation(monthFmt, v, srv.loc)
	if err != nil {
		http.Error(w, "bad month", http.StatusBadRequest)
		return month, false
	}
	return month, true
}

// partTimeReject re-renders month's page with 400 and msg. reopen, when given, puts the submitted values back in a
// dialog and returns that dialog's id, or "" when the page has no such dialog.
func (srv *server) partTimeReject(w http.ResponseWriter, r *http.Request, month time.Time, msg string, reopen func(*partTimePage) string) {
	page, err := srv.partTimeData(r, month, srv.today())
	if err != nil {
		serverError(w, "part-time", err)
		return
	}
	page.Error = msg
	if reopen != nil {
		page.Reopen = reopen(&page)
	}
	srv.render(w, http.StatusBadRequest, "parttime.html", page)
}

// partTimeCorrect fills a day's missing check-in or check-out, exactly as the People page's correction form does.
func (srv *server) partTimeCorrect(w http.ResponseWriter, r *http.Request) {
	month, ok := srv.payMonthParam(w, r.PostFormValue("month"))
	if !ok {
		return
	}
	emps, err := srv.st.Employees(0)
	if err != nil {
		serverError(w, "employees", err)
		return
	}
	emp, day, in, out := r.PostFormValue("emp"), r.PostFormValue("day"), r.PostFormValue("in"), r.PostFormValue("out")
	_, msg, err := srv.applyCorrection(emps, emp, day, in, out, currentUser(r))
	if err != nil {
		serverError(w, "correction", err)
		return
	}
	if msg == "" {
		http.Redirect(w, r, "/part-time?month="+month.Format(monthFmt), http.StatusSeeOther)
		return
	}
	srv.partTimeReject(w, r, month, msg, func(p *partTimePage) string {
		for ri, row := range p.Rows {
			for ci, c := range row.Cells {
				// Only ? and … cells have a fill-in dialog.
				if row.Emp.ID == emp && c.Active && c.Day.Date.Format(dateFmt) == day && (c.Day.State == ptMissing || c.Day.State == ptOpen) {
					p.Rows[ri].Cells[ci].In, p.Rows[ri].Cells[ci].Out = in, out
					return fmt.Sprintf("pt-%d-%d", ri, ci)
				}
			}
		}
		return ""
	})
}

func (srv *server) partTimeAddBreak(w http.ResponseWriter, r *http.Request) {
	month, ok := srv.payMonthParam(w, r.PostFormValue("month"))
	if !ok {
		return
	}
	f := breakForm{r.PostFormValue("emp"), r.PostFormValue("start_date"), r.PostFormValue("start_time"),
		r.PostFormValue("end_date"), r.PostFormValue("end_time")}
	msg, err := srv.addBreak(f, currentUser(r))
	if err != nil {
		serverError(w, "add break", err)
		return
	}
	if msg == "" {
		http.Redirect(w, r, "/part-time?month="+month.Format(monthFmt), http.StatusSeeOther)
		return
	}
	srv.partTimeReject(w, r, month, msg, func(p *partTimePage) string {
		if len(p.PartTimers) == 0 { // no one shown, so the page has no break dialog
			return ""
		}
		p.NewBreak = f
		return "add-break"
	})
}

// addBreak validates and saves a break. msg is a user-facing rejection and nothing was saved; err is a server error.
func (srv *server) addBreak(f breakForm, by string) (msg string, err error) {
	emps, err := srv.st.Employees(0)
	if err != nil {
		return "", err
	}
	i := slices.IndexFunc(emps, func(e Employee) bool { return e.ID == f.Emp })
	if i < 0 {
		return "unknown employee", nil
	}
	emp := emps[i]
	if !emp.PartTime {
		return "breaks are only for part-time employees", nil
	}
	start, err := time.ParseInLocation(dateFmt+" "+clockFmt, f.StartDate+" "+f.StartTime, srv.loc)
	if err != nil {
		return "bad break start", nil
	}
	end, err := time.ParseInLocation(dateFmt+" "+clockFmt, f.EndDate+" "+f.EndTime, srv.loc)
	if err != nil {
		return "bad break end", nil
	}
	if !end.After(start) {
		return "break must end after it starts", nil
	}
	day := attendanceDate(start)
	if end.After(time.Date(day.Year(), day.Month(), day.Day()+1, dayStartHour, 0, 0, 0, srv.loc)) {
		return "a break must end by 03:00 the next morning", nil
	}
	if ds := day.Format(dateFmt); ds < emp.ActiveFrom || (emp.ActiveUntil != "" && ds > emp.ActiveUntil) {
		return "employee was not active that day", nil
	}
	_, err = srv.st.AddBreak(Break{EmployeeID: emp.ID, Day: day, Start: start, End: end, By: by, At: srv.now()})
	return "", err
}

func (srv *server) partTimeDeleteBreak(w http.ResponseWriter, r *http.Request) {
	month, ok := srv.payMonthParam(w, r.PostFormValue("month"))
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		srv.partTimeReject(w, r, month, "bad break", nil)
		return
	}
	if err := srv.st.DeleteBreak(id); err != nil {
		serverError(w, "delete break", err)
		return
	}
	http.Redirect(w, r, "/part-time?month="+month.Format(monthFmt), http.StatusSeeOther)
}

func (srv *server) partTimeSetWage(w http.ResponseWriter, r *http.Request) {
	month, ok := srv.payMonthParam(w, r.PostFormValue("month"))
	if !ok {
		return
	}
	wage, err := strconv.ParseInt(r.PostFormValue("wage"), 10, 64)
	if err != nil || wage < 1 || wage > 100000000 {
		srv.partTimeReject(w, r, month, "wage must be a whole number of rupiah", nil)
		return
	}
	if err := srv.st.SetPartTimeWage(wage); err != nil {
		serverError(w, "set wage", err)
		return
	}
	http.Redirect(w, r, "/part-time?month="+month.Format(monthFmt), http.StatusSeeOther)
}

func (srv *server) partTimeData(r *http.Request, month, today time.Time) (partTimePage, error) {
	first, last := payPeriod(month)
	page := partTimePage{Month: month.Format(monthFmt), Title: periodTitle(first, last),
		NewBreak: breakForm{StartDate: last.Format(dateFmt), EndDate: last.Format(dateFmt)}}
	for d := first; !d.After(last); d = d.AddDate(0, 0, 1) {
		page.Days = append(page.Days, d)
	}

	emps, err := srv.st.Employees(0)
	if err != nil {
		return page, err
	}
	var ptIDs []string
	for _, e := range emps {
		if e.PartTime {
			ptIDs = append(ptIDs, e.ID)
		}
	}
	// The dropdown runs from the earliest part-timer tap's period through the one containing today,
	// always including the default and the shown period.
	from, to := defaultPayMonth(today), payMonthOf(today)
	earliest, ok, err := srv.st.EarliestTap(ptIDs)
	if err != nil {
		return page, err
	}
	if e := payMonthOf(attendanceDate(earliest)); ok && e.Before(from) {
		from = e
	}
	if month.Before(from) {
		from = month
	}
	if month.After(to) {
		to = month
	}
	for m := from; !m.After(to); m = m.AddDate(0, 1, 0) {
		page.Months = append(page.Months, monthOption{m.Format(monthFmt), m.Format("January 2006"), m.Equal(month)})
	}

	if page.Wage, err = srv.st.PartTimeWage(); err != nil {
		return page, err
	}
	breaks, err := srv.st.Breaks(first, last)
	if err != nil {
		return page, err
	}
	breaksOn := map[string][]Break{} // by employee ID and day
	for _, b := range breaks {
		k := b.EmployeeID + " " + b.Day.Format(dateFmt)
		breaksOn[k] = append(breaksOn[k], b)
	}
	rows, err := srv.st.Report(0, "", first, last, srv.now())
	if err != nil {
		return page, err
	}
	shown := map[string]Employee{}
	for _, rep := range rows {
		e := rep.Employee
		if !e.PartTime {
			continue
		}
		row := ptRow{Emp: e}
		active := false
		for _, res := range rep.Days {
			ds := res.Date.Format(dateFmt)
			// Report evaluates taps outside the active period too; those days are blank and unpaid.
			if ds < e.ActiveFrom || (e.ActiveUntil != "" && ds > e.ActiveUntil) {
				row.Cells = append(row.Cells, ptCell{})
				continue
			}
			active = true
			d := partTimeDay(res, breaksOn[e.ID+" "+ds], page.Wage)
			c := ptCell{Active: true, Day: d}
			if !d.In.IsZero() {
				c.In = clock(d.In)
			}
			if !d.Out.IsZero() {
				c.Out = clock(d.Out)
			}
			row.Cells = append(row.Cells, c)
			if d.Hours > 0 {
				row.DaysWorked++
			}
			row.Hours += d.Hours
			row.Pay += d.Pay
			row.Unsure = row.Unsure || d.State == ptMissing || d.State == ptOpen
		}
		if !active {
			continue
		}
		page.Rows = append(page.Rows, row)
		page.PartTimers = append(page.PartTimers, e)
		shown[e.ID] = e
		page.Total.DaysWorked += row.DaysWorked
		page.Total.Hours += row.Hours
		page.Total.Pay += row.Pay
		page.Total.Unsure = page.Total.Unsure || row.Unsure
	}
	for _, b := range breaks {
		if e, ok := shown[b.EmployeeID]; ok {
			page.Breaks = append(page.Breaks, breakRow{ID: b.ID, Emp: e, Day: b.Day.Format("Mon 2 Jan"),
				Start: breakClock(b.Day, b.Start), End: breakClock(b.Day, b.End), By: b.By})
		}
	}
	page.pageBase, err = srv.base(r)
	return page, err
}
