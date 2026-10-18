package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
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
		{mins(12*time.Minute + 40*time.Second), "12m"},
		{clock(time.Date(2026, 10, 5, 14, 40, 0, 0, time.UTC)), "14:40"},
	} {
		if c.got != c.want {
			t.Errorf("got %q, want %q", c.got, c.want)
		}
	}
}

// webFixture serves a seeded store to a logged-in manager, with the clock at 2026-10-12 12:00.
// Office: HHG0002 taps 08:42/17:30 on 10-05 (12m late), HHG0004 never taps.
// Cafe: HHG0012 is rostered on shift 2 (11:00-20:00) on 10-05 and taps 11:08.
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
		{ID: "HHG0004", Name: "Dewi", OrgID: office.ID},
		{ID: "HHG0012", Name: "Gita", OrgID: cafe.ID},
	} {
		e.ActiveFrom = "2026-10-01"
		must(st.AddEmployee(e))
	}
	must(st.SetRoster("HHG0012", date("2026-10-05"), shifts[1].ID, "mgr", now))
	for i, tap := range []Tap{
		{EmployeeID: "HHG0002", Time: at("2026-10-05 08:42:00")},
		{EmployeeID: "HHG0002", Time: at("2026-10-05 17:30:00")},
		{EmployeeID: "HHG0012", Time: at("2026-10-05 11:08:00")},
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
	wantAll := func(page string, subs ...string) {
		t.Helper()
		for _, s := range subs {
			if !strings.Contains(page, s) {
				t.Errorf("page lacks %q", s)
			}
		}
	}

	month := get(fmt.Sprintf("org=%d&view=month&at=2026-10-05", office.ID))
	wantAll(month, "HHG0002", "12m late", "HHG0004", "absent",
		"gshhgfp: no contact yet", "ana", `action="/logout"`, "/static/htmx.min.js", `hx-boost="true"`)
	// October 2026 starts on a Thursday and ends on a Saturday: 3 blanks before, 1 after.
	if n := strings.Count(month, `class="blank"`); n != 4 {
		t.Errorf("month grid has %d blank cells, want 4", n)
	}

	week := get(fmt.Sprintf("org=%d&view=week&at=2026-10-07", office.ID))
	wantAll(week, ">Mon 5<", ">Tue 6<", ">Wed 7<", ">Thu 8<", ">Fri 9<", ">Sat 10<", ">Sun 11<",
		"/person?emp=HHG0002&amp;month=2026-10", "8h48m / 8h48m")

	week = get(fmt.Sprintf("org=%d&view=week&at=2026-09-30", office.ID))
	wantAll(week, ">Mon 28<", ">Sun 4<")

	wantAll(get(fmt.Sprintf("org=%d&view=month&at=2026-10-05", cafe.ID)), "HHG0012", "· shift 2 · 8m late")
	wantAll(get(fmt.Sprintf("org=%d&view=week&at=2026-10-05", cafe.ID)), "HHG0012", "8m late")

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
