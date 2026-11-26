package main

import (
	"net/http"
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
}

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
	Active bool
	Day    ptDay
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
	today := srv.today()
	month := defaultPayMonth(today)
	if v := r.URL.Query().Get("month"); v != "" {
		var err error
		if month, err = time.ParseInLocation(monthFmt, v, srv.loc); err != nil {
			http.Error(w, "bad month", http.StatusBadRequest)
			return
		}
	}
	page, err := srv.partTimeData(r, month, today)
	if err != nil {
		serverError(w, "part-time", err)
		return
	}
	srv.render(w, http.StatusOK, "parttime.html", page)
}

func (srv *server) partTimeData(r *http.Request, month, today time.Time) (partTimePage, error) {
	first, last := payPeriod(month)
	page := partTimePage{Month: month.Format(monthFmt), Title: periodTitle(first, last)}
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
			row.Cells = append(row.Cells, ptCell{Active: true, Day: d})
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
