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
)

// allowedDeviceIDs are the Device IDs (from each device's platform access
// settings) allowed to register. Fill these in.
var allowedDeviceIDs = []string{"gshhgfp"}

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

func listenEHome(addr string, st *Store) error {
	conn, err := net.ListenPacket("udp", addr)
	if err != nil {
		return err
	}
	slog.Info("ehome listening", "addr", addr)
	registered := map[string]bool{} // sender addresses that passed REGISTER
	buf := make([]byte, 64<<10)
	for {
		n, from, err := conn.ReadFrom(buf)
		if err != nil {
			return err
		}
		if reply := handleEHome(buf[:n], from, registered, st); reply != nil {
			if _, err := conn.WriteTo(reply, from); err != nil {
				slog.Warn("ehome reply failed", "to", from, "err", err)
			}
		}
	}
}

// handleEHome logs one packet, records device contact, events and taps in st, and returns the reply to send, if any.
// A nil reply for a request means the event or tap could not be stored, so the device resends it.
// shortcut: senders are trusted by UDP source address, which anyone on the internet can spoof; the firewall allow-list of the devices' public IPs is what protects this.
func handleEHome(pkt []byte, from net.Addr, registered map[string]bool, st *Store) []byte {
	pkt = bytes.TrimRight(pkt, "\x00") // ACS events arrive NUL-padded to a fixed size
	var m ppvsp
	d := xml.NewDecoder(bytes.NewReader(pkt))
	// shortcut: GBK/GB2312 is read as-is, so only ASCII text survives; decode with golang.org/x/text if names are Chinese.
	d.CharsetReader = func(_ string, r io.Reader) (io.Reader, error) { return r, nil }
	if err := d.Decode(&m); err != nil {
		slog.Warn("ehome unreadable packet", "from", from, "err", err, "data", string(pkt))
		return nil
	}

	p := m.Params
	now := time.Now()
	touch := func() {
		if err := st.TouchDevice(p.DeviceID, now); err != nil {
			slog.Warn("record device contact failed", "deviceID", p.DeviceID, "err", err)
		}
	}
	if m.Command != "REGISTER" && registered[from.String()] {
		touch()
	}
	switch {
	case m.Command == "REGISTER":
		if !slices.Contains(allowedDeviceIDs, p.DeviceID) {
			slog.Warn("register rejected", "from", from, "deviceID", p.DeviceID)
			return reply(m, 401, "Unauthorized", "", "")
		}
		registered[from.String()] = true
		touch()
		// Send events to the address the device was pointed at: behind NAT we can't see our public IP ourselves.
		ip := p.RegisterServer
		if serverAddrRE.MatchString(ip) && net.ParseIP(ip) == nil {
			ip = resolveIPv4(ip)
		}
		if !serverAddrRE.MatchString(ip) {
			ip = localIPFor(from)
			host, _, _ := net.SplitHostPort(from.String())
			if isPublic(net.ParseIP(host)) && net.ParseIP(ip).IsPrivate() {
				slog.Error("device sent no usable RegisterServer and this server is behind NAT; its events will go to a private address", "from", from, "alarmTo", ip)
			}
		}
		slog.Info("device registered", "from", from, "deviceID", p.DeviceID, "model", p.Model, "firmware", p.Firmware, "alarmTo", ip)
		// AlarmServerType 0 = UDP only.
		return reply(m, 200, "OK", "", fmt.Sprintf("<KeepAliveSeconds>15</KeepAliveSeconds><AlarmServerIP>%s</AlarmServerIP>"+
			"<AlarmServerPort>%s</AlarmServerPort><AlarmServerType>0</AlarmServerType>", ip, port))
	case !registered[from.String()]:
		// Employee and time are logged so a real device's taps aren't lost silently, e.g. right after this server restarts.
		slog.Warn("ignored, sender not registered", "from", from, "command", m.Command, "employeeNo", p.EmployeeNo, "time", p.Time)
		// 403 with Method REGISTER asks the device to register again (FreeEhome does this for keepalives).
		return reply(m, 403, "Forbidden", "<Method>REGISTER</Method>", "")
	case m.Command == "ACS":
		// Every event is stored, whether or not it carries an employee number: unrecognised fingerprints, doors, alarms and menu logins are a security trail.
		if p.EmployeeNo == "" {
			slog.Info("event", "time", p.Time, "major", p.MajorType, "minor", p.MinorType, "deviceID", p.DeviceID)
		} else {
			// shortcut: denied attempts by a known user are logged as taps too; filter on minor (e.g. 0x1 card, 0x26 fingerprint, 0x4b face pass) if they must not count.
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
		at, err := time.ParseInLocation(stampFmt, p.Time, st.loc)
		if err != nil {
			// Resending wouldn't fix an unreadable time, so ack and drop it.
			slog.Warn("event time unreadable, not stored", "employeeNo", p.EmployeeNo, "time", p.Time, "serialNo", p.SerialNo, "deviceID", p.DeviceID, "err", err)
			break
		}
		// A store error means no ack, so the device resends; a value too long for its column fails the same way on every resend, so that one is acked and dropped.
		if err := st.InsertEvent(Event{DeviceID: p.DeviceID, SerialNo: p.SerialNo, Major: p.MajorType, Minor: p.MinorType, EmployeeID: p.EmployeeNo, Time: at}, now); err != nil {
			slog.Error("store event failed", "time", p.Time, "major", p.MajorType, "minor", p.MinorType, "serialNo", p.SerialNo, "deviceID", p.DeviceID, "err", err)
			if !isTooLong(err) {
				return nil
			}
		}
		if p.EmployeeNo != "" {
			if err := st.InsertTap(Tap{DeviceID: p.DeviceID, SerialNo: p.SerialNo, EmployeeID: p.EmployeeNo, Time: at}, now); err != nil {
				slog.Error("store tap failed", "employeeNo", p.EmployeeNo, "time", p.Time, "serialNo", p.SerialNo, "deviceID", p.DeviceID, "err", err)
				if !isTooLong(err) {
					return nil
				}
			}
		}
	case m.Command != "KEEPALIVE":
		slog.Info("ehome packet", "from", from, "data", string(pkt))
	}
	return reply(m, 200, "OK", "", "")
}

// reply builds the RESPONSE to request m; responses from the device get none.
func reply(m ppvsp, status int, desc, method, params string) []byte {
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

// isPublic reports whether ip is an internet address: not private, loopback, link-local, multicast or unspecified.
func isPublic(ip net.IP) bool { return ip.IsGlobalUnicast() && !ip.IsPrivate() }
