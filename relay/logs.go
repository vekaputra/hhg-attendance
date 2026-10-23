package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// logCap is how much log text hourlyLog buffers before it flushes early.
const logCap = 4 << 20

// hourlyLog is an io.Writer that keeps log lines in RAM and appends them to <dir>/<YYYY-MM-DD>.log only on flush.
// The Pi's root filesystem is read-only and /data is an SD card, so it must not write per line.
type hourlyLog struct {
	dir string           // <data>/logs
	now func() time.Time // the Pi's local time; the file date comes from it

	mu    sync.Mutex
	lines []logLine
	size  int // bytes in lines
}

// logLine is one Write, tagged with the date it was written so a flush after midnight still files it correctly.
type logLine struct {
	date string
	text []byte
}

// Write buffers p, and flushes at logCap. It never fails: a lost log line must not break the caller.
func (h *hourlyLog) Write(p []byte) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.lines = append(h.lines, logLine{h.now().Format(time.DateOnly), bytes.Clone(p)})
	h.size += len(p)
	if h.size >= logCap && h.flushLocked() != nil {
		// The disk is failing: drop the buffer rather than grow without bound. stderr still has these lines, and
		// the hourly flush reports the error.
		h.lines, h.size = nil, 0
	}
	return len(p), nil
}

// flush appends the buffered lines to their days' files and fsyncs. On error the lines not yet written stay buffered.
func (h *hourlyLog) flush() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.flushLocked()
}

func (h *hourlyLog) flushLocked() error {
	if len(h.lines) == 0 {
		return nil
	}
	for len(h.lines) > 0 {
		date := h.lines[0].date
		var buf []byte
		n := 0
		for ; n < len(h.lines) && h.lines[n].date == date; n++ {
			buf = append(buf, h.lines[n].text...)
		}
		if err := appendSync(filepath.Join(h.dir, date+".log"), buf); err != nil {
			return err
		}
		h.lines, h.size = h.lines[n:], h.size-len(buf)
	}
	h.lines = nil
	// fsync the directory too, so a new day's file survives a power cut.
	d, err := os.Open(h.dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func appendSync(path string, b []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	return errors.Join(err, f.Close())
}

// prune removes <YYYY-MM-DD>.log files dated more than keep before now. It goes by file name, and leaves other files.
func (h *hourlyLog) prune(keep time.Duration) error {
	entries, err := os.ReadDir(h.dir)
	if err != nil {
		return err
	}
	now := h.now()
	cutoff := now.Add(-keep)
	var errs []error
	for _, e := range entries {
		date, ok := strings.CutSuffix(e.Name(), ".log")
		day, perr := time.ParseInLocation(time.DateOnly, date, now.Location())
		if ok && perr == nil && day.Before(cutoff) {
			errs = append(errs, os.Remove(filepath.Join(h.dir, e.Name())))
		}
	}
	return errors.Join(errs...)
}
