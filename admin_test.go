package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// mustGet returns v, panicking on a store error: these reads only check what a handler stored.
func mustGet[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

// formOf builds a form from key, value pairs; a key may repeat.
func formOf(kv ...string) url.Values {
	f := url.Values{}
	for i := 0; i < len(kv); i += 2 {
		f.Add(kv[i], kv[i+1])
	}
	return f
}

// adminFixture serves a store to a logged-in manager, with the clock at 2026-10-18 12:00.
// Office has HHG0002 (active) and HHG0004 (deactivated 2026-10-10); Cafe has no employees.
func adminFixture(t *testing.T) (h http.Handler, session *http.Cookie, st *Store, office, cafe Org) {
	t.Helper()
	st = newReportStore(t)
	now := at("2026-10-18 12:00:00")
	orgs := mustGet(st.Orgs())
	office, cafe = orgs[0], orgs[1]
	for _, e := range []Employee{
		{ID: "HHG0002", Name: "Budi", OrgID: office.ID, ActiveFrom: "2026-10-01"},
		{ID: "HHG0004", Name: "Dewi", OrgID: office.ID, ActiveFrom: "2026-10-01", ActiveUntil: "2026-10-10"},
	} {
		if err := st.AddEmployee(e); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.SetUser("ana", "correct horse"); err != nil {
		t.Fatal(err)
	}
	token := mustGet(st.NewSession("ana", now))
	return newServer(st, testLoc, func() time.Time { return now }), &http.Cookie{Name: sessionCookie, Value: token}, st, office, cafe
}

func TestAdminPages(t *testing.T) {
	h, session, st, office, cafe := adminFixture(t)
	post := func(path string, f url.Values) *httptest.ResponseRecorder { return postForm(h, path, f, session) }
	redirects := func(w *httptest.ResponseRecorder, loc string) {
		t.Helper()
		if w.Code != 303 || w.Header().Get("Location") != loc {
			t.Fatalf("got %d to %q, want 303 to %q: %s", w.Code, w.Header().Get("Location"), loc, w.Body)
		}
	}
	get := func(path string) string {
		t.Helper()
		w := getWithCookies(h, path, session)
		if w.Code != 200 {
			t.Fatalf("GET %s = %d, want 200", path, w.Code)
		}
		return w.Body.String()
	}
	from, to := date("2000-01-01"), date("2100-01-01")
	officeID, cafeID := strconv.FormatInt(office.ID, 10), strconv.FormatInt(cafe.ID, 10)

	// Employees: added active from today's business date, then deactivated as of today.
	rina := formOf("id", "HHG0020", "name", "Rina", "org", cafeID)
	redirects(post("/employees", rina), "/employees")
	want := Employee{ID: "HHG0020", Name: "Rina", OrgID: cafe.ID, ActiveFrom: "2026-10-18"}
	if got := mustGet(st.Employees(cafe.ID)); !slices.Equal(got, []Employee{want}) {
		t.Fatalf("Employees(cafe) = %v, want [%v]", got, want)
	}
	page := get("/employees")
	wantAll(t, page, "HHG0020", "Rina", "Cafe", "2026-10-18", `action="/employees/HHG0020/deactivate"`, `action="/employees/HHG0020"`,
		`hx-confirm="Deactivate HHG0020 Rina? Their past days stay in the reports."`,
		"2026-10-10", `href="/employees"`, `href="/orgs"`, `href="/leave"`, `href="/holidays"`)
	if strings.Contains(page, "/employees/HHG0004/deactivate") {
		t.Error("a deactivated employee still has a deactivate button")
	}
	if !strings.Contains(page, `action="/employees/HHG0004/reactivate"`) || strings.Contains(page, "/employees/HHG0002/reactivate") {
		t.Error("only a deactivated employee should have a reactivate button")
	}
	redirects(post("/employees/HHG0020/deactivate", nil), "/employees")
	want.ActiveUntil = "2026-10-18"
	if got := mustGet(st.Employees(cafe.ID)); !slices.Equal(got, []Employee{want}) {
		t.Errorf("after deactivating, Employees(cafe) = %v, want [%v]", got, want)
	}

	// Organisations: an update keeps the org's name; weekdays are stored ascending.
	officeForm := formOf("id", officeID, "name", "Office", "kind", "fixed", "grace", "10", "start", "08:30", "end", "17:30")
	officeForm["weekdays"] = []string{"5", "1", "3"}
	redirects(post("/orgs", officeForm), "/orgs")
	wantOffice := Org{ID: office.ID, Name: "Office", Kind: "fixed", GraceMinutes: 10, Start: "08:30", End: "17:30", Weekdays: "135"}
	if got := mustGet(st.Orgs())[0]; got != wantOffice {
		t.Errorf("Orgs()[0] = %+v, want %+v", got, wantOffice)
	}
	orgsPage := get("/orgs")
	wantAll(t, orgsPage, "Office", "Cafe", `action="/orgs/`+cafeID+`/shifts"`,
		`name="weekdays" value="3" checked`, `name="weekdays" value="2">`)
	if n := strings.Count(orgsPage, `<select name="kind">`); n != 1 {
		t.Errorf("/orgs has %d kind selects, want 1 (only the new organisation's)", n)
	}

	// A new roster org keeps no times or weekdays.
	redirects(post("/orgs", formOf("id", "", "name", "Kitchen", "kind", "roster", "grace", "5", "start", "08:00", "end", "17:00", "weekdays", "1")), "/orgs")
	kitchen := mustGet(st.Orgs())[2]
	if wantKitchen := (Org{ID: kitchen.ID, Name: "Kitchen", Kind: "roster", GraceMinutes: 5}); kitchen != wantKitchen {
		t.Errorf("new roster org = %+v, want %+v", kitchen, wantKitchen)
	}

	// Shifts: add one, then edit it; the edit keeps the row and stores 07:30, not 7:30.
	shiftsPath := "/orgs/" + cafeID + "/shifts"
	redirects(post(shiftsPath, formOf("shift_id", "", "name", "4", "start", "08:00", "end", "14:00")), "/orgs")
	shifts := mustGet(st.Shifts(cafe.ID))
	if len(shifts) != 4 {
		t.Fatalf("Shifts(cafe) has %d shifts, want 4", len(shifts))
	}
	four := shifts[slices.IndexFunc(shifts, func(s Shift) bool { return s.Name == "4" })]
	redirects(post(shiftsPath, formOf("shift_id", strconv.FormatInt(four.ID, 10), "name", "4", "start", "7:30", "end", "14:00")), "/orgs")
	shifts = mustGet(st.Shifts(cafe.ID))
	if i := slices.IndexFunc(shifts, func(s Shift) bool { return s.ID == four.ID }); len(shifts) != 4 || shifts[i].Start != "07:30" {
		t.Errorf("after editing shift 4, Shifts(cafe) = %+v, want 4 shifts with 4 starting 07:30", shifts)
	}

	// Leave: add, listed with a delete button, delete twice.
	redirects(post("/leave", formOf("emp", "HHG0002", "from", "2026-10-20", "to", "2026-10-22")), "/leave")
	leaves := mustGet(st.Leaves(from, to))
	if len(leaves) != 1 || leaves[0] != (Leave{ID: leaves[0].ID, EmployeeID: "HHG0002", From: "2026-10-20", To: "2026-10-22"}) {
		t.Fatalf("Leaves = %+v, want HHG0002 2026-10-20 to 2026-10-22", leaves)
	}
	deleteLeave := fmt.Sprintf("/leave/%d/delete", leaves[0].ID)
	wantAll(t, get("/leave"), "HHG0002", "Budi", "2026-10-20", "2026-10-22", `action="`+deleteLeave+`"`)
	redirects(post(deleteLeave, nil), "/leave")
	redirects(post(deleteLeave, nil), "/leave")
	if got := mustGet(st.Leaves(from, to)); len(got) != 0 {
		t.Errorf("Leaves after delete = %+v, want none", got)
	}

	// A leave that starts before the list window but reaches into it is still listed.
	redirects(post("/leave", formOf("emp", "HHG0002", "from", "2026-06-01", "to", "2026-12-31")), "/leave")
	wantAll(t, get("/leave"), "2026-06-01", "2026-12-31")

	// Holidays: add, listed with a delete button, delete twice.
	redirects(post("/holidays", formOf("day", "2026-12-25")), "/holidays")
	wantAll(t, get("/holidays"), "2026-12-25", `action="/holidays/2026-12-25/delete"`)
	redirects(post("/holidays/2026-12-25/delete", nil), "/holidays")
	redirects(post("/holidays/2026-12-25/delete", nil), "/holidays")
	if got := mustGet(st.Holidays(from, to)); len(got) != 0 {
		t.Errorf("Holidays after delete = %v, want none", got)
	}

	for _, path := range []string{"/employees", "/orgs", "/leave", "/holidays"} {
		if w := getWithCookies(h, path); w.Code != 303 || w.Header().Get("Location") != "/login" {
			t.Errorf("GET %s without a session = %d to %q, want 303 to /login", path, w.Code, w.Header().Get("Location"))
		}
	}
}

// A window may not start before the attendance day does, nor run overnight past its end, or its taps split across two days.
func TestTimeWindow(t *testing.T) {
	for _, c := range []struct {
		start, end string
		ok         bool
	}{
		{"08:30", "17:30", true},
		{"03:00", "11:00", true},
		{"15:00", "01:00", true},
		{"15:00", "03:00", true},
		{"22:00", "06:00", false},
		{"22:00", "03:01", false},
		{"02:00", "10:00", false},
		{"00:00", "08:00", false},
	} {
		if _, _, err := timeWindow(c.start, c.end); (err == nil) != c.ok {
			t.Errorf("timeWindow(%s, %s) error = %v, want ok = %v", c.start, c.end, err, c.ok)
		}
	}
}

func TestEditEmployee(t *testing.T) {
	h, session, st, office, cafe := adminFixture(t)
	post := func(path string, f url.Values) *httptest.ResponseRecorder { return postForm(h, path, f, session) }

	// Moving a part-timer to Cafe keeps their ID and active period and trims the name.
	w := post("/employees/HHG0002", formOf("name", "  Budi S  ", "org", strconv.FormatInt(cafe.ID, 10)))
	if w.Code != 303 || w.Header().Get("Location") != "/employees" {
		t.Fatalf("edit = %d to %q, want 303 to /employees", w.Code, w.Header().Get("Location"))
	}
	want := Employee{ID: "HHG0002", Name: "Budi S", OrgID: cafe.ID, ActiveFrom: "2026-10-01"}
	if got := mustGet(st.Employees(cafe.ID)); !slices.Equal(got, []Employee{want}) {
		t.Errorf("Employees(cafe) = %v, want [%v]", got, want)
	}
	if got := mustGet(st.Employees(office.ID)); len(got) != 1 || got[0].ID != "HHG0004" {
		t.Errorf("Employees(office) = %v, want only HHG0004", got)
	}

	// Reactivating clears the last active day, and only that.
	if w := post("/employees/HHG0004/reactivate", nil); w.Code != 303 || w.Header().Get("Location") != "/employees" {
		t.Fatalf("reactivate = %d to %q, want 303 to /employees", w.Code, w.Header().Get("Location"))
	}
	want = Employee{ID: "HHG0004", Name: "Dewi", OrgID: office.ID, ActiveFrom: "2026-10-01"}
	if got := mustGet(st.Employees(office.ID)); !slices.Equal(got, []Employee{want}) {
		t.Errorf("Employees(office) = %v, want [%v]", got, want)
	}
}

// adminState is everything the admin pages can change, as text.
func adminState(st *Store) string {
	from, to := date("2000-01-01"), date("2100-01-01")
	orgs := mustGet(st.Orgs())
	var shifts [][]Shift
	for _, o := range orgs {
		shifts = append(shifts, mustGet(st.Shifts(o.ID)))
	}
	return fmt.Sprint(orgs, shifts, mustGet(st.Employees(0)), mustGet(st.Leaves(from, to)), mustGet(st.Holidays(from, to)))
}

// Every rejection re-renders the page with the message and saves nothing.
func TestAdminRejectsBadInput(t *testing.T) {
	h, session, st, office, cafe := adminFixture(t)
	kitchen := mustGet(st.SaveOrg(Org{Name: "Kitchen", Kind: "roster"}))
	if err := st.AddHoliday(date("2026-12-25")); err != nil {
		t.Fatal(err)
	}
	cafeShift := strconv.FormatInt(mustGet(st.Shifts(cafe.ID))[0].ID, 10)
	officeID, cafeID, kitchenID := strconv.FormatInt(office.ID, 10), strconv.FormatInt(cafe.ID, 10), strconv.FormatInt(kitchen, 10)
	before := adminState(st)

	// fixed is a valid Office form with the given fields replaced.
	fixed := func(kv ...string) url.Values {
		f := formOf("id", officeID, "name", "Office", "kind", "fixed", "grace", "0", "start", "08:30", "end", "17:30", "weekdays", "1")
		for i := 0; i < len(kv); i += 2 {
			f.Set(kv[i], kv[i+1])
		}
		return f
	}
	const nameMsg = "name must be 1 to 100 characters"
	long := strings.Repeat("x", 101)
	for _, c := range []struct {
		path string
		form url.Values
		msg  string
	}{
		{"/employees", formOf("id", "bad id!", "name", "Rina", "org", cafeID), "bad employee ID"},
		{"/employees", formOf("id", strings.Repeat("A", 33), "name", "Rina", "org", cafeID), "bad employee ID"},
		{"/employees", formOf("id", "HHG0020", "name", "  ", "org", cafeID), nameMsg},
		{"/employees", formOf("id", "HHG0020", "name", long, "org", cafeID), nameMsg},
		{"/employees", formOf("id", "HHG0020", "name", "Rina", "org", "999"), "unknown organisation"},
		{"/employees", formOf("id", "HHG0020", "name", "Rina", "org", "x"), "unknown organisation"},
		{"/employees", formOf("id", "HHG0002", "name", "Rina", "org", cafeID), "already exists"},
		{"/employees", formOf("id", "HHG0020", "name", "Rina", "org", cafeID, "pad", strings.Repeat("x", maxForm)), "bad employee ID"}, // body over the cap is not parsed
		{"/employees/NOPE/deactivate", nil, "unknown employee"},
		{"/employees/HHG0004/deactivate", nil, "already deactivated"},
		{"/employees/NOPE", formOf("name", "Rina", "org", cafeID), "unknown employee"},
		{"/employees/HHG0002", formOf("name", "  ", "org", cafeID), nameMsg},
		{"/employees/HHG0002", formOf("name", long, "org", cafeID), nameMsg},
		{"/employees/HHG0002", formOf("name", "Rina", "org", "999"), "unknown organisation"},
		{"/employees/HHG0002", formOf("name", "Rina", "org", "x"), "unknown organisation"},
		{"/employees/NOPE/reactivate", nil, "unknown employee"},
		{"/employees/HHG0002/reactivate", nil, "not deactivated"},

		{"/orgs", fixed("id", "999"), "unknown organisation"},
		{"/orgs", fixed("name", " "), nameMsg},
		{"/orgs", fixed("name", "Cafe"), "already exists"},
		{"/orgs", fixed("id", "", "name", "Office"), "already exists"},
		{"/orgs", fixed("kind", "daily"), "kind must be fixed or roster"},
		{"/orgs", fixed("kind", "roster"), "kind can&#39;t be changed"},
		{"/orgs", fixed("grace", "abc"), "grace must be 0 to 240"},
		{"/orgs", fixed("grace", "-1"), "grace must be 0 to 240"},
		{"/orgs", fixed("grace", "241"), "grace must be 0 to 240"},
		{"/orgs", fixed("start", "25:00"), "bad start time"},
		{"/orgs", fixed("end", "5pm"), "bad end time"},
		{"/orgs", fixed("end", "08:30"), "start and end must differ"},
		{"/orgs", fixed("start", "22:00", "end", "06:00"), "must end by 03:00"},
		{"/orgs", fixed("start", "02:00", "end", "10:00"), "start must be 03:00 or later"},
		{"/orgs", fixed("weekdays", ""), "tick at least one weekday"},

		{"/orgs/" + officeID + "/shifts", formOf("name", "5", "start", "08:00", "end", "14:00"), "only roster organisations"},
		{"/orgs/999/shifts", formOf("name", "5", "start", "08:00", "end", "14:00"), "unknown organisation"},
		{"/orgs/" + cafeID + "/shifts", formOf("name", "", "start", "08:00", "end", "14:00"), nameMsg},
		{"/orgs/" + cafeID + "/shifts", formOf("name", "5", "start", "8am", "end", "14:00"), "bad start time"},
		{"/orgs/" + cafeID + "/shifts", formOf("name", "5", "start", "08:00", "end", "08:00"), "start and end must differ"},
		{"/orgs/" + cafeID + "/shifts", formOf("name", "5", "start", "22:00", "end", "06:00"), "must end by 03:00"},
		{"/orgs/" + cafeID + "/shifts", formOf("name", "5", "start", "02:00", "end", "10:00"), "start must be 03:00 or later"},
		{"/orgs/" + cafeID + "/shifts", formOf("shift_id", "999", "name", "5", "start", "08:00", "end", "14:00"), "unknown shift"},
		{"/orgs/" + cafeID + "/shifts", formOf("shift_id", "x", "name", "5", "start", "08:00", "end", "14:00"), "unknown shift"},
		{"/orgs/" + kitchenID + "/shifts", formOf("shift_id", cafeShift, "name", "5", "start", "08:00", "end", "14:00"), "unknown shift"}, // Cafe's shift

		{"/leave", formOf("emp", "NOPE", "from", "2026-10-20", "to", "2026-10-22"), "unknown employee"},
		{"/leave", formOf("emp", "HHG0002", "from", "2026-10-2", "to", "2026-10-22"), "bad from date"},
		{"/leave", formOf("emp", "HHG0002", "from", "2026-10-20", "to", ""), "bad to date"},
		{"/leave", formOf("emp", "HHG0002", "from", "2026-10-22", "to", "2026-10-20"), "cannot end before it starts"},
		{"/leave", formOf("emp", "HHG0002", "from", "2026-01-01", "to", "2026-01-05"), "between 2026-07-20 and 2027-10-18"}, // before the list window
		{"/leave", formOf("emp", "HHG0002", "from", "2027-11-01", "to", "2027-11-05"), "between 2026-07-20 and 2027-10-18"}, // after it

		{"/holidays", formOf("day", "25/12/2026"), "bad day"},
		{"/holidays", formOf("day", "2026-12-25"), "already a holiday"},
		{"/holidays", formOf("day", "2024-12-31"), "between 2025-01-01 and 2027-12-31"},
		{"/holidays", formOf("day", "2028-01-01"), "between 2025-01-01 and 2027-12-31"},
	} {
		w := postForm(h, c.path, c.form, session)
		if w.Code != 400 {
			t.Errorf("POST %s %v = %d, want 400", c.path, c.form, w.Code)
		}
		wantAll(t, w.Body.String(), `action="/logout"`, `role="alert"`, c.msg)
	}
	if after := adminState(st); after != before {
		t.Errorf("rejected input changed the data:\nbefore %s\nafter  %s", before, after)
	}
}

// rosterFixture adds three Cafe employees to adminFixture: HHG0012 (active all month), HHG0013 (active from
// Wednesday 2026-10-21) and HHG0014 (left on 2026-10-10). The clock is Sunday 2026-10-18 12:00.
func rosterFixture(t *testing.T) (h http.Handler, session *http.Cookie, st *Store, office, cafe Org) {
	t.Helper()
	h, session, st, office, cafe = adminFixture(t)
	for _, e := range []Employee{
		{ID: "HHG0012", Name: "Sari", OrgID: cafe.ID, ActiveFrom: "2026-10-01"},
		{ID: "HHG0013", Name: "Tono", OrgID: cafe.ID, ActiveFrom: "2026-10-21"},
		{ID: "HHG0014", Name: "Umar", OrgID: cafe.ID, ActiveFrom: "2026-10-01", ActiveUntil: "2026-10-10"},
	} {
		if err := st.AddEmployee(e); err != nil {
			t.Fatal(err)
		}
	}
	return
}

func TestRosterPage(t *testing.T) {
	h, session, st, office, cafe := rosterFixture(t)
	id := func(n int64) string { return strconv.FormatInt(n, 10) }
	shift2 := mustGet(st.Shifts(cafe.ID))[1] // ordered by start: 1, 2, 3
	mon := date("2026-10-19")
	stored := func() RosterDay { return mustGet(st.Roster("HHG0012", mon, mon))["2026-10-19"] }
	set := func(shift string) *httptest.ResponseRecorder {
		return postForm(h, "/roster", formOf("emp", "HHG0012", "day", "2026-10-19", "shift", shift), session)
	}

	// Setting a cell answers with that cell, selected and titled with who changed it and when.
	w := set(id(shift2.ID))
	if w.Code != 200 {
		t.Fatalf("POST /roster = %d, want 200: %s", w.Code, w.Body)
	}
	wantAll(t, w.Body.String(), `value="`+id(shift2.ID)+`" selected`, `title="changed by ana on 2026-10-18 at 12:00"`)
	if got := stored(); got.Start != "11:00" || got.ChangedBy != "ana" {
		t.Fatalf("stored day = %+v, want shift starting 11:00 changed by ana", got)
	}

	// Editing the shift later leaves the stored day alone, and the page shows the old times as the selection.
	if _, err := st.SaveShift(Shift{ID: shift2.ID, OrgID: cafe.ID, Name: "2", Start: "12:00", End: "21:00"}); err != nil {
		t.Fatal(err)
	}
	if got := stored(); got.Start != "11:00" {
		t.Errorf("after editing the shift, stored Start = %q, want 11:00", got.Start)
	}
	page := getWithCookies(h, "/roster?org="+id(cafe.ID)+"&week=2026-10-19", session).Body.String()
	wantAll(t, page, "HHG0012", `value="-1" selected disabled>2 · 11:00–20:00 (old times)`, `value="`+id(shift2.ID)+`">2 · 12:00–21:00`)

	// A shift of another organisation is refused and the day is unchanged.
	officeShift := mustGet(st.SaveShift(Shift{OrgID: office.ID, Name: "X", Start: "09:00", End: "18:00"}))
	before := stored()
	if w := set(id(officeShift)); w.Code != 400 {
		t.Errorf("POST with an Office shift = %d, want 400", w.Code)
	}
	if got := stored(); got != before {
		t.Errorf("a refused shift changed the day: %+v, want %+v", got, before)
	}
}

func TestRosterWeek(t *testing.T) {
	h, session, _, office, cafe := rosterFixture(t)
	get := func(path string) *httptest.ResponseRecorder { return getWithCookies(h, path, session) }
	selects := func(page, emp string) int { // the selects in emp's table row
		_, row, _ := strings.Cut(page, emp)
		row, _, _ = strings.Cut(row, "</tr>")
		return strings.Count(row, "<select")
	}

	// The default is next week, and any day of a week shows that week.
	def := get("/roster")
	if def.Code != 200 {
		t.Fatalf("GET /roster = %d, want 200", def.Code)
	}
	page := def.Body.String()
	wantAll(t, page, "Mon 19 Oct", "Sun 25 Oct 2026", "week=2026-10-12", "week=2026-10-26", "org="+strconv.FormatInt(cafe.ID, 10))
	if thu := get("/roster?week=2026-10-22").Body.String(); thu != page {
		t.Error("a Thursday in the week shows a different page from the default")
	}

	// Rows are the employees active at some point in the week; days outside their period have no select.
	for emp, want := range map[string]int{"HHG0012": 7, "HHG0013": 5} {
		if got := selects(page, emp); got != want {
			t.Errorf("%s has %d selects, want %d", emp, got, want)
		}
	}
	if strings.Contains(page, "HHG0014") || strings.Contains(page, "HHG0002") {
		t.Error("the page lists an employee who left before the week or works in another organisation")
	}

	for _, q := range []string{"org=" + strconv.FormatInt(office.ID, 10), "org=999", "week=19/10/2026"} {
		if w := get("/roster?" + q); w.Code != 400 {
			t.Errorf("GET /roster?%s = %d, want 400", q, w.Code)
		}
	}
}

// A refused change saves nothing and answers 400: an unknown or off-roster employee or a bad day with a plain
// message, anything else with the stored cell and the message.
func TestRosterRejectsBadInput(t *testing.T) {
	h, session, st, office, cafe := rosterFixture(t)
	shift := mustGet(st.Shifts(cafe.ID))[1]
	officeShift := mustGet(st.SaveShift(Shift{OrgID: office.ID, Name: "X", Start: "09:00", End: "18:00"}))
	if err := st.SetRoster("HHG0012", date("2026-10-19"), shift.ID, "bo", at("2026-10-17 09:00:00")); err != nil {
		t.Fatal(err)
	}
	from, to := date("2000-01-01"), date("2100-01-01")
	state := func() string {
		return fmt.Sprint(mustGet(st.Roster("HHG0012", from, to)), mustGet(st.Roster("HHG0013", from, to)), mustGet(st.Roster("HHG0002", from, to)))
	}
	before := state()

	sid := strconv.FormatInt(shift.ID, 10)
	for _, c := range []struct {
		emp, day, shift, msg string
		cell                 bool
	}{
		{"NOPE", "2026-10-19", sid, "unknown employee", false},
		{"HHG0002", "2026-10-19", sid, "not on a roster", false}, // Office is a fixed organisation
		{"HHG0012", "19/10/2026", sid, "bad day", false},
		{"HHG0013", "2026-10-19", sid, "not active that day", true}, // starts on the 21st
		{"HHG0014", "2026-10-19", sid, "not active that day", true}, // left on the 10th
		{"HHG0012", "2026-10-19", "", "unknown shift", true},
		{"HHG0012", "2026-10-19", "x", "unknown shift", true},
		{"HHG0012", "2026-10-19", "-1", "unknown shift", true}, // the "(old times)" option
		{"HHG0012", "2026-10-19", "999", "unknown shift", true},
		{"HHG0012", "2026-10-19", strconv.FormatInt(officeShift, 10), "unknown shift", true},
	} {
		w := postForm(h, "/roster", formOf("emp", c.emp, "day", c.day, "shift", c.shift), session)
		if w.Code != 400 {
			t.Errorf("POST %+v = %d, want 400", c, w.Code)
		}
		wantAll(t, w.Body.String(), c.msg)
		if c.cell {
			wantAll(t, w.Body.String(), `class="cell"`, `role="alert"`)
		}
	}
	if after := state(); after != before {
		t.Errorf("refused input changed the roster:\nbefore %s\nafter  %s", before, after)
	}

	// The cell that comes back still shows what is stored.
	w := postForm(h, "/roster", formOf("emp", "HHG0012", "day", "2026-10-19", "shift", "x"), session)
	wantAll(t, w.Body.String(), `value="`+sid+`" selected`, `title="changed by bo on 2026-10-17 at 09:00"`)
}
