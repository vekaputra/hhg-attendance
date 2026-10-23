package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"hhg-attendance/internal/wire"
)

// fakeVPS stands in for POST /api/events. It answers status, or drops the connection while status is 0.
type fakeVPS struct {
	*httptest.Server
	status atomic.Int32

	mu         sync.Mutex
	hits       int
	got        []wire.Message // messages answered with 200, in arrival order
	user, pass string         // basic auth of the last request
}

func newFakeVPS(t *testing.T) *fakeVPS {
	v := &fakeVPS{}
	v.status.Store(200)
	v.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v.mu.Lock()
		defer v.mu.Unlock()
		v.hits++
		v.user, v.pass, _ = r.BasicAuth()
		status := int(v.status.Load())
		if status == 0 {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				conn.Close()
			}
			return
		}
		if status == 200 {
			var m wire.Message
			if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
				t.Errorf("bad body: %v", err)
			}
			v.got = append(v.got, m)
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(v.Close)
	return v
}

func (v *fakeVPS) received() []wire.Message {
	v.mu.Lock()
	defer v.mu.Unlock()
	return slices.Clone(v.got)
}

func newTestSender(v *fakeVPS, queueDir string) *sender {
	return &sender{url: v.URL, user: "relay", password: "s3cret", client: &http.Client{Timeout: 10 * time.Second}, queueDir: queueDir}
}

func event(serial string) wire.Message {
	m := wire.Message{Command: "ACS", DeviceID: "admin", SerialNo: serial, Time: "2026-10-18 10:42:03", Major: "0x5", Minor: "0x26", EmployeeNo: "HHG0001"}
	m.Hash = wire.EventHash(m)
	return m
}

func queued(t *testing.T, dir string) []string {
	t.Helper()
	names, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	return names
}

func serials(ms []wire.Message) []string {
	var s []string
	for _, m := range ms {
		s = append(s, m.SerialNo)
	}
	return s
}

func TestDeliverPostsLive(t *testing.T) {
	v := newFakeVPS(t)
	s := newTestSender(v, t.TempDir())
	if err := s.deliver(event("1")); err != nil {
		t.Fatal(err)
	}
	if q := queued(t, s.queueDir); len(q) != 0 {
		t.Errorf("queued %v after a 200", q)
	}
	if got := serials(v.received()); !slices.Equal(got, []string{"1"}) {
		t.Errorf("received %v", got)
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.user != "relay" || v.pass != "s3cret" {
		t.Errorf("basic auth = %q/%q", v.user, v.pass)
	}
}

func TestDeliverQueuesWhenDown(t *testing.T) {
	v := newFakeVPS(t)
	v.status.Store(503)
	s := newTestSender(v, t.TempDir())
	if err := s.deliver(event("1")); err != nil {
		t.Fatal(err)
	}
	if q := queued(t, s.queueDir); len(q) != 1 {
		t.Errorf("queue = %v, want one file", q)
	}
}

func TestDeliverFailsWhenQueueUnwritable(t *testing.T) {
	v := newFakeVPS(t)
	v.status.Store(0)
	notDir := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(notDir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := newTestSender(v, notDir).deliver(event("1")); err == nil {
		t.Error("deliver returned nil with the VPS down and no queue")
	}
}

func TestFlushOldestFirstThenEmpty(t *testing.T) {
	v := newFakeVPS(t)
	v.status.Store(0)
	s := newTestSender(v, t.TempDir())
	for _, serial := range []string{"3", "1", "2"} {
		if err := s.deliver(event(serial)); err != nil {
			t.Fatal(err)
		}
	}
	v.status.Store(200)
	s.flush()
	if got := serials(v.received()); !slices.Equal(got, []string{"3", "1", "2"}) {
		t.Errorf("received %v, want enqueue order", got)
	}
	if q := queued(t, s.queueDir); len(q) != 0 {
		t.Errorf("queue not empty: %v", q)
	}
}

func TestFlushSkipsBrokenFiles(t *testing.T) {
	v := newFakeVPS(t)
	s := newTestSender(v, t.TempDir())
	for _, serial := range []string{"1", "2"} {
		if err := s.enqueue(event(serial)); err != nil {
			t.Fatal(err)
		}
	}
	// Named to sort first, so flush must move past them.
	garbage, tmp := filepath.Join(s.queueDir, "0-garbage.json"), filepath.Join(s.queueDir, "0-partial.json.tmp")
	for _, name := range []string{garbage, tmp} {
		if err := os.WriteFile(name, []byte("{not json"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	s.flush()
	if got := serials(v.received()); !slices.Equal(got, []string{"1", "2"}) {
		t.Errorf("received %v", got)
	}
	for _, name := range []string{garbage, tmp} {
		if _, err := os.Stat(name); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestFlushStopsOnNetworkError(t *testing.T) {
	v := newFakeVPS(t)
	v.status.Store(0)
	s := newTestSender(v, t.TempDir())
	for _, serial := range []string{"1", "2", "3"} {
		if err := s.enqueue(event(serial)); err != nil {
			t.Fatal(err)
		}
	}
	s.flush()
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.hits != 1 {
		t.Errorf("flush made %d requests, want 1", v.hits)
	}
	if q := queued(t, s.queueDir); len(q) != 3 {
		t.Errorf("queue = %v, want all three", q)
	}
}

func TestLiveSuccessFlushesQueue(t *testing.T) {
	v := newFakeVPS(t)
	s := newTestSender(v, t.TempDir())
	if err := s.enqueue(event("1")); err != nil {
		t.Fatal(err)
	}
	if err := s.deliver(event("2")); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(2 * time.Second); len(queued(t, s.queueDir)) != 0; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("queued event not flushed within 2s")
		}
	}
	s.flushing.Lock() // wait for the flush goroutine to finish
	if got := serials(v.received()); !slices.Equal(got, []string{"2", "1"}) {
		t.Errorf("received %v", got)
	}
}
