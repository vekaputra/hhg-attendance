package main

// EHome 2.x/4.0 signalling: the device registers over UDP with "PPVSPMessage"
// XML, we reply with where to send alarms (this same socket), then it sends
// KEEPALIVE and ACS (access control event) requests, each acked with 200.
// Message shapes follow FreeEhome (github.com/tsingeye/FreeEhome) and a DS-K1A8503MF capture.

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"net"
	"regexp"
	"slices"
	"time"

	"hhg-attendance/internal/wire"
)

// serverAddrRE accepts an IP or hostname the device reports as RegisterServer, so it is safe to echo into the reply.
var serverAddrRE = regexp.MustCompile(`^[A-Za-z0-9.:-]{1,253}$`)

// ppvsp holds the fields we use; REGISTER's Password is deliberately not read so it never reaches the log.
type ppvsp struct {
	Sequence    int    `xml:"Sequence"`
	CommandType string `xml:"CommandType"`
	Command     string `xml:"Command"`
	Params      struct {
		DeviceID         string `xml:"DeviceID"`
		RegisterServer   string `xml:"RegisterServer"`
		Model            string `xml:"DevDetailedType"`
		Firmware         string `xml:"FirmwareVersion"`
		Time             string `xml:"Time"`
		MajorType        string `xml:"MajorType"`
		MinorType        string `xml:"MinorType"`
		CardNo           string `xml:"CardNo"`
		EmployeeNo       string `xml:"employeeNoString"`
		AttendanceStatus string `xml:"attendanceStatus"`
		SerialNo         string `xml:"serialNo"`
	} `xml:"Params"`
}

// ehome is the relay's side of the EHome protocol. It is used from the one serve loop only.
type ehome struct {
	allowed    []string        // RELAY_DEVICE_IDS
	registered map[string]bool // sender addresses that passed REGISTER
}

// handle returns the reply for the device and, for anything but REGISTER/KEEPALIVE from a registered sender,
// the message to forward (ACS fields filled and Hash set; other commands carry Raw). reply is the 200 ack.
// shortcut: senders are trusted by UDP source address, which anyone on the LAN can spoof; upgrade if the LAN is shared.
func (e *ehome) handle(pkt []byte, from net.Addr) (reply []byte, msg *wire.Message) {
	pkt = bytes.TrimRight(pkt, "\x00") // ACS events arrive NUL-padded to a fixed size
	var m ppvsp
	d := xml.NewDecoder(bytes.NewReader(pkt))
	// shortcut: GBK/GB2312 is read as-is, so only ASCII text survives; decode with golang.org/x/text if names are Chinese.
	d.CharsetReader = func(_ string, r io.Reader) (io.Reader, error) { return r, nil }
	if err := d.Decode(&m); err != nil {
		// Not the data: an unreadable packet may be a REGISTER, which carries the device password.
		slog.Warn("ehome unreadable packet", "from", from, "bytes", len(pkt), "err", err)
		return nil, nil
	}

	p := m.Params
	switch {
	case m.Command == "REGISTER":
		if !slices.Contains(e.allowed, p.DeviceID) {
			slog.Warn("register rejected", "from", from, "deviceID", p.DeviceID)
			return response(m, 401, "Unauthorized", "", ""), nil
		}
		e.registered[from.String()] = true
		// Send events to the address the device was pointed at, which is this Pi's LAN IP.
		ip := p.RegisterServer
		if serverAddrRE.MatchString(ip) && net.ParseIP(ip) == nil {
			ip = resolveIPv4(ip)
		}
		if !serverAddrRE.MatchString(ip) {
			ip = localIPFor(from)
		}
		slog.Info("device registered", "from", from, "deviceID", p.DeviceID, "model", p.Model, "firmware", p.Firmware, "alarmTo", ip)
		// AlarmServerType 0 = UDP only.
		return response(m, 200, "OK", "", fmt.Sprintf("<KeepAliveSeconds>15</KeepAliveSeconds><AlarmServerIP>%s</AlarmServerIP>"+
			"<AlarmServerPort>%s</AlarmServerPort><AlarmServerType>0</AlarmServerType>", ip, port)), nil
	case !e.registered[from.String()]:
		// Employee and time are logged so a real device's taps aren't lost silently, e.g. right after the relay restarts.
		slog.Warn("ignored, sender not registered", "from", from, "command", m.Command, "employeeNo", p.EmployeeNo, "time", p.Time)
		// 403 with Method REGISTER asks the device to register again (FreeEhome does this for keepalives).
		return response(m, 403, "Forbidden", "<Method>REGISTER</Method>", ""), nil
	case m.Command == "ACS":
		if p.EmployeeNo == "" {
			slog.Info("event", "time", p.Time, "major", p.MajorType, "minor", p.MinorType, "serialNo", p.SerialNo, "deviceID", p.DeviceID)
		} else {
			slog.Info("tap",
				"employeeNo", p.EmployeeNo,
				"time", p.Time,
				"status", p.AttendanceStatus,
				"major", p.MajorType,
				"minor", p.MinorType,
				"cardNo", p.CardNo,
				"serialNo", p.SerialNo,
				"deviceID", p.DeviceID,
			)
		}
		msg = &wire.Message{Command: m.Command, DeviceID: p.DeviceID, SerialNo: p.SerialNo, Time: p.Time, Major: p.MajorType,
			Minor: p.MinorType, EmployeeNo: p.EmployeeNo, CardNo: p.CardNo, AttendanceStatus: p.AttendanceStatus}
		msg.Hash = wire.EventHash(*msg)
	case m.Command != "KEEPALIVE":
		slog.Info("ehome packet", "from", from, "data", string(pkt))
		msg = &wire.Message{Command: m.Command, DeviceID: p.DeviceID, Raw: string(pkt)}
	}
	return response(m, 200, "OK", "", ""), msg
}

// response builds the RESPONSE to request m; responses from the device get none.
func response(m ppvsp, status int, desc, method, params string) []byte {
	if m.CommandType != "REQUEST" {
		return nil
	}
	return fmt.Appendf(nil, `<?xml version="1.0" encoding="GB2312"?>`+
		"<PPVSPMessage><Version>2.5</Version><Sequence>%d</Sequence><CommandType>RESPONSE</CommandType>%s"+
		"<WhichCommand>%s</WhichCommand><Status>%d</Status><Description>%s</Description><Params>%s</Params></PPVSPMessage>",
		m.Sequence, method, m.Command, status, desc, params)
}

// resolveIPv4 turns the domain a device was pointed at into an IPv4 address, since the firmware may not accept a
// hostname as AlarmServerIP. If the lookup fails it returns the name unchanged for the device to try itself.
func resolveIPv4(host string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip4", host)
	if err != nil || len(ips) == 0 {
		slog.Warn("could not resolve RegisterServer; sending the name to the device", "host", host, "err", err)
		return host
	}
	return ips[0].String()
}

// localIPFor returns our IP on the route to addr, which is the one the device reached on a LAN.
func localIPFor(addr net.Addr) string {
	c, err := net.Dial("udp", addr.String())
	if err != nil {
		return ""
	}
	defer c.Close()
	return c.LocalAddr().(*net.UDPAddr).IP.String()
}
