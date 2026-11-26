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
