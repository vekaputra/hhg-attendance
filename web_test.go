package main

import (
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestDeviceWarnings(t *testing.T) {
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	contacts := map[string]time.Time{
		"a": now.Add(-30 * time.Second),
		"b": now.Add(-3 * time.Hour),
		"d": now.Add(-5*time.Minute - 50*time.Second),
		"e": now.Add(-contactWarnAfter),
	}
	got := deviceWarnings(contacts, []string{"a", "b", "c", "d", "e"}, now)
	want := []string{"b: no contact for 3h", "c: no contact yet", "d: no contact for 5m"}
	if !slices.Equal(got, want) {
		t.Errorf("deviceWarnings = %q, want %q", got, want)
	}
}

func TestTemplateFormats(t *testing.T) {
	for _, c := range []struct{ got, want string }{
		{hours(7 * time.Hour), "7h00m"},
		{hours(10*time.Hour + 20*time.Minute), "10h20m"},
		{mins(12 * time.Minute), "12m"},
		{mins(12*time.Minute + 40*time.Second), "13m"},
		{mins(40 * time.Second), "1m"},
		{clock(time.Date(2026, 10, 5, 14, 40, 0, 0, time.UTC)), "14:40"},
	} {
		if c.got != c.want {
			t.Errorf("got %q, want %q", c.got, c.want)
		}
	}
}

func wantAll(t *testing.T, page string, subs ...string) {
	t.Helper()
	for _, s := range subs {
		if !strings.Contains(page, s) {
			t.Errorf("page lacks %q", s)
		}
	}
}

func postForm(h http.Handler, path string, form url.Values, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", path, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, c := range cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// webFixture serves a seeded store to a logged-in manager, with the clock at 2026-10-12 12:00.
// Office: HHG0002 taps 08:42/17:30 on 10-05 (12m late), HHG0004 never taps and leaves after 10-31.
// Cafe: HHG0012 taps 14:40 and 01:00 on shift 3 on 10-04, 11:08 and 20:00 on shift 2 on 10-05 (8m late),
// and only 10:55 on shift 2 on 10-07 (no check-out).
func webFixture(t *testing.T) (h http.Handler, session *http.Cookie, office, cafe Org) {
	t.Helper()
	st := newReportStore(t)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	orgs, err := st.Orgs()
	must(err)
	office, cafe = orgs[0], orgs[1]
	shifts, err := st.Shifts(cafe.ID)
	must(err)
	now := at("2026-10-12 12:00:00")

	for _, e := range []Employee{
		{ID: "HHG0002", Name: "Budi", OrgID: office.ID},
		{ID: "HHG0004", Name: "Dewi", OrgID: office.ID, ActiveUntil: "2026-10-31"},
		{ID: "HHG0012", Name: "Gita", OrgID: cafe.ID},
	} {
		e.ActiveFrom = "2026-10-01"
		must(st.AddEmployee(e))
	}
	for day, shift := range map[string]Shift{"2026-10-04": shifts[2], "2026-10-05": shifts[1], "2026-10-07": shifts[1]} {
		must(st.SetRoster("HHG0012", date(day), shift.ID, "mgr", now))
	}
	for i, tap := range []Tap{
		{EmployeeID: "HHG0002", Time: at("2026-10-05 08:42:00")},
		{EmployeeID: "HHG0002", Time: at("2026-10-05 17:30:00")},
		{EmployeeID: "HHG0012", Time: at("2026-10-04 14:40:00")},
		{EmployeeID: "HHG0012", Time: at("2026-10-05 01:00:00")},
		{EmployeeID: "HHG0012", Time: at("2026-10-05 11:08:00")},
		{EmployeeID: "HHG0012", Time: at("2026-10-05 20:00:00")},
		{EmployeeID: "HHG0012", Time: at("2026-10-07 10:55:00")},
	} {
		tap.DeviceID, tap.SerialNo = "d", fmt.Sprint(i)
		must(st.InsertTap(tap, now))
	}

	must(st.SetUser("ana", "correct horse"))
	token, err := st.NewSession("ana", now)
	must(err)
	return newServer(st, testLoc, func() time.Time { return now }), &http.Cookie{Name: sessionCookie, Value: token}, office, cafe
}

func TestCalendarViews(t *testing.T) {
	saved := allowedDeviceIDs
	allowedDeviceIDs = []string{"gshhgfp"}
	t.Cleanup(func() { allowedDeviceIDs = saved })
	h, session, office, cafe := webFixture(t)
	get := func(query string) string {
		t.Helper()
		w := getWithCookies(h, "/calendar?"+query, session)
		if w.Code != 200 {
			t.Fatalf("GET /calendar?%s = %d, want 200", query, w.Code)
		}
		return w.Body.String()
	}

	month := get(fmt.Sprintf("org=%d&view=month&at=2026-10-05", office.ID))
	wantAll(t, month, "HHG0002", "12m late", "HHG0004", "absent",
		"gshhgfp: no contact yet", "ana", `action="/logout"`, "/static/htmx.min.js", `hx-boost="true"`,
		`"historyCacheSize":0`, `href="/person"`)
	// October 2026 starts on a Thursday and ends on a Saturday: 3 blanks before, 1 after.
	if n := strings.Count(month, `class="blank"`); n != 4 {
		t.Errorf("month grid has %d blank cells, want 4", n)
	}

	week := get(fmt.Sprintf("org=%d&view=week&at=2026-10-07", office.ID))
	wantAll(t, week, ">Mon 5<", ">Tue 6<", ">Wed 7<", ">Thu 8<", ">Fri 9<", ">Sat 10<", ">Sun 11<",
		"/person?emp=HHG0002&amp;month=2026-10", "8h48m · 8h48m")

	week = get(fmt.Sprintf("org=%d&view=week&at=2026-09-30", office.ID))
	wantAll(t, week, ">Mon 28<", ">Sun 4<")

	wantAll(t, get(fmt.Sprintf("org=%d&view=month&at=2026-10-05", cafe.ID)), "HHG0012", "· shift 2 · 8m late")
	wantAll(t, get(fmt.Sprintf("org=%d&view=week&at=2026-10-05", cafe.ID)), "HHG0012", "8m late")

	if w := getWithCookies(h, "/calendar"); w.Code != 303 {
		t.Errorf("GET /calendar without a session = %d, want 303", w.Code)
	}
	if w := getWithCookies(h, "/", session); w.Code != 303 || w.Header().Get("Location") != "/calendar" {
		t.Errorf("GET / = %d to %q, want 303 to /calendar", w.Code, w.Header().Get("Location"))
	}
	if body := get(""); !strings.Contains(body, "October 2026") || !strings.Contains(body, "Office") {
		t.Error("defaults should be the first org's month containing today")
	}
}

func TestCalendarNavigation(t *testing.T) {
	h, session, office, _ := webFixture(t)
	for query, want := range map[string][]string{
		// A month link from the 31st must not overflow into the month after.
		"view=month&at=2026-01-31": {"at=2025-12-01", "at=2026-02-01"},
		"view=week&at=2026-10-07":  {"at=2026-09-28", "at=2026-10-12"},
	} {
		body := getWithCookies(h, fmt.Sprintf("/calendar?org=%d&%s", office.ID, query), session).Body.String()
		for _, s := range want {
			if !strings.Contains(body, s) {
				t.Errorf("%s lacks link %q", query, s)
			}
		}
		// Today (the clock is 2026-10-12) keeps the view and organisation.
		if !regexp.MustCompile(`href="[^"]*at=2026-10-12[^"]*">Today<`).MatchString(body) {
			t.Errorf("%s lacks a Today link to at=2026-10-12", query)
		}
	}
}

func TestCalendarRejectsBadQuery(t *testing.T) {
	h, session, _, _ := webFixture(t)
	for _, query := range []string{"org=99", "org=x", "view=year", "at=2026-13-45", "at=2026-10-5"} {
		if w := getWithCookies(h, "/calendar?"+query, session); w.Code != 400 {
			t.Errorf("GET /calendar?%s = %d, want 400", query, w.Code)
		}
	}
}

func TestPublicRoutes(t *testing.T) {
	h, _, _, _ := webFixture(t)
	for path, want := range map[string]string{"/login": `name="password"`, "/static/style.css": "@media"} {
		if w := getWithCookies(h, path); w.Code != 200 || !strings.Contains(w.Body.String(), want) {
			t.Errorf("GET %s without a session = %d, want 200 containing %q", path, w.Code, want)
		}
	}
}

// style.css is the app's only stylesheet: mvp.css is gone and no page links it.
func TestStylesheet(t *testing.T) {
	h, session, _, _ := webFixture(t)
	if w := getWithCookies(h, "/static/mvp.css"); w.Code != 404 {
		t.Errorf("GET /static/mvp.css = %d, want 404", w.Code)
	}
	for name, page := range map[string]string{
		"login":    getWithCookies(h, "/login").Body.String(),
		"calendar": getWithCookies(h, "/calendar", session).Body.String(),
	} {
		wantAll(t, page, `href="/static/style.css"`)
		if strings.Contains(page, "mvp.css") {
			t.Errorf("%s page still links mvp.css", name)
		}
	}
}

var navCurrentRE = regexp.MustCompile(`<a href="([^"]+)"[^>]* aria-current="page"`)

// Every page marks its own sidebar link as the current page, and no other link, including after a rejected POST.
func TestActiveNav(t *testing.T) {
	h, session, _, _ := webFixture(t)
	check := func(from string, w *httptest.ResponseRecorder, want string) {
		t.Helper()
		got := navCurrentRE.FindAllStringSubmatch(w.Body.String(), -1)
		if len(got) != 1 || got[0][1] != want {
			t.Errorf("%s marks %q as the current page, want only %q", from, got, want)
		}
	}
	for _, path := range []string{"/calendar", "/person", "/roster", "/employees", "/orgs", "/leave", "/holidays"} {
		check("GET "+path, getWithCookies(h, path, session), path)
	}
	check("rejected POST /corrections", postCorrection(h, session, "NOPE", "2026-10-07", "", "20:00"), "/person")
}

var summaryRE = regexp.MustCompile(`<dt>([^<]+)</dt>\s*<dd class="value">(\d+)</dd>`)

// The cards count the days shown, over everyone in the organisation. The clock is Monday 2026-10-12, so the week
// of 10-05 is over: Budi is late on Monday and absent Tuesday to Friday, Dewi is absent all five days, and Gita is
// late on 10-05 and on time on 10-07 but missing her check-out. The month also holds Gita's on-time shift on 10-04.
func TestCalendarSummary(t *testing.T) {
	h, session, office, cafe := webFixture(t)
	for _, c := range []struct {
		name  string
		org   Org
		query string
		want  map[string]int
	}{
		{"office week", office, "view=week&at=2026-10-07", map[string]int{"On time": 0, "Late": 1, "Absent": 9, "Missing tap": 0}},
		{"cafe week", cafe, "view=week&at=2026-10-07", map[string]int{"On time": 1, "Late": 1, "Absent": 0, "Missing tap": 1}},
		{"cafe month", cafe, "view=month&at=2026-10-05", map[string]int{"On time": 2, "Late": 1, "Absent": 0, "Missing tap": 1}},
	} {
		page := getWithCookies(h, fmt.Sprintf("/calendar?org=%d&%s", c.org.ID, c.query), session).Body.String()
		got := map[string]int{}
		for _, m := range summaryRE.FindAllStringSubmatch(page, -1) {
			got[m[1]], _ = strconv.Atoi(m[2])
		}
		if !maps.Equal(got, c.want) {
			t.Errorf("%s: summary cards = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestCrossOriginPostRejected(t *testing.T) {
	h, session, _, _ := webFixture(t)
	for site, want := range map[string]int{"cross-site": 403, "same-origin": 303} {
		r := httptest.NewRequest("POST", "/logout", nil)
		r.Header.Set("Sec-Fetch-Site", site)
		r.AddCookie(session)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Errorf("POST /logout with Sec-Fetch-Site %s = %d, want %d", site, w.Code, want)
		}
	}
}

// bareServer serves a store with no employees to a logged-in manager, with the clock at now.
func bareServer(t *testing.T, now time.Time) (http.Handler, *http.Cookie) {
	t.Helper()
	st := newReportStore(t)
	if err := st.SetUser("ana", "correct horse"); err != nil {
		t.Fatal(err)
	}
	token, err := st.NewSession("ana", now)
	if err != nil {
		t.Fatal(err)
	}
	return newServer(st, testLoc, func() time.Time { return now }), &http.Cookie{Name: sessionCookie, Value: token}
}

func TestCalendarDefaultsToAttendanceDay(t *testing.T) {
	// 01:30 on 1 Nov is still 31 Oct's attendance day.
	h, session := bareServer(t, at("2026-11-01 01:30:00"))
	if body := getWithCookies(h, "/calendar", session).Body.String(); !strings.Contains(body, "October 2026") {
		t.Error("calendar before 03:00 should show the previous day's month")
	}
}

func TestUnauthenticatedRedirects(t *testing.T) {
	h, _, _, _ := webFixture(t)
	for _, c := range []struct{ method, path string }{{"POST", "/logout"}, {"GET", "/nope"}, {"GET", "/person"}, {"POST", "/corrections"}} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(c.method, c.path, nil))
		if w.Code != 303 || w.Header().Get("Location") != "/login" {
			t.Errorf("%s %s without a session = %d to %q, want 303 to /login", c.method, c.path, w.Code, w.Header().Get("Location"))
		}
	}
}

func TestPersonPageAndCorrection(t *testing.T) {
	h, session, _, _ := webFixture(t)
	const personURL = "/person?emp=HHG0012&month=2026-10"
	get := func() string {
		t.Helper()
		w := getWithCookies(h, personURL, session)
		if w.Code != 200 {
			t.Fatalf("GET %s = %d, want 200", personURL, w.Code)
		}
		if cc := w.Header().Get("Cache-Control"); cc != "no-store" {
			t.Errorf("Cache-Control = %q, want no-store", cc)
		}
		return w.Body.String()
	}

	page := get()
	// 10-04: 14:40 to 01:00 is 10h20m, 7h00m of it in shift 3. 10-05: 11:08 to 20:00 is 8h52m, all in shift 2.
	wantAll(t, page, "10h20m", "7h00m", "no check-out", "Missing tap", "19h12m", "15h52m",
		"month=2026-09", "month=2026-11", `value="HHG0002"`, `action="/corrections"`)

	form := url.Values{"emp": {"HHG0012"}, "day": {"2026-10-07"}, "out": {"20:00"}}
	if w := postForm(h, "/corrections", form, session); w.Code != 303 || w.Header().Get("Location") != personURL {
		t.Fatalf("POST /corrections = %d to %q, want 303 to %s", w.Code, w.Header().Get("Location"), personURL)
	}
	page = get()
	wantAll(t, page, "manager edit", "9h05m")
	if strings.Contains(page, "no check-out") {
		t.Error("corrected day still shows no check-out")
	}

	form.Set("emp", "NOPE")
	if w := postForm(h, "/corrections", form, session); w.Code != 400 {
		t.Errorf("POST /corrections for an unknown employee = %d, want 400", w.Code)
	}
}

func TestPersonQuery(t *testing.T) {
	h, session, _, _ := webFixture(t)
	// The first employee and the clock's month by default.
	w := getWithCookies(h, "/person", session)
	wantAll(t, w.Body.String(), `value="HHG0002" selected`, "October 2026")

	for _, query := range []string{"emp=NOPE", "emp=HHG0002&month=2026-13", "month=2026-1"} {
		if w := getWithCookies(h, "/person?"+query, session); w.Code != 400 {
			t.Errorf("GET /person?%s = %d, want 400", query, w.Code)
		}
	}

	h, session = bareServer(t, at("2026-10-12 12:00:00"))
	if w := getWithCookies(h, "/person", session); w.Code != 200 || !strings.Contains(w.Body.String(), "No employees yet") {
		t.Errorf("GET /person with no employees = %d, want 200 with a note", w.Code)
	}
}

func postCorrection(h http.Handler, session *http.Cookie, emp, day, in, out string) *httptest.ResponseRecorder {
	return postForm(h, "/corrections", url.Values{"emp": {emp}, "day": {day}, "in": {in}, "out": {out}}, session)
}

func getPerson(h http.Handler, session *http.Cookie) string {
	return getWithCookies(h, "/person?emp=HHG0012&month=2026-10", session).Body.String()
}

func TestCorrectionInput(t *testing.T) {
	h, session, _, _ := webFixture(t)

	// Rejections re-render the person page with the message. 10-07 has only a 10:55 tap, so a check-out
	// must come after it; 10-04 is 14:40 to 01:00 the next day; HHG0004 is active 10-01 to 10-31.
	for _, c := range []struct{ name, emp, day, in, out, msg string }{
		{"unknown employee", "NOPE", "2026-10-07", "", "20:00", "unknown employee"},
		{"bad day", "HHG0012", "2026-13-07", "", "20:00", "bad day"},
		{"bad check-in", "HHG0012", "2026-10-07", "9am", "", "bad check-in"},
		{"bad check-out", "HHG0012", "2026-10-07", "", "24:00", "bad check-out"},
		{"out before tap in", "HHG0012", "2026-10-07", "", "10:00", "check-out must be after check-in"},
		{"out before given in", "HHG0012", "2026-10-07", "21:00", "20:00", "check-out must be after check-in"},
		{"in after tap out", "HHG0012", "2026-10-04", "02:00", "", "check-out must be after check-in"}, // 02:00 is 10-05
		{"before active from", "HHG0012", "2026-09-30", "", "20:00", "not active"},
		{"after active until", "HHG0004", "2026-11-01", "09:00", "17:00", "not active"},
	} {
		w := postCorrection(h, session, c.emp, c.day, c.in, c.out)
		if w.Code != 400 {
			t.Errorf("%s: POST /corrections = %d, want 400", c.name, w.Code)
		}
		wantAll(t, w.Body.String(), `action="/logout"`, `role="alert"`, c.msg)
	}
	if strings.Contains(getPerson(h, session), "manager edit") {
		t.Error("a rejected correction was saved")
	}
	if w := postCorrection(h, session, "HHG0004", "2026-10-31", "09:00", "17:00"); w.Code != 303 {
		t.Errorf("correction on the last active day = %d, want 303", w.Code)
	}

	// Before 03:00 is the next calendar day: 10:55 to 01:00 is 14h05m.
	if w := postCorrection(h, session, "HHG0012", "2026-10-07", "", "01:00"); w.Code != 303 || !strings.Contains(getPerson(h, session), "14h05m") {
		t.Errorf("check-out 01:00 = %d, want 303 and 14h05m on the page", w.Code)
	}
	// Both empty removes the correction.
	if w := postCorrection(h, session, "HHG0012", "2026-10-07", "", ""); w.Code != 303 || strings.Contains(getPerson(h, session), "manager edit") {
		t.Errorf("empty correction = %d, want 303 and no manager edit", w.Code)
	}
}

// A field left empty keeps the day's current value, so editing one time never undoes the other.
func TestCorrectionKeepsOtherField(t *testing.T) {
	h, session, _, _ := webFixture(t)
	post := func(in, out string) int { return postCorrection(h, session, "HHG0012", "2026-10-07", in, out).Code }

	// 10-07 has only a 10:55 tap.
	if code := post("09:00", ""); code != 303 {
		t.Fatalf("check-in 09:00 = %d, want 303", code)
	}
	if code := post("", "10:00"); code != 303 {
		t.Fatalf("check-out 10:00 = %d, want 303", code)
	}
	wantAll(t, getPerson(h, session), "09:00 → 10:00", "1h00m")

	if code := post("", "08:30"); code != 400 {
		t.Errorf("check-out 08:30 before the corrected check-in = %d, want 400", code)
	}
	wantAll(t, getPerson(h, session), "09:00 → 10:00")
}
