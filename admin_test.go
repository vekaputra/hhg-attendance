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
	wantAll(t, page, "HHG0020", "Rina", "Cafe", "2026-10-18", `action="/employees/HHG0020/deactivate"`,
		"2026-10-10", `href="/employees"`, `href="/orgs"`, `href="/leave"`, `href="/holidays"`)
	if strings.Contains(page, "/employees/HHG0004/deactivate") {
		t.Error("a deactivated employee still has a deactivate button")
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
	wantAll(t, get("/orgs"), "Office", "Cafe", `action="/orgs/`+cafeID+`/shifts"`,
		`name="weekdays" value="3" checked`, `name="weekdays" value="2">`)

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

		{"/orgs", fixed("id", "999"), "unknown organisation"},
		{"/orgs", fixed("name", " "), nameMsg},
		{"/orgs", fixed("name", "Cafe"), "already exists"},
		{"/orgs", fixed("id", "", "name", "Office"), "already exists"},
		{"/orgs", fixed("kind", "daily"), "kind must be fixed or roster"},
		{"/orgs", fixed("grace", "abc"), "grace must be 0 to 240"},
		{"/orgs", fixed("grace", "-1"), "grace must be 0 to 240"},
		{"/orgs", fixed("grace", "241"), "grace must be 0 to 240"},
		{"/orgs", fixed("start", "25:00"), "bad start time"},
		{"/orgs", fixed("end", "5pm"), "bad end time"},
		{"/orgs", fixed("end", "08:30"), "start and end must differ"},
		{"/orgs", fixed("weekdays", ""), "tick at least one weekday"},

		{"/orgs/" + officeID + "/shifts", formOf("name", "5", "start", "08:00", "end", "14:00"), "only roster organisations"},
		{"/orgs/999/shifts", formOf("name", "5", "start", "08:00", "end", "14:00"), "unknown organisation"},
		{"/orgs/" + cafeID + "/shifts", formOf("name", "", "start", "08:00", "end", "14:00"), nameMsg},
		{"/orgs/" + cafeID + "/shifts", formOf("name", "5", "start", "8am", "end", "14:00"), "bad start time"},
		{"/orgs/" + cafeID + "/shifts", formOf("name", "5", "start", "08:00", "end", "08:00"), "start and end must differ"},
		{"/orgs/" + cafeID + "/shifts", formOf("shift_id", "999", "name", "5", "start", "08:00", "end", "14:00"), "unknown shift"},
		{"/orgs/" + cafeID + "/shifts", formOf("shift_id", "x", "name", "5", "start", "08:00", "end", "14:00"), "unknown shift"},
		{"/orgs/" + kitchenID + "/shifts", formOf("shift_id", cafeShift, "name", "5", "start", "08:00", "end", "14:00"), "unknown shift"}, // Cafe's shift

		{"/leave", formOf("emp", "NOPE", "from", "2026-10-20", "to", "2026-10-22"), "unknown employee"},
		{"/leave", formOf("emp", "HHG0002", "from", "2026-10-2", "to", "2026-10-22"), "bad from date"},
		{"/leave", formOf("emp", "HHG0002", "from", "2026-10-20", "to", ""), "bad to date"},
		{"/leave", formOf("emp", "HHG0002", "from", "2026-10-22", "to", "2026-10-20"), "cannot end before it starts"},

		{"/holidays", formOf("day", "25/12/2026"), "bad day"},
		{"/holidays", formOf("day", "2026-12-25"), "already a holiday"},
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
