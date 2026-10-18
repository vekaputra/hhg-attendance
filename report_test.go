package main

import (
	"slices"
	"strconv"
	"testing"
	"time"
)

func newReportStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenStore(t.TempDir()+"/a.db", testLoc)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// date is midnight of a dateFmt day in testLoc.
func date(s string) time.Time { return at(s + " 00:00:00") }

// on returns the row's result for a dateFmt day.
func on(t *testing.T, r Row, day string) Result {
	t.Helper()
	for _, res := range r.Days {
		if res.Date.Format(dateFmt) == day {
			return res
		}
	}
	t.Fatalf("%s has no result for %s", r.Employee.ID, day)
	return Result{}
}

func TestReport(t *testing.T) {
	s := newReportStore(t)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	orgs, err := s.Orgs()
	must(err)
	office, cafe := orgs[0], orgs[1]
	shifts, err := s.Shifts(cafe.ID) // ordered by start: 1, 2, 3
	must(err)
	shift2, shift3 := shifts[1], shifts[2]

	must(s.AddEmployee(Employee{ID: "HHG0001", Name: "Ann", OrgID: office.ID, ActiveFrom: "2026-10-01"}))
	must(s.AddEmployee(Employee{ID: "HHG0012", Name: "Bob", OrgID: cafe.ID, ActiveFrom: "2026-10-01"}))
	must(s.AddHoliday(date("2026-10-07")))
	must(s.AddLeave("HHG0001", date("2026-10-08"), date("2026-10-09")))
	for _, r := range []struct {
		day   string
		shift int64
	}{{"2026-10-05", shift2.ID}, {"2026-10-31", shift3.ID}, {"2026-10-06", shift2.ID}} {
		must(s.SetRoster("HHG0012", date(r.day), r.shift, "mgr", at("2026-10-01 09:00:00")))
	}
	shift2.Start, shift2.End = "12:00", "21:00"
	_, err = s.SaveShift(shift2)
	must(err)
	for i, v := range []string{"2026-10-31 14:55:00", "2026-11-01 01:00:00"} {
		must(s.InsertTap(Tap{DeviceID: "d", SerialNo: strconv.Itoa(i), EmployeeID: "HHG0012", Time: at(v)}, at(v)))
	}
	now := time.Date(2026, 11, 2, 5, 0, 0, 0, time.UTC)

	rows, err := s.Report(office.ID, "", date("2026-09-28"), date("2026-10-11"), now)
	must(err)
	if len(rows) != 1 || rows[0].Employee.ID != "HHG0001" || len(rows[0].Days) != 14 {
		t.Fatalf("office report = %d rows, want 1 row of 14 days", len(rows))
	}
	for day, want := range map[string]Status{
		"2026-09-30": StatusOff, // before the active period
		"2026-10-03": StatusOff, // Saturday
		"2026-10-05": StatusAbsent,
		"2026-10-07": StatusHoliday,
		"2026-10-08": StatusLeave,
	} {
		if got := on(t, rows[0], day).Status; got != want {
			t.Errorf("HHG0001 %s = %q, want %q", day, got, want)
		}
	}

	rows, err = s.Report(0, "HHG0012", date("2026-10-01"), date("2026-10-31"), now)
	must(err)
	if len(rows) != 1 {
		t.Fatalf("cafe report = %d rows, want 1", len(rows))
	}
	row := rows[0]
	if got := on(t, row, "2026-10-05").Status; got != StatusAbsent {
		t.Errorf("10-05 = %q, want absent", got)
	}
	if w := on(t, row, "2026-10-06").Window; w == nil || w.Start.Format(clockFmt) != "11:00" || w.End.Format(clockFmt) != "20:00" {
		t.Errorf("10-06 window = %+v, want the 11:00-20:00 snapshot", w)
	}
	if out := on(t, row, "2026-10-31").Out; !is(out, "2026-11-01 01:00:00") {
		t.Errorf("10-31 out = %v, want 2026-11-01 01:00", out)
	}
	if want := 10*time.Hour + 5*time.Minute; row.Actual != want {
		t.Errorf("Actual = %v, want %v", row.Actual, want)
	}

	// 19:00 local, before the 20:00 snapshot end: not yet absent.
	rows, err = s.Report(0, "HHG0012", date("2026-10-05"), date("2026-10-05"), time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC))
	must(err)
	if got := rows[0].Days[0].Status; got != StatusPending {
		t.Errorf("10-05 at 19:00 = %q, want pending", got)
	}
}

func TestReportEdits(t *testing.T) {
	s := newReportStore(t)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	orgs, err := s.Orgs()
	must(err)
	office := orgs[0]
	must(s.AddEmployee(Employee{ID: "A", Name: "Ann", OrgID: office.ID, ActiveFrom: "2026-10-01"}))
	must(s.AddEmployee(Employee{ID: "B", Name: "Bea", OrgID: office.ID, ActiveFrom: "2026-10-01"}))
	now := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)

	if err := s.AddLeave("A", date("2026-10-09"), date("2026-10-08")); err == nil {
		t.Error("AddLeave with to before from succeeded")
	}
	must(s.AddLeave("A", date("2026-10-08"), date("2026-10-09")))
	for _, c := range []struct {
		from, to string
		want     int
	}{{"2026-10-01", "2026-10-07", 0}, {"2026-10-01", "2026-10-08", 1}, {"2026-10-09", "2026-10-12", 1}, {"2026-10-10", "2026-10-12", 0}} {
		if got, err := s.Leaves(date(c.from), date(c.to)); err != nil || len(got) != c.want {
			t.Errorf("Leaves(%s, %s) = %v, %v; want %d", c.from, c.to, got, err, c.want)
		}
	}

	// A correction reaches the report; saving an empty one removes it.
	must(s.SaveCorrection(Correction{EmployeeID: "A", Day: date("2026-10-05"), In: at("2026-10-05 08:20:00"), Out: at("2026-10-05 17:40:00"), By: "mgr", At: now}))
	rows, err := s.Report(office.ID, "A", date("2026-10-05"), date("2026-10-05"), now)
	must(err)
	got := rows[0].Days[0]
	if got.Status != StatusOnTime || !slices.Contains(got.Flags, FlagManagerEdit) || rows[0].Actual != 9*time.Hour+20*time.Minute {
		t.Errorf("corrected day = %+v, actual %v", got, rows[0].Actual)
	}
	must(s.SaveCorrection(Correction{EmployeeID: "A", Day: date("2026-10-05"), By: "mgr", At: now}))
	if fixes, err := s.Corrections("A", date("2026-10-01"), date("2026-10-31")); err != nil || len(fixes) != 0 {
		t.Errorf("Corrections after clearing = %v, %v; want none", fixes, err)
	}

	// B leaves on 10-02: reported while the range reaches that day, and off after it.
	must(s.DeactivateEmployee("B", date("2026-10-02")))
	rows, err = s.Report(office.ID, "", date("2026-10-05"), date("2026-10-05"), now)
	must(err)
	if len(rows) != 1 || rows[0].Employee.ID != "A" {
		t.Errorf("report after B left has %d rows, want just A", len(rows))
	}
	rows, err = s.Report(office.ID, "B", date("2026-10-02"), date("2026-10-05"), now)
	must(err)
	if len(rows) != 1 || on(t, rows[0], "2026-10-02").Status != StatusAbsent || on(t, rows[0], "2026-10-05").Status != StatusOff {
		t.Errorf("B's last days = %+v", rows)
	}
}
