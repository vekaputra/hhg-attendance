// Command relay runs on a Raspberry Pi on the fingerprint device's LAN. It answers the device's EHome UDP (ehome.go)
// and forwards its messages to the VPS's POST /api/events over HTTPS, queueing ACS events on <data>/queue while the
// VPS cannot take them (sender.go).
package main

import (
	"cmp"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// port is the EHome server port configured on the device; RELAY_PORT overrides it.
var port = "7600"

func main() {
	// Logger setup goes here, first: every file logs through slog's default logger, which writes to stderr.

	env := map[string]string{}
	var missing []string
	for _, k := range []string{"RELAY_URL", "RELAY_USER", "RELAY_PASSWORD", "RELAY_DEVICE_IDS"} {
		if env[k] = os.Getenv(k); env[k] == "" {
			missing = append(missing, k)
		}
	}
	allowed := strings.FieldsFunc(env["RELAY_DEVICE_IDS"], func(r rune) bool { return r == ',' || r == ' ' })
	switch {
	case len(missing) > 0:
		fatal("missing required environment", "vars", missing)
	case !strings.HasPrefix(env["RELAY_URL"], "https://"):
		fatal("RELAY_URL must start with https://") // basic auth must never cross the internet in clear text
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

	s := &sender{
		url:      env["RELAY_URL"],
		user:     env["RELAY_USER"],
		password: env["RELAY_PASSWORD"],
		client:   &http.Client{Timeout: 10 * time.Second},
		queueDir: filepath.Join(data, "queue"),
	}
	conn, err := net.ListenPacket("udp", ":"+port)
	if err != nil {
		fatal("listen", "port", port, "err", err)
	}
	slog.Info("relay listening", "port", port, "vps", s.url, "deviceIDs", allowed, "data", data)
	go func() {
		s.flush()
		for range time.Tick(time.Hour) {
			s.flush()
		}
	}()
	slog.Error("ehome listener stopped", "err", serve(conn, &ehome{allowed: allowed, registered: map[string]bool{}}, s))
	os.Exit(1)
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
			if err := s.post(*msg); err != nil {
				slog.Warn("forward failed, dropped", "command", msg.Command, "deviceID", msg.DeviceID, "err", err)
			}
		}
	}
}
