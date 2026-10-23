package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"hhg-attendance/internal/wire"
)

// sender delivers messages to the VPS's POST /api/events. ACS events it cannot post wait in queueDir, one JSON file
// each, until a flush gets a 200 for them.
type sender struct {
	url, user, password string
	client              *http.Client // Timeout 10s
	queueDir            string       // <data>/queue
	flushing            sync.Mutex   // TryLock: one flush at a time
}

// statusError is a non-200 answer from the VPS.
type statusError int

func (e statusError) Error() string { return fmt.Sprintf("VPS answered %d", int(e)) }

// post sends m once. It returns nil only on HTTP 200, which means stored, already had, or deliberately dropped.
func (s *sender) post(m wire.Message) error {
	body, err := json.Marshal(m)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, s.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(s.user, s.password)
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10)) // so the connection can be reused
	if resp.StatusCode != http.StatusOK {
		return statusError(resp.StatusCode)
	}
	return nil
}

// enqueue durably writes m to <queueDir>/<unixnano>-<hash>.json: a .tmp file, fsynced, renamed, then the directory
// fsynced, so a power cut leaves either the whole event or nothing. Zero-padding keeps name order = enqueue order.
func (s *sender) enqueue(m wire.Message) error {
	body, err := json.Marshal(m)
	if err != nil {
		return err
	}
	name := filepath.Join(s.queueDir, fmt.Sprintf("%019d-%s.json", time.Now().UnixNano(), m.Hash))
	tmp := name + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(body)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, name)
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	dir, err := os.Open(s.queueDir)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

// deliver posts m; on failure it enqueues m. It returns nil if m was posted or queued. After a successful post it
// starts flush in a goroutine, so the queue empties as soon as the VPS is reachable again.
func (s *sender) deliver(m wire.Message) error {
	err := s.post(m)
	if err == nil {
		go s.flush()
		return nil
	}
	slog.Warn("post failed, queueing event", "serialNo", m.SerialNo, "deviceID", m.DeviceID, "hash", m.Hash, "err", err)
	if qerr := s.enqueue(m); qerr != nil {
		return errors.Join(err, qerr)
	}
	return nil
}

// flush posts queued files in name order and deletes each one that got a 200. On a network error or a 5xx it stops
// until next time. On another status, or a file it cannot read or decode, it logs, keeps the file and moves on.
// It ignores *.tmp files.
func (s *sender) flush() {
	if !s.flushing.TryLock() {
		return
	}
	defer s.flushing.Unlock()
	names, err := filepath.Glob(filepath.Join(s.queueDir, "*.json")) // sorted
	if err != nil {
		slog.Error("list queue failed", "dir", s.queueDir, "err", err)
		return
	}
	sent := 0
	for i, name := range names {
		var m wire.Message
		b, err := os.ReadFile(name)
		if err == nil {
			err = json.Unmarshal(b, &m)
		}
		if err != nil {
			slog.Error("queued event unreadable, kept", "file", name, "err", err)
			continue
		}
		if err := s.post(m); err != nil {
			if status, ok := errors.AsType[statusError](err); ok && status < 500 {
				slog.Error("VPS refused queued event, kept", "file", name, "err", err)
				continue
			}
			slog.Warn("flush stopped, VPS unreachable", "sent", sent, "left", len(names)-i, "err", err)
			return
		}
		sent++
		if err := os.Remove(name); err != nil {
			slog.Error("remove sent event failed", "file", name, "err", err)
		}
	}
	if sent > 0 {
		slog.Info("queue flushed", "sent", sent, "kept", len(names)-sent)
	}
}
