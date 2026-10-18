package main

import (
	"slices"
	"strconv"
	"strings"
	"time"
)

// Row is one employee's report: a Result per day plus totals over the days with hours.
// Flagged counts days missing a check-in or check-out.
type Row struct {
	Employee Employee
	Org      Org
	Days     []Result
	Actual   time.Duration
	InShift  time.Duration
	Late     int
	Absent   int
	Flagged  int
}

// Report evaluates every day from through to for one employee, or for everyone in orgID (all when 0).
// With employeeID set, orgID is ignored. Employees whose active period ended before from are left out.
func (s *Store) Report(orgID int64, employeeID string, from, to time.Time, now time.Time) ([]Row, error) {
	now = now.In(s.loc)
	// Re-anchor the calendar dates in s.loc; attendance days run 03:00 to 03:00.
	fy, fm, fd := from.Date()
	ty, tm, td := to.Date()
	first := time.Date(fy, fm, fd, 0, 0, 0, 0, s.loc)
	last := time.Date(ty, tm, td, 0, 0, 0, 0, s.loc)
	tapsFrom := first.Add(dayStartHour * time.Hour)
	tapsTo := last.AddDate(0, 0, 1).Add(dayStartHour * time.Hour)

	orgs, err := s.Orgs()
	if err != nil {
		return nil, err
	}
	if employeeID != "" {
		orgID = 0
	}
	emps, err := s.Employees(orgID)
	if err != nil {
		return nil, err
	}
	holidays, err := s.Holidays(first, last)
	if err != nil {
		return nil, err
	}
	leaves, err := s.Leaves(first, last)
	if err != nil {
		return nil, err
	}

	var rows []Row
	for _, e := range emps {
		if (employeeID != "" && e.ID != employeeID) || (e.ActiveUntil != "" && e.ActiveUntil < first.Format(dateFmt)) {
			continue
		}
		oi := slices.IndexFunc(orgs, func(o Org) bool { return o.ID == e.OrgID })
		if oi < 0 {
			continue // unreachable while the employees.org_id foreign key holds
		}
		org := orgs[oi]
		roster := org.Kind == "roster"

		taps, err := s.Taps(e.ID, tapsFrom, tapsTo)
		if err != nil {
			return nil, err
		}
		tapsByDay := map[string][]time.Time{}
		for _, t := range taps {
			d := attendanceDate(t).Format(dateFmt)
			tapsByDay[d] = append(tapsByDay[d], t)
		}
		fixes, err := s.Corrections(e.ID, first, last)
		if err != nil {
			return nil, err
		}
		var rosterDays map[string]RosterDay
		if roster {
			if rosterDays, err = s.Roster(e.ID, first, last); err != nil {
				return nil, err
			}
		}

		row := Row{Employee: e, Org: org}
		grace := time.Duration(org.GraceMinutes) * time.Minute
		for date := first; !date.After(last); date = date.AddDate(0, 0, 1) {
			ds := date.Format(dateFmt)
			d := Day{Date: date, Roster: roster, Taps: tapsByDay[ds]}
			if f, ok := fixes[ds]; ok {
				d.Fix = &f
			}
			if ds >= e.ActiveFrom && (e.ActiveUntil == "" || ds <= e.ActiveUntil) {
				d.Leave = slices.ContainsFunc(leaves, func(l Leave) bool { return l.EmployeeID == e.ID && l.From <= ds && ds <= l.To })
				d.Holiday = !roster && holidays[ds]
				if !d.Leave && !d.Holiday {
					if roster {
						if r := rosterDays[ds]; r.Start != "" {
							w := windowOn(date, r.Start, r.End, r.Shift)
							d.Window = &w
						}
					} else if strings.Contains(org.Weekdays, strconv.Itoa(isoWeekday(date))) {
						w := windowOn(date, org.Start, org.End, "")
						d.Window = &w
					}
				}
			}

			res := evaluate(d, grace, now)
			row.Days = append(row.Days, res)
			if res.HasHours {
				row.Actual += res.Actual
				row.InShift += res.InShift
			}
			switch res.Status {
			case StatusLate:
				row.Late++
			case StatusAbsent:
				row.Absent++
			}
			if slices.Contains(res.Flags, FlagNoCheckIn) || slices.Contains(res.Flags, FlagNoCheckOut) {
				row.Flagged++
			}
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// isoWeekday returns 1 = Monday .. 7 = Sunday.
func isoWeekday(t time.Time) int {
	if wd := int(t.Weekday()); wd != 0 {
		return wd
	}
	return 7
}
