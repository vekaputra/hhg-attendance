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
// It answers 400 to the event whose SerialNo is reject.
type fakeVPS struct {
	*httptest.Server
	status atomic.Int32

	mu         sync.Mutex
	reject     string
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
		var m wire.Message
		if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
			t.Errorf("bad body: %v", err)
		}
		if m.SerialNo == v.reject {
			status = 400
		}
		if status == 200 {
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

func newTestSender(url, queueDir string) *sender {
	return &sender{url: url, user: "relay", password: "s3cret", client: newClient(), queueDir: queueDir}
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
	s := newTestSender(v.URL, t.TempDir())
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
	s := newTestSender(v.URL, t.TempDir())
	if err := s.deliver(event("1")); err != nil {
		t.Fatal(err)
	}
	if q := queued(t, s.queueDir); len(q) != 1 {
		t.Errorf("queue = %v, want one file", q)
	}
	if !s.down.Load() {
		t.Error("down not set after a 503")
	}
}

func TestDeliverFailsWhenQueueUnwritable(t *testing.T) {
	v := newFakeVPS(t)
	v.status.Store(0)
	notDir := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(notDir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := newTestSender(v.URL, notDir).deliver(event("1")); err == nil {
		t.Error("deliver returned nil with the VPS down and no queue")
	}
}

func TestFlushOldestFirstThenEmpty(t *testing.T) {
	v := newFakeVPS(t)
	v.status.Store(0)
	s := newTestSender(v.URL, t.TempDir())
	for _, serial := range []string{"3", "1", "2"} {
		if err := s.enqueue(event(serial)); err != nil {
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
	s := newTestSender(v.URL, t.TempDir())
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

// A 503 or a network error marks the VPS down and stops the flush at the first file.
func TestFlushStopsWhenUnreachable(t *testing.T) {
	for name, status := range map[string]int32{"503": 503, "network error": 0} {
		t.Run(name, func(t *testing.T) {
			v := newFakeVPS(t)
			v.status.Store(status)
			s := newTestSender(v.URL, t.TempDir())
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
			if !s.down.Load() {
				t.Error("down not set")
			}
		})
	}
}

// A 400 is the VPS refusing that one event: the file stays and the rest of the queue is sent.
func TestFlushKeepsRefusedAndContinues(t *testing.T) {
	v := newFakeVPS(t)
	v.mu.Lock()
	v.reject = "1"
	v.mu.Unlock()
	s := newTestSender(v.URL, t.TempDir())
	for _, serial := range []string{"1", "2", "3"} {
		if err := s.enqueue(event(serial)); err != nil {
			t.Fatal(err)
		}
	}
	refused := queued(t, s.queueDir)[0]
	s.flush()
	if got := serials(v.received()); !slices.Equal(got, []string{"2", "3"}) {
		t.Errorf("received %v", got)
	}
	if q := queued(t, s.queueDir); !slices.Equal(q, []string{refused}) {
		t.Errorf("queue = %v, want only %s", q, refused)
	}
	if s.down.Load() {
		t.Error("down set by a 400")
	}
}

// While down, deliver queues without posting, so a black-holed VPS costs the device an fsync, not the client
// timeout; a background flush probes the VPS.
func TestDeliverWhileDownQueuesWithoutWaiting(t *testing.T) {
	probed, release := make(chan struct{}, 1), make(chan struct{})
	vps := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case probed <- struct{}{}:
		default:
		}
		<-release // black-holed: no answer until the test ends
	}))
	t.Cleanup(vps.Close)
	s := newTestSender(vps.URL, t.TempDir())
	t.Cleanup(func() {
		close(release)
		s.flushing.Lock() // wait for the probing flush
	})
	s.down.Store(true)

	start := time.Now()
	if err := s.deliver(event("1")); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("deliver took %v while down", d)
	}
	if q := queued(t, s.queueDir); len(q) != 1 {
		t.Errorf("queue = %v, want one file", q)
	}
	select {
	case <-probed:
	case <-time.After(2 * time.Second):
		t.Error("no background flush probed the VPS")
	}
}

// While down with no usable queue, deliver still tries the post rather than refusing the event.
func TestDeliverWhileDownPostsIfQueueFails(t *testing.T) {
	v := newFakeVPS(t)
	notDir := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(notDir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	s := newTestSender(v.URL, notDir)
	s.down.Store(true)
	if err := s.deliver(event("1")); err != nil {
		t.Fatal(err)
	}
	if got := serials(v.received()); !slices.Equal(got, []string{"1"}) {
		t.Errorf("received %v", got)
	}
	if s.down.Load() {
		t.Error("down still set after a 200")
	}
}

// A flush that gets a 200 clears down, so the next event is posted live again.
func TestFlushSuccessClearsDown(t *testing.T) {
	v := newFakeVPS(t)
	s := newTestSender(v.URL, t.TempDir())
	s.down.Store(true)
	if err := s.enqueue(event("1")); err != nil {
		t.Fatal(err)
	}
	s.flush()
	if s.down.Load() {
		t.Fatal("down still set after a 200")
	}
	if err := s.deliver(event("2")); err != nil {
		t.Fatal(err)
	}
	// Posted live, not queued: the VPS has it before deliver returns.
	if got := serials(v.received()); !slices.Equal(got, []string{"1", "2"}) {
		t.Errorf("received %v", got)
	}
	if q := queued(t, s.queueDir); len(q) != 0 {
		t.Errorf("queue = %v, want empty", q)
	}
	s.flushing.Lock() // wait for the flush deliver started
}

func TestLiveSuccessFlushesQueue(t *testing.T) {
	v := newFakeVPS(t)
	s := newTestSender(v.URL, t.TempDir())
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

// A wrong RELAY_URL path reaches the VPS's login redirect (303 -> GET /login -> 200). Following it would count as
// delivered: the event must be queued, and flush must keep a queued file.
func TestRedirectIsNotDelivery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" {
			return // 200
		}
		http.Redirect(w, r, "/login", http.StatusSeeOther)
	}))
	t.Cleanup(srv.Close)
	s := newTestSender(srv.URL+"/api/events/", t.TempDir())
	if err := s.deliver(event("1")); err != nil {
		t.Fatal(err)
	}
	if q := queued(t, s.queueDir); len(q) != 1 {
		t.Fatalf("queue = %v, want the event queued", q)
	}
	s.flush()
	if q := queued(t, s.queueDir); len(q) != 1 {
		t.Errorf("flush dropped the event on a redirect: queue = %v", q)
	}
}
