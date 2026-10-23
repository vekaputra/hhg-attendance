package main

import (
	"database/sql"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
)

func TestOpenStoreSeedsOnce(t *testing.T) {
	path := t.TempDir() + "/a.db"
	check := func(s *Store) {
		t.Helper()
		orgs, err := s.Orgs()
		if err != nil {
			t.Fatal(err)
		}
		if len(orgs) != 2 {
			t.Fatalf("got %d orgs, want 2: %+v", len(orgs), orgs)
		}
		office, cafe := orgs[0], orgs[1]
		wantOffice := Org{ID: office.ID, Name: "Office", Kind: "fixed", Start: "08:30", End: "17:30", Weekdays: "12345"}
		if office != wantOffice {
			t.Errorf("office = %+v, want %+v", office, wantOffice)
		}
		wantCafe := Org{ID: cafe.ID, Name: "Cafe", Kind: "roster"}
		if cafe != wantCafe {
			t.Errorf("cafe = %+v, want %+v", cafe, wantCafe)
		}
		shifts, err := s.Shifts(cafe.ID)
		if err != nil {
			t.Fatal(err)
		}
		var got [][3]string
		for _, sh := range shifts {
			got = append(got, [3]string{sh.Name, sh.Start, sh.End})
		}
		want := [][3]string{{"1", "06:00", "15:00"}, {"2", "11:00", "20:00"}, {"3", "15:00", "22:00"}}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("cafe shifts = %v, want %v", got, want)
		}
		if shifts, err := s.Shifts(office.ID); err != nil || len(shifts) != 0 {
			t.Errorf("office shifts = %v, %v; want none", shifts, err)
		}
	}

	s, err := OpenStore("sqlite", path, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	check(s)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = OpenStore("sqlite", path, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	check(s)
}

func TestIsDuplicate(t *testing.T) {
	s := newTestStore(t)
	exec := func(q string) error { _, err := s.db.Exec(q); return err }

	if err := exec(`INSERT INTO holidays (day) VALUES ('2026-01-01')`); err != nil {
		t.Fatal(err)
	}
	for name, q := range map[string]string{
		"primary key": `INSERT INTO holidays (day) VALUES ('2026-01-01')`,
		"unique":      `INSERT INTO orgs (name, kind) VALUES ('Office', 'fixed')`, // seeded
	} {
		if err := exec(q); err == nil || !isDuplicate(err) {
			t.Errorf("%s: isDuplicate(%v) = false, want true", name, err)
		}
	}
	// Other constraint failures and no error at all are not duplicates.
	if err := exec(`INSERT INTO shifts (org_id, name, start_time, end_time) VALUES (999, 'x', '06:00', '07:00')`); err == nil || isDuplicate(err) {
		t.Errorf("foreign key violation: err = %v, isDuplicate = %v; want an error that is not a duplicate", err, isDuplicate(err))
	}
	if isDuplicate(nil) {
		t.Error("isDuplicate(nil) = true")
	}
}

// Each upsert run twice must leave one row holding the second call's values.
func TestUpsertsKeepLatest(t *testing.T) {
	s := newTestStoreIn(t, testLoc)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	cafe := mustGet(s.Orgs())[1]
	shifts := mustGet(s.Shifts(cafe.ID))
	must(s.AddEmployee(Employee{ID: "E1", Name: "Eve", OrgID: cafe.ID, ActiveFrom: "2026-01-01"}))

	must(s.TouchDevice("d1", at("2026-10-18 09:00:00")))
	must(s.TouchDevice("d1", at("2026-10-18 10:00:00")))
	if got, want := mustGet(s.DeviceContacts()), map[string]time.Time{"d1": at("2026-10-18 10:00:00")}; !reflect.DeepEqual(got, want) {
		t.Errorf("TouchDevice: contacts = %v, want %v", got, want)
	}

	must(s.SetUser("ana", "first password"))
	must(s.SetUser("ana", "second password"))
	var users int
	must(s.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&users))
	if first, second := mustGet(s.CheckUser("ana", "first password")), mustGet(s.CheckUser("ana", "second password")); users != 1 || first || !second {
		t.Errorf("SetUser: %d users, first password ok = %v, second ok = %v; want 1, false, true", users, first, second)
	}

	day := date("2026-10-19")
	must(s.SetRoster("E1", day, shifts[0].ID, "mgr", at("2026-10-18 09:00:00")))
	must(s.SetRoster("E1", day, shifts[1].ID, "bo", at("2026-10-18 10:00:00")))
	wantRoster := map[string]RosterDay{"2026-10-19": {Shift: "2", Start: "11:00", End: "20:00", ChangedBy: "bo", ChangedAt: "2026-10-18 10:00:00"}}
	if got := mustGet(s.Roster("E1", day, day)); !reflect.DeepEqual(got, wantRoster) {
		t.Errorf("SetRoster: roster = %v, want %v", got, wantRoster)
	}

	// The second correction replaces the first outright: its missing check-in is not merged from the first.
	must(s.SaveCorrection(Correction{EmployeeID: "E1", Day: day, In: at("2026-10-19 08:00:00"), By: "mgr", At: at("2026-10-19 12:00:00")}))
	must(s.SaveCorrection(Correction{EmployeeID: "E1", Day: day, Out: at("2026-10-19 18:00:00"), By: "bo", At: at("2026-10-19 19:00:00")}))
	wantFixes := map[string]Fix{"2026-10-19": {Out: at("2026-10-19 18:00:00")}}
	if got := mustGet(s.Corrections("E1", day, day)); !reflect.DeepEqual(got, wantFixes) {
		t.Errorf("SaveCorrection: fixes = %v, want %v", got, wantFixes)
	}
}

