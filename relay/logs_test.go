package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// newTestLog returns an hourlyLog in a temp dir whose clock is *now.
func newTestLog(t *testing.T, now *time.Time) *hourlyLog {
	return &hourlyLog{dir: t.TempDir(), now: func() time.Time { return *now }}
}

func readLog(t *testing.T, h *hourlyLog, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(h.dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestHourlyLogFlushNotBefore(t *testing.T) {
	now := time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC)
	h := newTestLog(t, &now)
	h.Write([]byte("one\n"))
	h.Write([]byte("two\n"))
	if names, _ := os.ReadDir(h.dir); len(names) != 0 {
		t.Fatalf("files before flush: %v", names)
	}
	if err := h.flush(); err != nil {
		t.Fatal(err)
	}
	if got := readLog(t, h, "2026-10-23.log"); got != "one\ntwo\n" {
		t.Fatalf("got %q", got)
	}
}

func TestHourlyLogSplitsAtMidnight(t *testing.T) {
	now := time.Date(2026, 10, 23, 23, 59, 0, 0, time.UTC)
	h := newTestLog(t, &now)
	h.Write([]byte("late\n"))
	now = now.Add(2 * time.Minute)
	h.Write([]byte("early\n"))
	if err := h.flush(); err != nil {
		t.Fatal(err)
	}
	if got := readLog(t, h, "2026-10-23.log"); got != "late\n" {
		t.Fatalf("23rd: %q", got)
	}
	if got := readLog(t, h, "2026-10-24.log"); got != "early\n" {
		t.Fatalf("24th: %q", got)
	}
}

func TestHourlyLogAppends(t *testing.T) {
	now := time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC)
	h := newTestLog(t, &now)
	h.Write([]byte("one\n"))
	h.flush()
	h.Write([]byte("two\n"))
	h.flush()
	if got := readLog(t, h, "2026-10-23.log"); got != "one\ntwo\n" {
		t.Fatalf("got %q", got)
	}
}

func TestHourlyLogFlushesEarlyAtCap(t *testing.T) {
	now := time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC)
	h := newTestLog(t, &now)
	h.Write(bytes.Repeat([]byte("x"), 4<<20+1))
	if got := readLog(t, h, "2026-10-23.log"); len(got) != 4<<20+1 {
		t.Fatalf("flushed %d bytes", len(got))
	}
}

// A failing disk must not make the buffer grow without bound.
func TestHourlyLogDropsAtCapWhenFlushFails(t *testing.T) {
	now := time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC)
	h := newTestLog(t, &now)
	h.dir = filepath.Join(h.dir, "missing")
	h.Write(bytes.Repeat([]byte("x"), 4<<20+1))
	if h.size != 0 {
		t.Fatalf("buffer kept %d bytes", h.size)
	}
}

func TestPrune(t *testing.T) {
	now := time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC)
	h := newTestLog(t, &now)
	old := now.AddDate(0, 0, -31).Format(time.DateOnly) + ".log"
	recent := now.AddDate(0, 0, -29).Format(time.DateOnly) + ".log"
	for _, name := range []string{old, recent, "notes.txt"} {
		if err := os.WriteFile(filepath.Join(h.dir, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.prune(30 * 24 * time.Hour); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(h.dir)
	var left []string
	for _, e := range entries {
		left = append(left, e.Name())
	}
	if len(left) != 2 || left[0] != recent || left[1] != "notes.txt" {
		t.Fatalf("left %v", left)
	}
}
