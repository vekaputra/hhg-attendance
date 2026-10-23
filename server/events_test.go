package main

import (
	"maps"
	"strconv"
	"strings"
	"testing"
)

func TestDescribeEvent(t *testing.T) {
	for _, c := range []struct{ major, minor, label, tone string }{
		{"0x5", "0x26", "Fingerprint matched", "ok"},
		{"0X5", "0X4B", "Face matched", "ok"}, // hex digits in either case
		{"0x5", "0x31", "Fingerprint not recognised", "bad"},
		{"0x5", "0x6", "Card has no permission", "bad"},
		{"0x5", "0x15", "Door unlocked", "muted"},
		{"0x3", "0x50", "Device menu login", "muted"},
		// Unknown minors fall back to the major.
		{"0x1", "0x99", "Alarm", "warn"},
		{"0x2", "0x1", "Device problem", "warn"},
		{"0x3", "0x1", "Operation", "muted"},
		{"0x5", "0x7f", "Access event", "muted"},
		{"0x9", "0x1", "Event", "muted"},
		{"", "", "Event", "muted"},
	} {
		if label, tone := describeEvent(c.major, c.minor); label != c.label || tone != c.tone {
			t.Errorf("describeEvent(%q, %q) = %q, %q; want %q, %q", c.major, c.minor, label, tone, c.label, c.tone)
		}
	}
}

// The clock is 2026-10-18 12:00. The filter offers the devices that sent an event that day.
func TestEventsPage(t *testing.T) {
	h, session, st, _, _ := adminFixture(t)
	for _, e := range []Event{
		{DeviceID: "door1", SerialNo: "1", Major: "0x5", Minor: "0x26", EmployeeID: "HHG0002", Time: at("2026-10-18 08:30:00")},
		{DeviceID: "door1", SerialNo: "2", Major: "0x5", Minor: "0x31", Time: at("2026-10-18 08:31:00")},
		{DeviceID: "door2", SerialNo: "7", Major: "0x5", Minor: "0x15", Time: at("2026-10-18 09:00:00")},
		{DeviceID: "door1", SerialNo: "3", Major: "0x1", Minor: "0x99", Time: at("2026-10-18 09:05:00")}, // unknown code
		{DeviceID: "old", SerialNo: "1", Major: "0x5", Minor: "0x1", EmployeeID: "ZZZ9999", Time: at("2026-10-18 09:10:00")},
		{DeviceID: "door1", SerialNo: "6", Major: "0x5", Minor: "0x6", EmployeeID: "HHG0002", Time: at("2026-10-18 09:20:00")},  // denied, by a known employee
		{DeviceID: "door1", SerialNo: "4", Major: "0x5", Minor: "0x26", EmployeeID: "HHG0002", Time: at("2026-10-19 01:30:00")}, // 10-18's day
		{DeviceID: "door1", SerialNo: "5", Major: "0x5", Minor: "0x27", Time: at("2026-10-19 03:00:00")},                        // 10-19's
	} {
		if err := st.InsertEvent(e, at("2026-10-19 04:00:00")); err != nil {
			t.Fatal(err)
		}
	}
	get := func(query string) string {
		t.Helper()
		w := getWithCookies(h, "/events?"+query, session)
		if w.Code != 200 {
			t.Fatalf("GET /events?%s = %d, want 200", query, w.Code)
		}
		return w.Body.String()
	}
	counts := func(page string) map[string]int {
		got := map[string]int{}
		for _, m := range summaryRE.FindAllStringSubmatch(page, -1) {
			got[m[1]], _ = strconv.Atoi(m[2])
		}
		return got
	}

	// The default day is today's attendance day, 10-18: the 01:30 event on 10-19 is part of it.
	page := get("")
	wantAll(t, page, "Fingerprint matched", "Fingerprint not recognised", "Door unlocked", "Card accepted",
		"0x5 / 0x26", "0x5 / 0x31", "Alarm", "0x1 / 0x99", "01:30:00", "09:10:00", "<td>door2</td>", "<td>old</td>",
		`href="/person?emp=HHG0002&amp;month=2026-10"`, "HHG0002 Budi", "ZZZ9999", "—",
		`<option value="old">`, `<option value="door2">`, "Sun 18 Oct 2026", "03:00 to 02:59")
	if strings.Contains(page, "emp=ZZZ9999") || strings.Contains(page, "didn&#39;t match") {
		t.Error("page links an unknown employee, or shows an event of another day")
	}
	// The cards are exclusive and sum to the total: accepted, failed, and everything else. The denied card of a
	// known employee is a failure, and the door and the alarm are neither.
	for _, c := range []struct {
		query string
		want  map[string]int
	}{
		{"", map[string]int{"Total events": 7, "Accepted": 3, "Failed attempts": 2, "Other": 2}},
		{"day=2026-10-18&device=door1", map[string]int{"Total events": 5, "Accepted": 2, "Failed attempts": 2, "Other": 1}},
		{"day=2026-10-18&device=door2", map[string]int{"Total events": 1, "Accepted": 0, "Failed attempts": 0, "Other": 1}},
	} {
		got := counts(get(c.query))
		if !maps.Equal(got, c.want) || got["Accepted"]+got["Failed attempts"]+got["Other"] != got["Total events"] {
			t.Errorf("GET /events?%s: summary cards = %v, want %v, summing to the total", c.query, got, c.want)
		}
	}

	// Filtering by device keeps the day and the filter in the Today link.
	page = get("day=2026-10-18&device=door2")
	wantAll(t, page, "Door unlocked", `<option value="door2" selected>`, `href="/events?day=2026-10-18&amp;device=door2">Today<`)
	if strings.Contains(page, "Fingerprint") {
		t.Error("door2 page shows another device's events")
	}
	wantAll(t, get("device=old"), "Card accepted") // seen that day

	// A well-formed device that sent nothing that day just has no events, and stays selected.
	wantAll(t, get("device=nope"), `<option value="nope" selected>`, "No events received this day.")

	// The next day starts at 03:00, and a day with nothing says so.
	wantAll(t, get("day=2026-10-19"), "Fingerprint didn&#39;t match", "03:00:00") // html/template escapes the apostrophe
	wantAll(t, get("day=2026-10-20"), "No events received this day.")

	wantAll(t, get("day=2026-10-20&device=old"), "No events received this day.")

	for _, query := range []string{"day=2026-13-45", "day=2026-10-5", "device=a%20b", "device=%3Cx%3E", "device=" + strings.Repeat("a", 65)} {
		if w := getWithCookies(h, "/events?"+query, session); w.Code != 400 {
			t.Errorf("GET /events?%s = %d, want 400", query, w.Code)
		}
	}
}
