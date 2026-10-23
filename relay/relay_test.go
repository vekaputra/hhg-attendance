package main

import (
	"errors"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

// The relay acks an ACS event only once it is on the VPS or in the queue, so the device keeps resending it otherwise.
func TestServeAcksOnlyWhenSafe(t *testing.T) {
	v := newFakeVPS(t)
	queueDir := t.TempDir()
	s := newTestSender(v.URL, queueDir)
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	stopped := make(chan error, 1)
	go func() { stopped <- serve(conn, &ehome{allowed: []string{"admin"}, registered: map[string]bool{}}, s) }()
	t.Cleanup(func() {
		conn.Close()
		<-stopped
		s.flushing.Lock() // wait for any flush goroutine a live post started
	})

	device, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer device.Close()
	// send sends pkt as the device and returns the reply's Status, or 0 if none came within wait.
	send := func(pkt string, wait time.Duration) int {
		t.Helper()
		if _, err := device.WriteTo([]byte(pkt), conn.LocalAddr()); err != nil {
			t.Fatal(err)
		}
		device.SetReadDeadline(time.Now().Add(wait))
		buf := make([]byte, 64<<10)
		n, _, err := device.ReadFrom(buf)
		if errors.Is(err, os.ErrDeadlineExceeded) {
			return 0
		}
		if err != nil {
			t.Fatal(err)
		}
		return replyStatus(t, buf[:n])
	}
	acs := func(serial string) string {
		return strings.Replace(acsPkt, "<serialNo>83<", "<serialNo>"+serial+"<", 1) + strings.Repeat("\x00", 8082)
	}

	if got := send(registerPkt, 5*time.Second); got != 200 {
		t.Fatalf("register: status %d", got)
	}

	if got := send(acs("1"), 5*time.Second); got != 200 {
		t.Errorf("acs with the VPS up: status %d", got)
	}
	if got := serials(v.received()); len(got) != 1 || got[0] != "1" {
		t.Errorf("VPS received %v", got)
	}

	v.status.Store(503)
	if got := send(acs("2"), 5*time.Second); got != 200 {
		t.Errorf("acs with the VPS down: status %d", got)
	}
	if q := queued(t, queueDir); len(q) != 1 {
		t.Errorf("queue = %v, want one file", q)
	}

	// Replace the queue directory with a file so nothing can be written there.
	v.status.Store(0)
	if err := os.RemoveAll(queueDir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(queueDir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := send(acs("3"), time.Second); got != 0 {
		t.Errorf("acs with the VPS down and no queue: status %d, want no reply", got)
	}
}
