package main

import (
	"database/sql"
	"os"
	"reflect"
	"strconv"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store { return newTestStoreIn(t, time.UTC) }

// newTestStoreIn opens a fresh store: a temp SQLite file, or the database at TEST_MYSQL_DSN, emptied first.
func newTestStoreIn(t *testing.T, loc *time.Location) *Store {
	t.Helper()
	driver, dsn := "sqlite", t.TempDir()+"/a.db"
	if v := os.Getenv("TEST_MYSQL_DSN"); v != "" {
		driver, dsn = "mysql", v
		dropTables(t, dsn)
	}
	s, err := OpenStore(driver, dsn, loc)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// dropTables empties the MySQL database, children before the tables they reference.
func dropTables(t *testing.T, dsn string) {
	t.Helper()
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, table := range []string{"sessions", "users", "corrections", "holidays", "leaves", "roster", "devices", "events", "taps", "employees", "shifts", "orgs"} {
		if _, err := db.Exec("DROP TABLE IF EXISTS " + table); err != nil {
			t.Fatal(err)
		}
	}
}

func tapsOn(t *testing.T, st *Store, employee string) []time.Time {
	t.Helper()
	got, err := st.Taps(employee, time.Date(2026, 10, 18, 3, 0, 0, 0, time.UTC), time.Date(2026, 10, 19, 3, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestLastReceived(t *testing.T) {
	st := newTestStoreIn(t, testLoc)
	if _, ok, err := st.LastReceived(); err != nil || ok {
		t.Fatalf("empty store: ok = %v, err = %v; want false, nil", ok, err)
	}
	for i, received := range []string{"2026-10-18 11:00:00", "2026-10-18 09:00:00"} { // newest first: MAX, not the last insert
		e := Event{DeviceID: "d1", SerialNo: strconv.Itoa(i), Major: "0x5", Minor: "0x26", Time: at("2026-10-18 08:00:00")}
		if err := st.InsertEvent(e, at(received)); err != nil {
			t.Fatal(err)
		}
	}
	got, ok, err := st.LastReceived()
	if err != nil || !ok || !got.Equal(at("2026-10-18 11:00:00")) || got.Location() != testLoc {
		t.Errorf("LastReceived = %v, %v, %v; want 2026-10-18 11:00:00 in %v, true, nil", got, ok, err, testLoc)
	}
}

func TestEvents(t *testing.T) {
	s := newTestStoreIn(t, testLoc)
	ev := func(device, serial, minor, employee, stamp string) Event {
		return Event{DeviceID: device, SerialNo: serial, Major: "0x5", Minor: minor, EmployeeID: employee, Time: at(stamp)}
	}
	all := []Event{
		ev("d1", "1", "0x26", "E1", "2026-10-19 01:30:00"), // still 10-18's attendance day
		ev("d1", "2", "0x31", "", "2026-10-19 03:00:00"),   // first second of 10-19
		ev("d2", "5", "0x15", "", "2026-10-19 05:00:00"),
		ev("d1", "9", "0x1", "E1", "2026-10-19 06:00:00"),
		ev("d1", "10", "0x16", "", "2026-10-19 06:00:00"), // same second as the last: the later serial comes first
		ev("d1", "3", "0x26", "E1", "2026-10-20 02:59:59"),
		ev("d1", "4", "0x26", "E1", "2026-10-20 03:00:00"), // 10-20's
	}
	for _, e := range all {
		for range 2 { // the device resends until acked
			if err := s.InsertEvent(e, at("2026-10-20 12:00:00")); err != nil {
				t.Fatal(err)
			}
		}
	}
	events := func(from, to, device string) []Event { return mustGet(s.Events(at(from), at(to), device)) }

	// 10-19, newest first.
	want := []Event{all[5], all[4], all[3], all[2], all[1]}
	if got := events("2026-10-19 03:00:00", "2026-10-20 03:00:00", ""); !reflect.DeepEqual(got, want) {
		t.Errorf("10-19 events = %+v, want %+v", got, want)
	}
	if got := events("2026-10-19 03:00:00", "2026-10-20 03:00:00", "d2"); !reflect.DeepEqual(got, []Event{all[2]}) {
		t.Errorf("10-19 events of d2 = %+v, want %+v", got, all[2])
	}
	if got := events("2026-10-18 03:00:00", "2026-10-19 03:00:00", ""); !reflect.DeepEqual(got, []Event{all[0]}) {
		t.Errorf("10-18 events = %+v, want the 01:30 one", got)
	}
}

func TestInsertEventHashDedupes(t *testing.T) {
	st := newTestStore(t)
	at := time.Date(2026, 10, 23, 8, 0, 0, 0, time.UTC)
	for _, serial := range []string{"1", "2"} {
		if err := st.InsertEvent(Event{DeviceID: "admin", SerialNo: serial, Major: "0x5", Minor: "0x4b", Time: at, Hash: "abc"}, at); err != nil {
			t.Fatal(err)
		}
	}
	got, err := st.Events(at.Add(-time.Hour), at.Add(time.Hour), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Errorf("got %d events, want 1 (same hash): %+v", len(got), got)
	}
}
