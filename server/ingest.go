package main

import (
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"hhg-attendance/internal/wire"
)

// relayUser and relayPassword are the basic-auth login of the Pi relay, set from the environment in main.
// While either is empty every ingest request is refused.
var relayUser, relayPassword string

// ingestHandler stores one device message the Pi relay forwards. Every 200 has an empty body and means "stored or
// deliberately dropped, delete it from your queue"; anything else is retried by the relay.
func ingestHandler(st *Store, loc *time.Location, now func() time.Time) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if relayUser == "" || relayPassword == "" || !ok ||
			subtle.ConstantTimeCompare([]byte(user), []byte(relayUser)) != 1 ||
			subtle.ConstantTimeCompare([]byte(pass), []byte(relayPassword)) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="relay"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		var m wire.Message
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&m); err != nil || m.Command == "" || m.DeviceID == "" {
			http.Error(w, "bad message", http.StatusBadRequest)
			return
		}
		if m.Command != "ACS" {
			slog.Info("relay message", "command", m.Command, "deviceID", m.DeviceID, "raw", m.Raw)
			return
		}
		// A wrong hash is a relay bug; failing loudly keeps the event in the relay's queue.
		if m.Hash != wire.EventHash(m) {
			slog.Error("relay event hash mismatch", "deviceID", m.DeviceID, "serialNo", m.SerialNo, "hash", m.Hash)
			http.Error(w, "bad hash", http.StatusBadRequest)
			return
		}

		// Every event is stored, whether or not it carries an employee number: unrecognised fingerprints, doors, alarms and menu logins are a security trail.
		at, err := time.ParseInLocation(stampFmt, m.Time, loc)
		if err != nil {
			// Resending wouldn't fix an unreadable time, so ack and drop it.
			slog.Warn("event time unreadable, not stored", "employeeNo", m.EmployeeNo, "time", m.Time, "serialNo", m.SerialNo, "deviceID", m.DeviceID, "err", err)
			return
		}
		// A store error is a 500, so the relay resends; a value too long for its column fails the same way on every resend, so that one is acked and dropped.
		received := now()
		if err := st.InsertEvent(Event{DeviceID: m.DeviceID, SerialNo: m.SerialNo, Major: m.Major, Minor: m.Minor, EmployeeID: m.EmployeeNo, Time: at, Hash: m.Hash}, received); err != nil {
			slog.Error("store event failed", "time", m.Time, "major", m.Major, "minor", m.Minor, "serialNo", m.SerialNo, "deviceID", m.DeviceID, "err", err)
			if !isTooLong(err) {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
		}
		if m.EmployeeNo != "" {
			// shortcut: denied attempts by a known user are logged as taps too; filter on minor (e.g. 0x1 card, 0x26 fingerprint, 0x4b face pass) if they must not count.
			if err := st.InsertTap(Tap{DeviceID: m.DeviceID, SerialNo: m.SerialNo, EmployeeID: m.EmployeeNo, Time: at}, received); err != nil {
				slog.Error("store tap failed", "employeeNo", m.EmployeeNo, "time", m.Time, "serialNo", m.SerialNo, "deviceID", m.DeviceID, "err", err)
				if !isTooLong(err) {
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
			}
		}
	})
}
