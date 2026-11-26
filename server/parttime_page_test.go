package main

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
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
		"10:00", "13:00",
		// The grid's header: months on the first row (26–31 Oct, 1–25 Nov), day numbers only on the second.
		`<th scope="colgroup" colspan="6" class="pt-month">Oct</th><th scope="colgroup" colspan="25" class="pt-month">Nov</th>`,
		`<th scope="col">26</th><th scope="col">27</th>`)
	for _, s := range []string{"Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"} {
		if strings.Contains(page, ">"+s+" ") || strings.Contains(page, "· "+s) {
			t.Errorf("page shows the weekday %q", s)
		}
	}
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
	// A month far outside the normal October–December range adds just itself.
	for _, c := range []struct{ month, first string }{{"2001-01", "2001-01"}, {"2099-12", "2026-10"}} {
		page := getPartTime(t, h, session, "?month="+c.month)
		_, sel, _ := strings.Cut(page, `<select name="month"`)
		sel, _, _ = strings.Cut(sel, "</select>")
		if n := strings.Count(sel, "<option "); n != 4 || !strings.Contains(sel, `<option value="`+c.month+`" selected>`) ||
			!strings.HasPrefix(sel[strings.Index(sel, "<option "):], `<option value="`+c.first+`"`) {
			t.Errorf("?month=%s dropdown has %d options, want 4 from %s with %s selected", c.month, n, c.first, c.month)
		}
	}
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

// Filling only the missing check-out leaves the known check-in alone, seconds and all.
func TestPartTimeFillKeepsKnownTime(t *testing.T) {
	h, session, st, _ := partTimeFixture(t)
	if err := st.InsertTap(Tap{EmployeeID: "PT1", Time: at("2026-11-02 09:05:20"), DeviceID: "d", SerialNo: "x"}, at("2026-11-27 10:00:00")); err != nil {
		t.Fatal(err)
	}
	wantAll(t, getPartTime(t, h, session, ""), `name="in0" value="09:05"`)
	postPartTime(t, h, session, "/part-time/corrections", "emp", "PT1", "day", "2026-11-02",
		"in", "09:05", "in0", "09:05", "out", "17:00", "out0", "")
	day := date("2026-11-02")
	if got := mustGet(st.Report(0, "PT1", day, day, at("2026-11-27 10:00:00")))[0].Days[0]; !got.In.Equal(at("2026-11-02 09:05:20")) {
		t.Errorf("check-in = %v, want the tap's 09:05:20", got.In)
	}
	if fix := mustGet(st.Corrections("PT1", day, day))["2026-11-02"]; !fix.In.IsZero() || !fix.Out.Equal(at("2026-11-02 17:00:00")) {
		t.Errorf("correction = %+v, want only check-out 17:00", fix)
	}
}

