package main

import (
	"database/sql"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

// insertDevice seeds the unused devices table, which copy-db still carries over.
func insertDevice(s *Store, id, lastContact string) error {
	_, err := s.db.Exec(`INSERT INTO devices (device_id, last_contact) VALUES (?, ?)`, id, lastContact)
	return err
}

func rowCount(t *testing.T, s *Store, table string) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// execOnFile runs a statement directly on a SQLite file, to set up databases OpenStore would not produce.
func execOnFile(t *testing.T, path, query string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(query); err != nil {
		t.Fatal(err)
	}
}

func TestCopyDB(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		name := "leaves"
		if legacy {
			name = "source with the old leave table"
		}
		t.Run(name, func(t *testing.T) {
			// Source: a SQLite file with a row in every table, and IDs that a fresh insert wouldn't reproduce.
			src := t.TempDir() + "/src.db"
			from, err := OpenStore("sqlite", src, testLoc)
			if err != nil {
				t.Fatal(err)
			}
			orgs := mustGet(from.Orgs())
			third := mustGet(from.SaveOrg(Org{Name: "Gym", Kind: "fixed", Start: "09:00", End: "10:00", Weekdays: "1"}))
			shifts := mustGet(from.Shifts(orgs[1].ID))
			for _, e := range []Employee{
				{ID: "E1", Name: "Eve", OrgID: orgs[1].ID, ActiveFrom: "2026-10-01"},
				{ID: "E2", Name: "Bo", OrgID: third, ActiveFrom: "2026-10-01", ActiveUntil: "2026-12-31"},
			} {
				if err := from.AddEmployee(e); err != nil {
					t.Fatal(err)
				}
			}
			for _, err := range []error{
				from.InsertTap(Tap{DeviceID: "d1", SerialNo: "83", EmployeeID: "E1", Time: at("2026-10-19 06:02:00")}, at("2026-10-19 06:02:01")),
				from.InsertTap(Tap{DeviceID: "d1", SerialNo: "84", EmployeeID: "E1", Time: at("2026-10-19 15:01:00")}, at("2026-10-19 15:01:01")),
				from.InsertEvent(Event{DeviceID: "d1", SerialNo: "83", Major: "0x5", Minor: "0x26", EmployeeID: "E1", Time: at("2026-10-19 06:02:00")}, at("2026-10-19 06:02:01")),
				from.InsertEvent(Event{DeviceID: "d1", SerialNo: "85", Major: "0x5", Minor: "0x31", Time: at("2026-10-19 06:03:00")}, at("2026-10-19 06:03:01")),
				insertDevice(from, "d1", "2026-10-19 15:05:00"),
				from.SetRoster("E1", date("2026-10-19"), shifts[0].ID, "mgr", at("2026-10-18 09:00:00")),
				from.SetRoster("E1", date("2026-10-20"), 0, "mgr", at("2026-10-18 09:00:00")),
				from.AddLeave("E2", date("2026-10-21"), date("2026-10-22")),
				from.AddLeave("E2", date("2026-10-23"), date("2026-10-24")),
				from.DeleteLeave(1), // the survivor keeps ID 2
				from.AddHoliday(date("2026-12-25")),
				from.SaveCorrection(Correction{EmployeeID: "E1", Day: date("2026-10-19"), Out: at("2026-10-19 15:30:00"), By: "mgr", At: at("2026-10-19 16:00:00")}),
				from.SetUser("ana", "correct horse"),
			} {
				if err != nil {
					t.Fatal(err)
				}
			}
			token := mustGet(from.NewSession("ana", at("2026-10-19 09:00:00")))
			wantCounts := map[string]int{}
			for _, tbl := range copyTables {
				if wantCounts[tbl.name] = rowCount(t, from, tbl.name); wantCounts[tbl.name] == 0 {
					t.Fatalf("source table %s is empty; the test must fill every table", tbl.name)
				}
			}
			report := func(s *Store) []Row {
				return mustGet(s.Report(0, "", date("2026-10-19"), date("2026-10-25"), at("2026-10-26 12:00:00")))
			}
			allDay := func(s *Store) []Event { return mustGet(s.Events(date("2026-10-19"), date("2026-10-20"), "")) }
			wantEvents := allDay(from)
			wantOrgs, wantShifts := mustGet(from.Orgs()), shifts
			wantReport, wantLeaves := report(from), mustGet(from.Leaves(date("2026-10-01"), date("2026-10-31")))
			from.Close()
			if legacy {
				execOnFile(t, src, `ALTER TABLE leaves RENAME TO leave`)
			}

			to := newTestStoreIn(t, testLoc)
			if err := copyDB(to, src); err != nil {
				t.Fatal(err)
			}
			for table, want := range wantCounts {
				if got := rowCount(t, to, table); got != want {
					t.Errorf("%s: %d rows copied, want %d", table, got, want)
				}
			}
			if got := mustGet(to.Orgs()); !reflect.DeepEqual(got, wantOrgs) || len(got) != 3 || got[2].ID != third {
				t.Errorf("orgs = %+v, want %+v", got, wantOrgs)
			}
			if got := mustGet(to.Shifts(orgs[1].ID)); !reflect.DeepEqual(got, wantShifts) {
				t.Errorf("shifts = %+v, want %+v", got, wantShifts)
			}
			if got := mustGet(to.Leaves(date("2026-10-01"), date("2026-10-31"))); !reflect.DeepEqual(got, wantLeaves) || got[0].ID != 2 {
				t.Errorf("leaves = %+v, want %+v (ID 2 kept)", got, wantLeaves)
			}
			if got := allDay(to); !reflect.DeepEqual(got, wantEvents) {
				t.Errorf("events = %+v, want %+v", got, wantEvents)
			}
			if got := report(to); !reflect.DeepEqual(got, wantReport) {
				t.Errorf("report differs after copy:\n got %+v\nwant %+v", got, wantReport)
			}
			if ok := mustGet(to.CheckUser("ana", "correct horse")); !ok {
				t.Error("copied user can't log in with the old password")
			}
			if user := mustGet(to.SessionUser(token, at("2026-10-19 10:00:00"))); user != "" {
				t.Errorf("session survived the copy for %q; sessions must be skipped", user)
			}

			// A second copy would mix two histories, so it must refuse and change nothing.
			if err := copyDB(to, src); err == nil || !strings.Contains(err.Error(), "not empty") {
				t.Errorf("second copyDB error = %v, want a refusal naming the non-empty target", err)
			}
			if got := rowCount(t, to, "taps"); got != wantCounts["taps"] {
				t.Errorf("refused copy changed taps: %d rows, want %d", got, wantCounts["taps"])
			}
		})
	}
}