// A resent tap and a repeated holiday are not errors and not stored twice.
func TestRepeatedInsertsIgnored(t *testing.T) {
	s := newTestStoreIn(t, testLoc)
	tap := Tap{DeviceID: "d1", SerialNo: "83", EmployeeID: "E1", Time: at("2026-10-19 08:00:00")}
	for range 2 {
		if err := s.InsertTap(tap, at("2026-10-19 08:00:01")); err != nil {
			t.Fatalf("InsertTap: %v", err)
		}
		if err := s.AddHoliday(date("2026-12-25")); err != nil {
			t.Fatalf("AddHoliday: %v", err)
		}
	}
	taps := mustGet(s.Taps("E1", at("2026-10-19 00:00:00"), at("2026-10-20 00:00:00")))
	holidays := mustGet(s.Holidays(date("2026-12-01"), date("2026-12-31")))
	if len(taps) != 1 || len(holidays) != 1 {
		t.Errorf("got %d taps and %d holidays, want 1 and 1", len(taps), len(holidays))
	}
}

// A file from before leave became leaves must keep its rows, not get a second, empty leaves table beside them.
func TestOpenStoreKeepsOldLeave(t *testing.T) {
	path := t.TempDir() + "/a.db"
	s, err := OpenStore("sqlite", path, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.AddEmployee(Employee{ID: "E1", Name: "Eve", OrgID: mustGet(s.Orgs())[0].ID, ActiveFrom: "2026-10-01"}))
	must(s.AddLeave("E1", date("2026-10-20"), date("2026-10-22")))
	want := mustGet(s.Leaves(date("2026-10-01"), date("2026-10-31")))
	s.Close()
	execOnFile(t, path, `ALTER TABLE leaves RENAME TO leave`)

	s, err = OpenStore("sqlite", path, time.UTC)
	must(err)
	defer s.Close()
	if got := mustGet(s.Leaves(date("2026-10-01"), date("2026-10-31"))); len(want) != 1 || !reflect.DeepEqual(got, want) {
		t.Errorf("leaves after reopening an old file = %+v, want %+v", got, want)
	}
	var legacy int
	must(s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name = 'leave'`).Scan(&legacy))
	if legacy != 0 {
		t.Error("the old leave table is still there beside leaves")
	}
}

func TestIsTooLong(t *testing.T) {
	tooLong := &mysql.MySQLError{Number: 1406, Message: "Data too long for column 'employee_id' at row 1"}
	if !isTooLong(tooLong) || !isTooLong(fmt.Errorf("wrapped: %w", tooLong)) {
		t.Error("isTooLong missed MySQL error 1406")
	}
	if isTooLong(&mysql.MySQLError{Number: 1062}) || isTooLong(nil) {
		t.Error("isTooLong accepted an error that is not 1406")
	}
}

func TestOpenStoreAddsEventHash(t *testing.T) {
	driver, dsn, oldEvents := "sqlite", t.TempDir()+"/a.db", `CREATE TABLE events (
		device_id TEXT NOT NULL, serial_no TEXT NOT NULL, device_time TEXT NOT NULL, major TEXT NOT NULL, minor TEXT NOT NULL,
		employee_id TEXT NOT NULL DEFAULT '', received_at TEXT NOT NULL, PRIMARY KEY (device_id, serial_no, device_time))`
	if v := os.Getenv("TEST_MYSQL_DSN"); v != "" {
		driver, dsn = "mysql", v
		dropTables(t, dsn)
		oldEvents = `CREATE TABLE events (
			device_id VARCHAR(64) NOT NULL, serial_no VARCHAR(20) NOT NULL, device_time VARCHAR(19) NOT NULL, major VARCHAR(8) NOT NULL,
			minor VARCHAR(8) NOT NULL, employee_id VARCHAR(32) NOT NULL DEFAULT '', received_at VARCHAR(19) NOT NULL,
			PRIMARY KEY (device_id, serial_no, device_time)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin`
	}
	old, err := sql.Open(driver, dsn)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{oldEvents, `INSERT INTO events VALUES ('admin', '1', '2026-10-23 08:00:00', '0x5', '0x4b', '', '2026-10-23 08:00:01')`} {
		if _, err := old.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	old.Close()

	var s *Store
	for range 2 { // the second open finds the column already there
		if s, err = OpenStore(driver, dsn, time.UTC); err != nil {
			t.Fatal(err)
		}
		defer s.Close()
	}
	at := time.Date(2026, 10, 23, 9, 0, 0, 0, time.UTC)
	for _, serial := range []string{"2", "3"} {
		if err := s.InsertEvent(Event{DeviceID: "admin", SerialNo: serial, Major: "0x5", Minor: "0x4b", Time: at, Hash: "abc"}, at); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Events(at.Add(-2*time.Hour), at.Add(time.Hour), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 { // the old row and one of the two same-hash inserts
		t.Errorf("got %d events, want 2: %+v", len(got), got)
	}
}
