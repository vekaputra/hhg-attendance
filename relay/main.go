// Command relay runs on a Raspberry Pi on the fingerprint device's LAN. It answers the device's EHome UDP (ehome.go)
// and forwards its messages to the VPS's POST /api/events over HTTPS, queueing ACS events on <data>/queue while the
// VPS cannot take them (sender.go).
package main

import (
	"cmp"
	"context"
	"io"
	"log/slog"
	"net"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// port is the EHome server port configured on the device; RELAY_PORT overrides it.
var port = "7600"

func main() {
	// Until the logs directory exists (below), slog's default logger writes to stderr only.
	env := map[string]string{}
	var missing []string
	for _, k := range []string{"RELAY_URL", "RELAY_USER", "RELAY_PASSWORD", "RELAY_DEVICE_IDS"} {
		if env[k] = os.Getenv(k); env[k] == "" {
			missing = append(missing, k)
		}
	}
	relayURL, _ := url.Parse(env["RELAY_URL"])
	allowed := strings.FieldsFunc(env["RELAY_DEVICE_IDS"], func(r rune) bool { return r == ',' || r == ' ' })
	switch {
	case len(missing) > 0:
		fatal("missing required environment", "vars", missing)
	case !strings.HasPrefix(env["RELAY_URL"], "https://"):
		fatal("RELAY_URL must start with https://") // basic auth must never cross the internet in clear text
	case relayURL == nil:
		fatal("RELAY_URL is not a valid URL") // not the error: it quotes the URL, credentials included
	case len(allowed) == 0:
		fatal("RELAY_DEVICE_IDS lists no device ID")
	}
	port = cmp.Or(os.Getenv("RELAY_PORT"), port)
	data := cmp.Or(os.Getenv("RELAY_DATA"), "/data")
	for _, dir := range []string{"queue", "logs"} {
		if err := os.MkdirAll(filepath.Join(data, dir), 0o700); err != nil {
			fatal("create data directory", "err", err)
		}
	}
	// Every file logs through slog's default logger: stderr (journald, in RAM) gets each line at once; the SD card
	// only gets them on hl.flush.
	hl := &hourlyLog{dir: filepath.Join(data, "logs"), now: time.Now}
	slog.SetDefault(slog.New(slog.NewTextHandler(io.MultiWriter(os.Stderr, hl), nil)))

	s := &sender{
		url:      env["RELAY_URL"],
		user:     env["RELAY_USER"],
		password: env["RELAY_PASSWORD"],
		client:   newClient(),
		queueDir: filepath.Join(data, "queue"),
	}
	conn, err := net.ListenPacket("udp", ":"+port)
	if err != nil {
		slog.Error("listen", "port", port, "err", err)
		hl.flush()
		os.Exit(1)
	}
	slog.Info("relay listening", "port", port, "vps", relayURL.Redacted(), "deviceIDs", allowed, "data", data)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	go func() {
		s.flush()
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := hl.flush(); err != nil {
					slog.Error("flush logs failed", "err", err)
				}
				if err := hl.prune(30 * 24 * time.Hour); err != nil {
					slog.Error("prune logs failed", "err", err)
				}
				s.flush()
			}
		}
	}()
	go func() { <-ctx.Done(); conn.Close() }() // unblocks serve

	err = serve(conn, &ehome{allowed: allowed, registered: map[string]bool{}}, s)
	code := 0
	if ctx.Err() != nil {
		slog.Info("relay stopping")
	} else {
		slog.Error("ehome listener stopped", "err", err)
		code = 1
	}
	if err := hl.flush(); err != nil {
		slog.Error("final log flush failed", "err", err)
		code = 1
	}
	os.Exit(code)
}

func fatal(msg string, args ...any) {
	slog.Error(msg, args...)
	os.Exit(1)
}

// serve reads packets from conn until it fails: REGISTER/KEEPALIVE get their reply;
// ACS gets the ack only if deliver returned nil; other commands get the ack and one best-effort post (never queued).
func serve(conn net.PacketConn, e *ehome, s *sender) error {
	buf := make([]byte, 64<<10)
	for {
		n, from, err := conn.ReadFrom(buf)
		if err != nil {
			return err
		}
		reply, msg := e.handle(buf[:n], from)
		if msg != nil && msg.Command == "ACS" {
			if err := s.deliver(*msg); err != nil {
				// No ack: the device keeps the event and resends it.
				slog.Error("event neither posted nor queued, not acked", "serialNo", msg.SerialNo, "employeeNo", msg.EmployeeNo, "time", msg.Time, "err", err)
				continue
			}
		}
		if reply != nil {
			if _, err := conn.WriteTo(reply, from); err != nil {
				slog.Warn("ehome reply failed", "to", from, "err", err)
			}
		}
		if msg != nil && msg.Command != "ACS" {
			if s.down.Load() {
				slog.Warn("forward skipped, VPS down", "command", msg.Command, "deviceID", msg.DeviceID)
			} else if err := s.post(*msg); err != nil {
				slog.Warn("forward failed, dropped", "command", msg.Command, "deviceID", msg.DeviceID, "err", err)
			}
		}
	}
}
