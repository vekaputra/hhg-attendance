package main

import (
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

// partTimeFixture: in the Cafe, part-timer PT1 (active from 10-01), full-timer FT1, and part-timer PT2 active from
// 11-10. The clock is 2026-11-27 10:00, so the default period is November (26 Oct – 25 Nov).
// PT1: 10-25 outside the period; 10-26 06:00/15:00 with a 10:00–13:00 break (6); 10-27 12:40:59/22:23:22 (8, capped);
// 10-28 and 10-29 one tap each (?). PT2: 11-05 before its active period; 11-12 09:00/15:00 (6).
func partTimeFixture(t *testing.T) (h http.Handler, session *http.Cookie, st *Store, cafe Org) {
	t.Helper()
	st = newReportStore(t)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	orgs, err := st.Orgs()
	must(err)
	cafe = orgs[1]
	now := at("2026-11-27 10:00:00")
	for _, e := range []Employee{
		{ID: "PT1", Name: "Putri", ActiveFrom: "2026-10-01", PartTime: true},
		{ID: "FT1", Name: "Fajar", ActiveFrom: "2026-10-01"},
		{ID: "PT2", Name: "Rina", ActiveFrom: "2026-11-10", PartTime: true},
	} {
		e.OrgID = cafe.ID
		must(st.AddEmployee(e))
	}
	for i, tap := range []Tap{
		{EmployeeID: "PT1", Time: at("2026-10-25 08:00:00")},
		{EmployeeID: "PT1", Time: at("2026-10-25 16:00:00")},
		{EmployeeID: "PT1", Time: at("2026-10-26 06:00:00")},
		{EmployeeID: "PT1", Time: at("2026-10-26 15:00:00")},
		{EmployeeID: "PT1", Time: at("2026-10-27 12:40:59")},
		{EmployeeID: "PT1", Time: at("2026-10-27 22:23:22")},
		{EmployeeID: "PT1", Time: at("2026-10-28 09:00:00")},
		{EmployeeID: "PT1", Time: at("2026-10-29 18:00:00")},
		{EmployeeID: "PT2", Time: at("2026-11-05 09:00:00")},
		{EmployeeID: "PT2", Time: at("2026-11-05 17:00:00")},
		{EmployeeID: "PT2", Time: at("2026-11-12 09:00:00")},
		{EmployeeID: "PT2", Time: at("2026-11-12 15:00:00")},
		{EmployeeID: "FT1", Time: at("2026-10-27 09:00:00")},
		{EmployeeID: "FT1", Time: at("2026-10-27 17:00:00")},
	} {
		tap.DeviceID, tap.SerialNo = "d", fmt.Sprint(i)
		must(st.InsertTap(tap, now))
	}
	_, err = st.AddBreak(Break{EmployeeID: "PT1", Day: date("2026-10-26"), Start: at("2026-10-26 10:00:00"),
		End: at("2026-10-26 13:00:00"), By: "ana", At: now})
	must(err)

	must(st.SetUser("ana", "correct horse"))
	token, err := st.NewSession("ana", now)
	must(err)
	return newServer(st, testLoc, func() time.Time { return now }), &http.Cookie{Name: sessionCookie, Value: token}, st, cafe
}

var (
	rowRE    = regexp.MustCompile(`(?s)<tr>.*?</tr>`)
	cellRE   = regexp.MustCompile(`(?s)<td[^>]*>(.*?)</td>`)
	buttonRE = regexp.MustCompile(`<button[^>]*>([^<]*)</button>`)
	tagRE    = regexp.MustCompile(`(?s)<div popover.*?</div>|<[^>]+>`)
)

// ptCells returns the text of each cell in empID's row of the table with the given class, nil when it has no row.
func ptCells(t *testing.T, page, class, empID string) []string {
	t.Helper()
	_, table, ok := strings.Cut(page, `<table class="`+class+`"`)
	if !ok {
		t.Fatalf("page has no %s table", class)
	}
	table, _, _ = strings.Cut(table, "</table>")
	for _, row := range rowRE.FindAllString(table, -1) {
		if !strings.Contains(row, "<small>"+empID+"</small>") {
			continue
		}
		var cells []string
		for _, m := range cellRE.FindAllStringSubmatch(row, -1) {
			if b := buttonRE.FindStringSubmatch(m[1]); b != nil {
				cells = append(cells, b[1])
			} else {
				cells = append(cells, strings.Join(strings.Fields(tagRE.ReplaceAllString(m[1], "")), " "))
			}
		}
		return cells
	}
	return nil
}

func getPartTime(t *testing.T, h http.Handler, session *http.Cookie, query string) string {
	t.Helper()
	w := getWithCookies(h, "/part-time"+query, session)
	if w.Code != 200 {
		t.Fatalf("GET /part-time%s = %d, want 200", query, w.Code)
	}
	return w.Body.String()
}

func TestPartTimePageDefaults(t *testing.T) {
	h, session, _, _ := partTimeFixture(t)
	page := getPartTime(t, h, session, "")
	wantAll(t, page, "26 Oct – 25 Nov 2026", `<option value="2026-11" selected>November 2026</option>`,
		`<option value="2026-10">October 2026</option>`, `<option value="2026-12">December 2026</option>`,
		"Mon 26 Oct", "10:00", "13:00")
	for _, s := range []string{"25 Oct", "September 2026", "FT1", "Fajar"} {
		if strings.Contains(page, s) {
			t.Errorf("page shows %q", s)
		}
	}

	pt1 := ptCells(t, page, "ptgrid", "PT1")
	if len(pt1) != 31 || !slices.Equal(pt1[:4], []string{"6", "8", "?", "?"}) {
		t.Errorf("PT1 grid = %q, want 31 days starting 6 8 ? ?", pt1)
	}
	pt2 := ptCells(t, page, "ptgrid", "PT2")
	if len(pt2) != 31 || pt2[10] != "" || pt2[17] != "6" { // 5 Nov and 12 Nov
		t.Errorf("PT2 grid = %q, want 5 Nov blank and 12 Nov 6", pt2)
	}

	for _, c := range []struct {
		emp  string
		want []string
	}{
		{"PT1", []string{"2", "14 +?", "Rp 130.000", "Rp 227.500 +?"}},
		{"PT2", []string{"1", "6", "Rp 130.000", "Rp 97.500"}},
	} {
		if got := ptCells(t, page, "data ptsum", c.emp); !slices.Equal(got, c.want) {
			t.Errorf("%s summary = %q, want %q", c.emp, got, c.want)
		}
	}
	wantAll(t, page, "Rp 325.000")
}

func TestPartTimePageMonthParam(t *testing.T) {
	h, session, _, _ := partTimeFixture(t)
	wantAll(t, getPartTime(t, h, session, "?month=2026-12"), "26 Nov – 25 Dec 2026", `<option value="2026-12" selected>December 2026</option>`)
	if w := getWithCookies(h, "/part-time?month=bad", session); w.Code != http.StatusBadRequest {
		t.Errorf("GET /part-time?month=bad = %d, want 400", w.Code)
	}
}

// Unflagging hides a part-timer without deleting their breaks; flagging them again brings the same hours back.
func TestPartTimeFlagOff(t *testing.T) {
	h, session, st, cafe := partTimeFixture(t)
	if err := st.UpdateEmployee("PT1", "Putri", cafe.ID, false); err != nil {
		t.Fatal(err)
	}
	if page := getPartTime(t, h, session, ""); strings.Contains(page, "PT1") {
		t.Error("unflagged PT1 is still shown")
	}
	if b, err := st.Breaks(date("2026-10-26"), date("2026-10-26")); err != nil || len(b) != 1 {
		t.Fatalf("breaks = %v, %v; want PT1's break kept", b, err)
	}
	if err := st.UpdateEmployee("PT1", "Putri", cafe.ID, true); err != nil {
		t.Fatal(err)
	}
	if got := ptCells(t, getPartTime(t, h, session, ""), "ptgrid", "PT1"); len(got) == 0 || got[0] != "6" {
		t.Errorf("PT1 grid = %q, want 6 on 26 Oct", got)
	}
}

func postPartTime(t *testing.T, h http.Handler, session *http.Cookie, path string, kv ...string) {
	t.Helper()
	w := postForm(h, path, formOf(append([]string{"month", "2026-11"}, kv...)...), session)
	if w.Code != 303 || w.Header().Get("Location") != "/part-time?month=2026-11" {
		t.Fatalf("POST %s = %d to %q, want 303 to the November period: %s", path, w.Code, w.Header().Get("Location"), w.Body)
	}
}

// rejected posts a form that must be refused with 400 and msg, and returns the re-rendered page.
func rejected(t *testing.T, h http.Handler, session *http.Cookie, path, msg string, kv ...string) string {
	t.Helper()
	w := postForm(h, path, formOf(append([]string{"month", "2026-11"}, kv...)...), session)
	if w.Code != 400 || !strings.Contains(w.Body.String(), msg) {
		t.Errorf("POST %s %v = %d, want 400 with %q", path, kv, w.Code, msg)
	}
	return w.Body.String()
}

func TestPartTimeFillMissing(t *testing.T) {
	h, session, _, _ := partTimeFixture(t)
	postPartTime(t, h, session, "/part-time/corrections", "emp", "PT1", "day", "2026-10-28", "in", "09:00", "out", "17:30")
	page := getPartTime(t, h, session, "")
	if got := ptCells(t, page, "ptgrid", "PT1"); got[2] != "8" || got[3] != "?" {
		t.Errorf("PT1 grid = %q, want 28 Oct 8 and 29 Oct still ?", got[:4])
	}
	wantAll(t, page, "09:00 → 17:30", "worked 8h30m", "(capped at 8)")
	if got := ptCells(t, page, "data ptsum", "PT1"); got[1] != "22 +?" {
		t.Errorf("PT1 hours = %q, want 22 +?", got[1])
	}
	wantAll(t, getWithCookies(h, "/person?emp=PT1&month=2026-10", session).Body.String(), "manager edit")
}

func TestPartTimeFillAfterMidnight(t *testing.T) {
	h, session, _, _ := partTimeFixture(t)
	postPartTime(t, h, session, "/part-time/corrections", "emp", "PT1", "day", "2026-10-29", "in", "18:00", "out", "00:30")
	if got := ptCells(t, getPartTime(t, h, session, ""), "ptgrid", "PT1"); got[3] != "7" {
		t.Errorf("PT1 29 Oct = %q, want 7", got[3])
	}
	postPartTime(t, h, session, "/part-time/corrections", "emp", "PT1", "day", "2026-10-28", "in", "09:00", "out", "17:30")
	if got := ptCells(t, getPartTime(t, h, session, ""), "data ptsum", "PT1"); got[1] != "29" {
		t.Errorf("PT1 hours = %q, want 29 with no +?", got[1])
	}
}

func TestPartTimeFillReject(t *testing.T) {
	h, session, _, _ := partTimeFixture(t)
	page := rejected(t, h, session, "/part-time/corrections", "check-out must be after check-in",
		"emp", "PT1", "day", "2026-10-28", "in", "09:00", "out", "08:00")
	// The message and the submitted values are in the dialog that the page reopens.
	before, after, _ := strings.Cut(page, "check-out must be after check-in")
	i := strings.LastIndex(before, "<dialog")
	if i < 0 {
		t.Fatal("page has no dialog before the message")
	}
	after, _, _ = strings.Cut(after, "</dialog>")
	if strings.Contains(before[i:], "</dialog>") || !strings.Contains(after, `value="08:00"`) {
		t.Errorf("message is not inside the dialog with the submitted values: %s", before[i:])
	}
	wantAll(t, page, ".showModal()</script>")
}

func TestPartTimeAddBreak(t *testing.T) {
	h, session, st, _ := partTimeFixture(t)
	postPartTime(t, h, session, "/part-time/breaks", "emp", "PT1", "start_date", "2026-10-27", "start_time", "15:00",
		"end_date", "2026-10-27", "end_time", "17:00")
	page := getPartTime(t, h, session, "")
	if got := ptCells(t, page, "ptgrid", "PT1"); got[1] != "8" {
		t.Errorf("PT1 27 Oct = %q, want 8", got[1])
	}
	wantAll(t, page, "12:40 → 15:00, 17:00 → 22:23", "worked 7h42m · break 2h00m")

	breaks := mustGet(st.Breaks(date("2026-10-27"), date("2026-10-27")))
	if len(breaks) != 1 {
		t.Fatalf("breaks on 27 Oct = %v, want 1", breaks)
	}
	postPartTime(t, h, session, fmt.Sprintf("/part-time/breaks/%d/delete", breaks[0].ID))
	if page := getPartTime(t, h, session, ""); strings.Contains(page, "break 2h00m") || !strings.Contains(page, "12:40 → 22:23") {
		t.Error("27 Oct still shows the deleted break")
	}

	postPartTime(t, h, session, "/part-time/breaks", "emp", "PT2", "start_date", "2026-11-12", "start_time", "23:00",
		"end_date", "2026-11-13", "end_time", "00:30")
	if b := mustGet(st.Breaks(date("2026-11-12"), date("2026-11-13"))); len(b) != 1 || !b[0].Day.Equal(date("2026-11-12")) {
		t.Errorf("overnight break = %v, want one on 12 Nov", b)
	}
	wantAll(t, getPartTime(t, h, session, ""), "Thu 12 Nov", "00:30 (&#43;1)") // html/template escapes +
}

func TestPartTimeBreakRejects(t *testing.T) {
	h, session, st, _ := partTimeFixture(t)
	for _, c := range []struct{ emp, sd, st, ed, et, msg string }{
		{"NOPE", "2026-10-27", "15:00", "2026-10-27", "17:00", "unknown employee"},
		{"FT1", "2026-10-27", "15:00", "2026-10-27", "17:00", "breaks are only for part-time employees"},
		{"PT1", "2026-10-27", "3pm", "2026-10-27", "17:00", "bad break start"},
		{"PT1", "2026-10-27", "15:00", "27-10-2026", "17:00", "bad break end"},
		{"PT1", "2026-10-27", "15:00", "2026-10-27", "15:00", "break must end after it starts"},
		{"PT1", "2026-10-28", "02:30", "2026-10-28", "03:30", "a break must end by 03:00 the next morning"},
		{"PT2", "2026-11-05", "12:00", "2026-11-05", "13:00", "employee was not active that day"},
	} {
		rejected(t, h, session, "/part-time/breaks", c.msg,
			"emp", c.emp, "start_date", c.sd, "start_time", c.st, "end_date", c.ed, "end_time", c.et)
	}
	if b := mustGet(st.Breaks(date("2026-10-01"), date("2026-11-30"))); len(b) != 1 {
		t.Errorf("breaks = %v, want only the fixture's", b)
	}
	// Ending exactly when the attendance day ends is allowed.
	postPartTime(t, h, session, "/part-time/breaks", "emp", "PT1", "start_date", "2026-10-27", "start_time", "23:00",
		"end_date", "2026-10-28", "end_time", "03:00")
}

func TestPartTimeWageForm(t *testing.T) {
	h, session, _, _ := partTimeFixture(t)
	postPartTime(t, h, session, "/part-time/wage", "wage", "150000")
	if got := ptCells(t, getPartTime(t, h, session, ""), "data ptsum", "PT1"); got[3] != "Rp 262.500 +?" {
		t.Errorf("PT1 November pay = %q, want Rp 262.500 +?", got[3])
	}
	if got := ptCells(t, getPartTime(t, h, session, "?month=2026-10"), "data ptsum", "PT1"); got[3] != "Rp 150.000" {
		t.Errorf("PT1 October pay = %q, want Rp 150.000", got[3])
	}
	for _, v := range []string{"abc", "-5", "0", "1.5"} {
		rejected(t, h, session, "/part-time/wage", "wage must be a whole number of rupiah", "wage", v)
	}
}