// Saving the dialog without changing anything must not delete the day's existing correction.
func TestPartTimeFillUnchangedKeepsCorrection(t *testing.T) {
	h, session, st, _ := partTimeFixture(t)
	postPartTime(t, h, session, "/part-time/corrections", "emp", "PT1", "day", "2026-10-28", "in", "08:30")
	postPartTime(t, h, session, "/part-time/corrections", "emp", "PT1", "day", "2026-10-28",
		"in", "08:30", "in0", "08:30", "out", "", "out0", "")
	day := date("2026-10-28")
	if fix := mustGet(st.Corrections("PT1", day, day))["2026-10-28"]; !fix.In.Equal(at("2026-10-28 08:30:00")) {
		t.Errorf("correction = %+v, want the check-in 08:30 kept", fix)
	}
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

// Days changed by hand, a break or a manager correction, are marked so they stand out from plain tap days.
func TestPartTimeRevisedDaysMarked(t *testing.T) {
	h, session, _, _ := partTimeFixture(t)
	postPartTime(t, h, session, "/part-time/corrections", "emp", "PT2", "day", "2026-11-12", "out", "16:00", "out0", "15:00")
	page := getPartTime(t, h, session, "")
	for id, want := range map[string]bool{
		"pt-0-0":  true,  // PT1 26 Oct has a break
		"pt-0-1":  false, // PT1 27 Oct, taps only
		"pt-1-17": true,  // PT2 12 Nov, manager correction
	} {
		before, _, ok := strings.Cut(page, `popovertarget="`+id+`"`)
		if !ok {
			t.Fatalf("no button for %s", id)
		}
		tag := before[strings.LastIndex(before, "<button"):]
		if got := strings.Contains(tag, " rev"); got != want {
			t.Errorf("%s revised = %v, want %v (%s)", id, got, want, tag)
		}
	}
}

// A day with no taps (–) opens the same form, so both times can be added from the grid.
func TestPartTimeAddTimesToEmptyDay(t *testing.T) {
	h, session, _, _ := partTimeFixture(t)
	page := getPartTime(t, h, session, "")
	if got := ptCells(t, page, "ptgrid", "PT1"); got[4] != "–" {
		t.Fatalf("PT1 30 Oct = %q, want –", got[4])
	}
	wantAll(t, page, `class="pt add"`, `name="day" value="2026-10-30"`)
	postPartTime(t, h, session, "/part-time/corrections", "emp", "PT1", "day", "2026-10-30",
		"in", "09:00", "in0", "", "out", "15:30", "out0", "")
	if got := ptCells(t, getPartTime(t, h, session, ""), "ptgrid", "PT1"); got[4] != "7" {
		t.Errorf("PT1 30 Oct = %q, want 7 (6h30m rounded up)", got[4])
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
	// One line per session, then worked time, then break time.
	wantAll(t, page, "<span>12:40 → 15:00</span><span>17:00 → 22:23</span><span>worked 7h42m</span><span>break 2h00m</span>")

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
	wantAll(t, getPartTime(t, h, session, ""), "12 Nov", "00:30 (&#43;1)") // html/template escapes +
}

// A break in another period redirects to that period, where it shows.
func TestPartTimeAddBreakOtherPeriod(t *testing.T) {
	h, session, _, _ := partTimeFixture(t)
	w := postForm(h, "/part-time/breaks", formOf("month", "2026-11", "emp", "PT1", "start_date", "2026-11-26",
		"start_time", "10:00", "end_date", "2026-11-26", "end_time", "12:00"), session)
	if w.Code != 303 || w.Header().Get("Location") != "/part-time?month=2026-12" {
		t.Errorf("POST = %d to %q, want 303 to the December period", w.Code, w.Header().Get("Location"))
	}
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

// sheetRows returns each row's XML of every worksheet in an xlsx body, keyed by sheet name.
func sheetRows(t *testing.T, body []byte) map[string][]string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	read := func(name string) string {
		f, err := zr.Open(name)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		b, _ := io.ReadAll(f)
		return string(b)
	}
	names := regexp.MustCompile(`<sheet name="([^"]*)"`).FindAllStringSubmatch(read("xl/workbook.xml"), -1)
	out := map[string][]string{}
	for i, m := range names {
		name := strings.ReplaceAll(m[1], "&amp;", "&")
		out[name] = regexp.MustCompile(`(?s)<row .*?</row>`).FindAllString(read(fmt.Sprintf("xl/worksheets/sheet%d.xml", i+1)), -1)
	}
	return out
}

func rowWith(rows []string, subs ...string) string {
	for _, r := range rows {
		if all := func() bool {
			for _, s := range subs {
				if !strings.Contains(r, s) {
					return false
				}
			}
			return true
		}(); all {
			return r
		}
	}
	return ""
}

func TestPartTimeExport(t *testing.T) {
	h, session, _, _ := partTimeFixture(t)
	w := getWithCookies(h, "/part-time/export?month=2026-11", session)
	if w.Code != 200 {
		t.Fatalf("export = %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet" {
		t.Errorf("Content-Type = %q", ct)
	}
	if cd := w.Header().Get("Content-Disposition"); !strings.Contains(cd, `filename="Part-time November 2026.xlsx"`) {
		t.Errorf("Content-Disposition = %q", cd)
	}
	sheets := sheetRows(t, w.Body.Bytes())

	sum := rowWith(sheets["Summary"], ">PT1<")
	for _, want := range []string{"<v>14</v>", "<v>227500</v>", ">No<"} {
		if !strings.Contains(sum, want) {
			t.Errorf("Summary PT1 row lacks %s: %s", want, sum)
		}
	}

	daily := rowWith(sheets["Daily hours"], ">PT1<")
	for _, want := range []string{`<c r="C2"><v>6</v>`, `<c r="E2" t="inlineStr"><is><t xml:space="preserve">?</t>`} {
		if !strings.Contains(daily, want) {
			t.Errorf("Daily hours PT1 row lacks %s: %s", want, daily)
		}
	}

	detail := sheets["Check-in & out"]
	d26 := rowWith(detail, ">26 Oct<", ">PT1<")
	for _, want := range []string{">06:00<", ">15:00<", ">10:00–13:00<", ">3h00m<", ">6h00m<", "<v>6</v>", "<v>97500</v>"} {
		if !strings.Contains(d26, want) {
			t.Errorf("26 Oct row lacks %s: %s", want, d26)
		}
	}
	if r := rowWith(detail, ">30 Oct<", ">PT1<"); r != "" {
		t.Errorf("PT1 has a row for a day with no taps: %s", r)
	}
}

func TestPartTimeExportBadMonth(t *testing.T) {
	h, session, _, _ := partTimeFixture(t)
	if w := getWithCookies(h, "/part-time/export?month=bad", session); w.Code != 400 {
		t.Errorf("bad month = %d, want 400", w.Code)
	}
}

func TestPartTimeWorkbookWholeDayBreak(t *testing.T) {
	day := date("2026-11-02")
	p := partTimePage{Days: []time.Time{day}, Rows: []ptRow{{Emp: Employee{ID: "PT1", Name: "Putri"},
		Cells: []ptCell{{Active: true, Day: ptDay{Date: day, State: ptHours, In: at("2026-11-02 09:00:00"),
			Out: at("2026-11-02 10:00:00"), Break: time.Hour}}}}}}
	row := partTimeWorkbook(p)[2].Rows[1]
	if row[5] != "09:00–10:00" || row[8] != int64(0) {
		t.Errorf("row = %v, want Breaks 09:00–10:00 and Hours 0", row)
	}
}

func TestPartTimeWorkbookCheckOutBeforeCheckIn(t *testing.T) {
	day := date("2026-11-02")
	p := partTimePage{Days: []time.Time{day}, Rows: []ptRow{{Emp: Employee{ID: "PT1"}, Cells: []ptCell{{Active: true,
		Day: ptDay{Date: day, State: ptMissing, In: at("2026-11-02 17:00:00"), Out: at("2026-11-02 09:00:00")}}}}}}
	if note := partTimeWorkbook(p)[2].Rows[1][10]; note != "check-out before check-in" {
		t.Errorf("note = %q, want %q", note, "check-out before check-in")
	}
}

func TestPartTimePageExportLink(t *testing.T) {
	h, session, _, _ := partTimeFixture(t)
	// The body is hx-boost'ed: without hx-boost="false" htmx fetches the file and swaps its bytes into the page.
	wantAll(t, getPartTime(t, h, session, "?month=2026-11"),
		`<a class="btn" href="/part-time/export?month=2026-11" hx-boost="false" download>Export</a>`)
}

func TestPartTimeWorkbookLeadingAndTrailingBreak(t *testing.T) {
	day := date("2026-11-02")
	ses := func(a, b string) session { return session{at("2026-11-02 " + a), at("2026-11-02 " + b)} }
	cell := func(d ptDay) ptCell { d.Date, d.State = day, ptHours; return ptCell{Active: true, Day: d} }
	p := partTimePage{Days: []time.Time{day, day}, Rows: []ptRow{{Emp: Employee{ID: "PT1"}, Cells: []ptCell{
		cell(ptDay{In: at("2026-11-02 09:00:00"), Out: at("2026-11-02 17:00:00"), Break: time.Hour, Sessions: []session{ses("10:00:00", "17:00:00")}}),
		cell(ptDay{In: at("2026-11-02 09:00:00"), Out: at("2026-11-02 17:00:00"), Break: 2 * time.Hour,
			Sessions: []session{ses("09:00:00", "12:00:00"), ses("13:00:00", "16:00:00")}}),
	}}}}
	rows := partTimeWorkbook(p)[2].Rows
	if rows[1][5] != "09:00–10:00" || rows[2][5] != "12:00–13:00, 16:00–17:00" {
		t.Errorf("Breaks = %q, %q", rows[1][5], rows[2][5])
	}
}
