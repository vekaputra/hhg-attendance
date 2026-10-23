package main

import (
	"bytes"
	"encoding/xml"
	"io"
	"net"
	"strings"
	"testing"

	"hhg-attendance/internal/wire"
)

// Trimmed from a DS-K1A8503MF capture; real ACS packets carry ~8 KB of NUL padding.
const (
	registerPkt = `<?xml version="1.0" encoding="UTF-8"?>` + "\n\r<PPVSPMessage>\n\r<Version>4.0</Version>\n\r<Sequence>97</Sequence>\n\r" +
		"<CommandType>REQUEST</CommandType>\n\r<Command>REGISTER</Command>\n\r<Params>\n\r<NetUnitType>PU</NetUnitType>\n\r" +
		"<DeviceID>admin</DeviceID>\n\r<Password>secret</Password>\n\r<DevDetailedType>DS-K1A8503MF</DevDetailedType>\n\r" +
		"<RegisterServer>10.0.0.2</RegisterServer>\n\r</Params>\n\r</PPVSPMessage>"
	acsPkt = `<?xml version="1.0" encoding="GBK"?>` + "\n\r<PPVSPMessage>\n\r<Version>2.5</Version>\n\r<Sequence>99</Sequence>\n\r" +
		"<CommandType>REQUEST</CommandType>\n\r<Command>ACS</Command>\n\r<Params>\n\r<DeviceID>admin</DeviceID>\n\r" +
		"<Time>2026-10-18 10:42:03</Time>\n\r<MajorType>0x5</MajorType>\n\r<MinorType>0x26</MinorType>\n\r<CardNo></CardNo>\n\r" +
		"<employeeNoString>HHG0001</employeeNoString><attendanceStatus>checkIn</attendanceStatus>\n\r<serialNo>83</serialNo>\n\r" +
		"</Params>\n\r</PPVSPMessage>"
)

func newEHome() *ehome {
	return &ehome{allowed: []string{"door2", "admin"}, registered: map[string]bool{}}
}

// replyStatus returns the Status of a reply, or 0 for no reply.
func replyStatus(t *testing.T, reply []byte) int {
	t.Helper()
	if reply == nil {
		return 0
	}
	var got struct {
		Status int `xml:"Status"`
	}
	d := xml.NewDecoder(bytes.NewReader(reply))
	d.CharsetReader = func(_ string, r io.Reader) (io.Reader, error) { return r, nil }
	if err := d.Decode(&got); err != nil {
		t.Fatal(err)
	}
	return got.Status
}

func TestHandleEHome(t *testing.T) {
	e := newEHome()
	device := &net.UDPAddr{IP: net.IPv4(192, 168, 100, 72), Port: 54288}
	stranger := &net.UDPAddr{IP: net.IPv4(192, 168, 100, 99), Port: 54288}
	fallback := localIPFor(device) // used when the device's RegisterServer can't be echoed

	// Steps share e.registered, so order matters.
	steps := []struct {
		name        string
		from        net.Addr
		pkt         string
		wantSeq     int
		wantStatus  int
		wantAlarmIP string
		wantMsg     bool
	}{
		{"acs before register", device, acsPkt, 99, 403, "", false},
		{"register with unknown device ID", device, strings.Replace(registerPkt, ">admin<", ">door9<", 1), 97, 401, "", false},
		{"register with an unsafe RegisterServer", device, strings.Replace(registerPkt, "10.0.0.2", "1.2.3.4&lt;/AlarmServerIP&gt;&lt;x&gt;", 1), 97, 200, fallback, false},
		{"register with a domain", device, strings.Replace(registerPkt, "10.0.0.2", "localhost", 1), 97, 200, "127.0.0.1", false},
		{"register with an unresolvable domain", device, strings.Replace(registerPkt, "10.0.0.2", "nothing.invalid", 1), 97, 200, "nothing.invalid", false},
		{"register", device, registerPkt, 97, 200, "10.0.0.2", false},
		{"nul-padded acs", device, acsPkt + strings.Repeat("\x00", 8082), 99, 200, "", true},
		{"acs from unregistered sender", stranger, acsPkt, 99, 403, "", false},
	}
	for _, s := range steps {
		t.Run(s.name, func(t *testing.T) {
			reply, msg := e.handle([]byte(s.pkt), s.from)
			var got struct {
				Sequence int    `xml:"Sequence"`
				Status   int    `xml:"Status"`
				AlarmIP  string `xml:"Params>AlarmServerIP"`
			}
			d := xml.NewDecoder(bytes.NewReader(reply))
			d.CharsetReader = func(_ string, r io.Reader) (io.Reader, error) { return r, nil }
			if err := d.Decode(&got); err != nil {
				t.Fatal(err)
			}
			if got.Sequence != s.wantSeq || got.Status != s.wantStatus || got.AlarmIP != s.wantAlarmIP {
				t.Errorf("got %+v", got)
			}
			if (msg != nil) != s.wantMsg {
				t.Fatalf("msg = %+v, want one: %v", msg, s.wantMsg)
			}
			if msg != nil {
				want := wire.Message{Command: "ACS", DeviceID: "admin", SerialNo: "83", Time: "2026-10-18 10:42:03", Major: "0x5", Minor: "0x26", EmployeeNo: "HHG0001", AttendanceStatus: "checkIn"}
				want.Hash = wire.EventHash(want)
				if *msg != want {
					t.Errorf("msg = %+v, want %+v", *msg, want)
				}
			}
		})
	}

	if reply, msg := e.handle([]byte("\x01\x02 not xml"), device); reply != nil || msg != nil {
		t.Errorf("non-XML packet: reply %q, msg %+v", reply, msg)
	}
}

// KEEPALIVE is answered and never forwarded; any other command is answered and forwarded raw.
func TestHandleForwardsOtherCommands(t *testing.T) {
	e := newEHome()
	device := &net.UDPAddr{IP: net.IPv4(192, 168, 100, 72), Port: 54288}
	e.handle([]byte(registerPkt), device)

	keepalive := strings.Replace(acsPkt, "<Command>ACS</Command>", "<Command>KEEPALIVE</Command>", 1)
	if reply, msg := e.handle([]byte(keepalive), device); replyStatus(t, reply) != 200 || msg != nil {
		t.Errorf("keepalive: reply %q, msg %+v", reply, msg)
	}

	other := strings.Replace(acsPkt, "<Command>ACS</Command>", "<Command>ALARM</Command>", 1)
	reply, msg := e.handle([]byte(other+"\x00\x00\x00"), device)
	if replyStatus(t, reply) != 200 {
		t.Errorf("other command: reply %q", reply)
	}
	if want := (wire.Message{Command: "ALARM", DeviceID: "admin", Raw: other}); msg == nil || *msg != want {
		t.Errorf("other command: msg = %+v, want %+v", msg, want)
	}
}
