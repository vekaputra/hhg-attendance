package wire

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// Message is one device message the relay forwards to POST /api/events, as JSON.
type Message struct {
	Command          string `json:"command"`        // "ACS", or any other EHome command
	Hash             string `json:"hash,omitempty"` // ACS only: EventHash(m)
	DeviceID         string `json:"device_id"`
	SerialNo         string `json:"serial_no,omitempty"`
	Time             string `json:"time,omitempty"` // device time as sent, "2006-01-02 15:04:05", no zone
	Major            string `json:"major,omitempty"`
	Minor            string `json:"minor,omitempty"`
	EmployeeNo       string `json:"employee_no,omitempty"`
	CardNo           string `json:"card_no,omitempty"`
	AttendanceStatus string `json:"attendance_status,omitempty"`
	Raw              string `json:"raw,omitempty"` // non-ACS only: the packet, NULs trimmed
}

// EventHash is the SHA-256 (lowercase hex) of the event's own fields joined with \x1f; never the raw packet,
// whose Sequence changes on every resend.
func EventHash(m Message) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{m.DeviceID, m.SerialNo, m.Time, m.Major, m.Minor, m.EmployeeNo, m.CardNo}, "\x1f")))
	return hex.EncodeToString(sum[:])
}