// A file from before the events table existed has none to copy, and still copies.
func TestCopyDBWithoutEvents(t *testing.T) {
	src := t.TempDir() + "/src.db"
	from, err := OpenStore("sqlite", src, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	mustGet(from.SaveOrg(Org{Name: "Gym", Kind: "roster"}))
	from.Close()
	execOnFile(t, src, `DROP TABLE events`)

	to := newTestStore(t)
	if err := copyDB(to, src); err != nil {
		t.Fatal(err)
	}
	if got := len(mustGet(to.Orgs())); got != 3 {
		t.Errorf("%d orgs after the copy, want the source's 3", got)
	}
	if got := rowCount(t, to, "events"); got != 0 {
		t.Errorf("%d events copied from a source without the table", got)
	}
}

// A copy that fails partway leaves the target as it was, seeded orgs included.
func TestCopyDBRollsBack(t *testing.T) {
	src := t.TempDir() + "/src.db"
	from, err := OpenStore("sqlite", src, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	mustGet(from.SaveOrg(Org{Name: "Gym", Kind: "roster"}))
	if _, err := from.db.Exec(`DROP TABLE taps`); err != nil { // orgs and shifts copy fine, then taps fails
		t.Fatal(err)
	}
	from.Close()

	to := newTestStore(t)
	if err := copyDB(to, src); err == nil {
		t.Fatal("copyDB succeeded from a source with no taps table")
	}
	if orgs := mustGet(to.Orgs()); len(orgs) != 2 || orgs[0].Name != "Office" || orgs[1].Name != "Cafe" {
		t.Errorf("orgs after failed copy = %+v, want the seeded Office and Cafe", orgs)
	}
}

// OpenStore on a file from before the rename recreates an empty leaves table, so copying it would silently drop
// the leave rows. A source holding rows in both tables cannot be copied without guessing.
func TestCopyDBRefusesLeaveInBothTables(t *testing.T) {
	src := t.TempDir() + "/src.db"
	from, err := OpenStore("sqlite", src, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if err := from.AddEmployee(Employee{ID: "E1", Name: "Eve", OrgID: mustGet(from.Orgs())[0].ID, ActiveFrom: "2026-10-01"}); err != nil {
		t.Fatal(err)
	}
	from.Close()
	execOnFile(t, src, `CREATE TABLE leave (id INTEGER PRIMARY KEY, employee_id TEXT NOT NULL, from_day TEXT NOT NULL, to_day TEXT NOT NULL)`)
	execOnFile(t, src, `INSERT INTO leave (employee_id, from_day, to_day) VALUES ('E1', '2026-10-20', '2026-10-22')`)

	to := newTestStore(t)
	if err := copyDB(to, src); err == nil || !strings.Contains(err.Error(), "leave") {
		t.Errorf("copyDB error = %v, want a refusal naming the leave table", err)
	}
	if got := rowCount(t, to, "leaves"); got != 0 {
		t.Errorf("refused copy left %d leaves rows", got)
	}
}

// A target may hold device contacts from an older run, which the copy replaces; holidays and events are real
// data, so a target holding any is refused.
func TestCopyDBTargetGuards(t *testing.T) {
	src := t.TempDir() + "/src.db"
	from, err := OpenStore("sqlite", src, testLoc)
	if err != nil {
		t.Fatal(err)
	}
	if err := insertDevice(from, "d1", "2026-10-19 10:00:00"); err != nil {
		t.Fatal(err)
	}
	from.Close()

	t.Run("device contacts replaced", func(t *testing.T) {
		to := newTestStoreIn(t, testLoc)
		if err := insertDevice(to, "d1", "2026-10-18 09:00:00"); err != nil {
			t.Fatal(err)
		}
		if err := copyDB(to, src); err != nil {
			t.Fatal(err)
		}
		var got string
		if err := to.db.QueryRow(`SELECT last_contact FROM devices WHERE device_id = 'd1'`).Scan(&got); err != nil || got != "2026-10-19 10:00:00" {
			t.Errorf("last_contact = %q, %v; want the source's 2026-10-19 10:00:00", got, err)
		}
	})
	t.Run("events refused", func(t *testing.T) {
		to := newTestStoreIn(t, testLoc)
		if err := to.InsertEvent(Event{DeviceID: "d1", SerialNo: "1", Major: "0x5", Minor: "0x31", Time: at("2026-10-18 09:00:00")}, at("2026-10-18 09:00:01")); err != nil {
			t.Fatal(err)
		}
		if err := copyDB(to, src); err == nil || !strings.Contains(err.Error(), "not empty") || !strings.Contains(err.Error(), "events") {
			t.Errorf("copyDB error = %v, want a refusal naming the non-empty events", err)
		}
	})
	t.Run("holidays refused", func(t *testing.T) {
		to := newTestStoreIn(t, testLoc)
		if err := to.AddHoliday(date("2026-12-25")); err != nil {
			t.Fatal(err)
		}
		if err := copyDB(to, src); err == nil || !strings.Contains(err.Error(), "not empty") {
			t.Errorf("copyDB error = %v, want a refusal naming the non-empty target", err)
		}
	})
}

// A source on a read-only mount (as in `docker run -v …:ro`) must still copy, since copy-db never writes to it.
func TestCopyDBReadOnlySource(t *testing.T) {
	dir := t.TempDir()
	src := dir + "/src.db"
	from, err := OpenStore("sqlite", src, testLoc)
	if err != nil {
		t.Fatal(err)
	}
	if err := from.AddEmployee(Employee{ID: "E1", Name: "Eve", OrgID: mustGet(from.Orgs())[0].ID, ActiveFrom: "2026-10-01"}); err != nil {
		t.Fatal(err)
	}
	from.Close()
	if err := os.Chmod(src, 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })

	to := newTestStore(t)
	if err := copyDB(to, src); err != nil {
		t.Fatalf("copyDB from a read-only source: %v", err)
	}
	if got := rowCount(t, to, "employees"); got != 1 {
		t.Errorf("employees copied = %d, want 1", got)
	}
}

func TestCopyDBRefusesUnsavedWAL(t *testing.T) {
	src := t.TempDir() + "/src.db"
	from, err := OpenStore("sqlite", src, testLoc)
	if err != nil {
		t.Fatal(err)
	}
	from.Close()
	if err := os.WriteFile(src+"-wal", []byte("pending"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := copyDB(newTestStore(t), src); err == nil || !strings.Contains(err.Error(), "unsaved changes") {
		t.Errorf("copyDB error = %v, want a refusal about unsaved changes", err)
	}
}
