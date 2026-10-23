package wire

import "testing"

func TestEventHash(t *testing.T) {
	base := Message{Command: "ACS", DeviceID: "gshhgfp", SerialNo: "228", Time: "2026-10-22 18:03:59", Major: "0x5", Minor: "0x26", EmployeeNo: "HHG0005"}
	const want = "a973307d7724ed4fc6bea79d74c58b43a610cb7d2022503ab831ed8ba72655ca"
	if got := EventHash(base); got != want {
		t.Fatalf("EventHash = %s; want %s", got, want)
	}

	for name, change := range map[string]func(*Message){
		"DeviceID":   func(m *Message) { m.DeviceID = "other" },
		"SerialNo":   func(m *Message) { m.SerialNo = "229" },
		"Time":       func(m *Message) { m.Time = "2026-10-22 18:04:00" },
		"Major":      func(m *Message) { m.Major = "0x1" },
		"Minor":      func(m *Message) { m.Minor = "0x31" },
		"EmployeeNo": func(m *Message) { m.EmployeeNo = "HHG0006" },
		"CardNo":     func(m *Message) { m.CardNo = "1234" },
	} {
		m := base
		change(&m)
		if EventHash(m) == want {
			t.Errorf("changing %s did not change the hash", name)
		}
	}

	for name, change := range map[string]func(*Message){
		"Command":          func(m *Message) { m.Command = "REGISTER" },
		"Hash":             func(m *Message) { m.Hash = "x" },
		"AttendanceStatus": func(m *Message) { m.AttendanceStatus = "in" },
		"Raw":              func(m *Message) { m.Raw = "packet" },
	} {
		m := base
		change(&m)
		if EventHash(m) != want {
			t.Errorf("changing %s changed the hash", name)
		}
	}
}
