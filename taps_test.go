package main

import (
	"database/sql"
	"net"
	"os"
	"strings"
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
	for _, table := range []string{"sessions", "users", "corrections", "holidays", "leaves", "roster", "devices", "taps", "employees", "shifts", "orgs"} {
		if _, err := db.Exec("DROP TABLE IF EXISTS " + table); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTouchDevice(t *testing.T) {
	alarmIP, allowedDeviceIDs = "10.0.0.2", []string{"admin"}
	st := newTestStore(t)
	device := &net.UDPAddr{IP: net.IPv4(192, 168, 100, 72), Port: 54288}
	registered := map[string]bool{}

	handleEHome([]byte(registerPkt), device, registered, st)
	contacts, err := st.DeviceContacts()
	if err != nil {
		t.Fatal(err)
	}
	afterRegister := contacts["admin"]
	if afterRegister.IsZero() {
		t.Fatalf("no contact after REGISTER: %v", contacts)
	}

	// Push the stored contact into the past so a KEEPALIVE must move it, without sleeping past stampFmt's 1s resolution.
	if err := st.TouchDevice("admin", afterRegister.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	keepalive := strings.Replace(acsPkt, "<Command>ACS</Command>", "<Command>KEEPALIVE</Command>", 1)
	handleEHome([]byte(keepalive), device, registered, st)
	contacts, err = st.DeviceContacts()
	if err != nil {
		t.Fatal(err)
	}
	if contacts["admin"].Before(afterRegister) {
		t.Errorf("contact after KEEPALIVE %v, want >= %v", contacts["admin"], afterRegister)
	}
}
