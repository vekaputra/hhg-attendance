package main

import (
	"bytes"
	"encoding/xml"
	"io"
	"net"
	"strings"
	"testing"
)

// Trimmed from a DS-K1A8503MF capture; real ACS packets carry ~8 KB of NUL padding.
const (
	registerPkt = `<?xml version="1.0" encoding="UTF-8"?>` + "\n\r<PPVSPMessage>\n\r<Version>4.0</Version>\n\r<Sequence>97</Sequence>\n\r" +
		"<CommandType>REQUEST</CommandType>\n\r<Command>REGISTER</Command>\n\r<Params>\n\r<NetUnitType>PU</NetUnitType>\n\r" +
		"<DeviceID>admin</DeviceID>\n\r<Password>secret</Password>\n\r<DevDetailedType>DS-K1A8503MF</DevDetailedType>\n\r</Params>\n\r</PPVSPMessage>"
	acsPkt = `<?xml version="1.0" encoding="GBK"?>` + "\n\r<PPVSPMessage>\n\r<Version>2.5</Version>\n\r<Sequence>99</Sequence>\n\r" +
		"<CommandType>REQUEST</CommandType>\n\r<Command>ACS</Command>\n\r<Params>\n\r<DeviceID>admin</DeviceID>\n\r" +
		"<Time>2026-10-18 10:42:03</Time>\n\r<MajorType>0x5</MajorType>\n\r<MinorType>0x26</MinorType>\n\r<CardNo></CardNo>\n\r" +
		"<employeeNoString>HHG0001</employeeNoString><attendanceStatus>checkIn</attendanceStatus>\n\r<serialNo>83</serialNo>\n\r" +
		"</Params>\n\r</PPVSPMessage>"
)

func TestHandleEHome(t *testing.T) {
	alarmIP, allowedDeviceIDs = "10.0.0.2", []string{"door2", "admin"}
	device := &net.UDPAddr{IP: net.IPv4(192, 168, 100, 72), Port: 54288}
	stranger := &net.UDPAddr{IP: net.IPv4(192, 168, 100, 99), Port: 54288}
	registered := map[string]bool{}

	// Steps share registered, so order matters.
	steps := []struct {
		name        string
		from        net.Addr
		pkt         string
		wantSeq     int
		wantStatus  int
		wantAlarmIP string
	}{
		{"acs before register", device, acsPkt, 99, 403, ""},
		{"register with unknown device ID", device, strings.Replace(registerPkt, ">admin<", ">door9<", 1), 97, 401, ""},
		{"register", device, registerPkt, 97, 200, "10.0.0.2"},
		{"nul-padded acs", device, acsPkt + strings.Repeat("\x00", 8082), 99, 200, ""},
		{"acs from unregistered sender", stranger, acsPkt, 99, 403, ""},
	}
	for _, s := range steps {
		t.Run(s.name, func(t *testing.T) {
			var got struct {
				Sequence int    `xml:"Sequence"`
				Status   int    `xml:"Status"`
				AlarmIP  string `xml:"Params>AlarmServerIP"`
			}
			d := xml.NewDecoder(bytes.NewReader(handleEHome([]byte(s.pkt), s.from, registered)))
			d.CharsetReader = func(_ string, r io.Reader) (io.Reader, error) { return r, nil }
			if err := d.Decode(&got); err != nil {
				t.Fatal(err)
			}
			if got.Sequence != s.wantSeq || got.Status != s.wantStatus || got.AlarmIP != s.wantAlarmIP {
				t.Errorf("got %+v", got)
			}
		})
	}

	if handleEHome([]byte("\x01\x02 not xml"), device, registered) != nil {
		t.Error("replied to a non-XML packet")
	}
}
