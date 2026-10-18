package main

import (
	"net"
	"strings"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenStore(t.TempDir()+"/a.db", time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
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
