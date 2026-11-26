# Part-time Summary Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A "Part-time summary" dashboard page that shows each part-timer's capped daily hours and pay for a 26th–25th
pay period, lets a manager fill missing check-ins/outs and record breaks, and sets the single part-time daily wage.

**Architecture:**
- **Stored data:** employees gain a `part_time` flag. New tables `breaks` and `settings` hold the breaks and the wage.
- **The page** reuses `Store.Report`, which already gives each day's check-in/out from taps plus corrections. A pure
  function then turns each `Result` plus that day's breaks into hours and pay.
- **Missing times** are saved through the same correction logic as the People page, refactored into one shared
  function.

**Tech Stack:** Go (standard library), html/template + htmx, MySQL (production) and SQLite (development).

**Spec:** `docs/superpowers/specs/2026-11-26-part-time-summary-design.md`

## Global Constraints

- **Hours per day** = worked time (check-out − check-in − merged breaks inside that span), rounded to the nearest whole
  minute, then rounded **up** to whole hours, then capped at **8**.
- **Day's pay** = wage × hours ÷ 8, rounded to whole rupiah. A `?`, `…` or `–` day pays 0.
- **The wage** is one stored setting for all part-timers, defaulting to **130000** when never set. Changing it changes
  every period, past ones included.
- **A pay period** named month M runs from the 26th of M−1 through the 25th of M, inclusive (November 2026 =
  2026-10-26 … 2026-11-25).
- **Default period:** the calendar month of today's attendance day (03:00 cutoff). 26–30 Nov opens November;
  1–31 Dec opens December.
- **Attendance days** run 03:00 to 02:59 (`dayStartHour`, `attendanceDate`).
- **Cell states:**
  - `–`: no in and no out.
  - A number: both in and out.
  - `?`: only one of them, and the day is over (the `FlagNoCheckIn`/`FlagNoCheckOut` flags).
  - `…`: only one of them, and the day is still running.
- **`+?` mark:** a summary row, and the grand total, are marked `+?` when any of their days is `?` or `…`.
- **Who appears:** only employees with the part-time flag set, and only on days inside their
  `active_from`/`active_until`.
- **Breaks:** only for part-time employees, and only on days inside their active period. A break belongs to the
  attendance day of its start and may not end after that day's end (03:00 next morning). Overlapping breaks are merged
  before subtracting.
- **Amounts** are formatted `Rp 3.445.000`, with dots as thousands separators.
- **Missing times** saved from this page use the same validation and storage as the People page's correction form.
- **Data rules:**
  - Every schema change goes in both `server/schema_mysql.sql` and `server/schema_sqlite.sql`.
  - Existing databases are upgraded in place: new tables use `CREATE TABLE IF NOT EXISTS`, and new columns follow the
    `addEventHash` pattern in `server/store.go`.
  - All SQL lives in `Store` methods.
- **Unchanged:** the Calendar and People pages and the lateness rules. They ignore breaks and the part-time flag.
- **No new dependencies.** Match the existing page style (`server/static/style.css`: cards, `table.data`, `.scroll`,
  `.fields`), including the phone layout and dark mode.
- **Commits:** stage files by name, never `git add -A`. The working tree holds unrelated untracked files, among them an
  `.xlsx` with staff data. Every commit message ends with
  `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.

## Review Focus

- **Taps outside the active period:** a part-timer's taps before `active_from` or after `active_until` show nothing
  and pay nothing (test in Task 5).
- **Overnight breaks:** a break on an overnight shift (shift 18:00–01:30, break 23:00 → 00:30 the next calendar day)
  belongs to the same attendance day and is subtracted (tests in Tasks 3 and 6).
- **Filling a check-out after midnight:** a `?` filled with check-out `00:30` lands on the next calendar day of the
  same attendance day, and produces hours (test in Task 6).
- **Changing the wage:** every period's pay recalculates with the new wage (test in Task 6).
- **Removing the part-time flag:** the employee disappears from the page, but their breaks are kept. Setting the flag
  again brings them back (test in Task 5).

---

### Task 1: Part-time flag on employees

**Files:**
- Modify:
  - `server/schema_mysql.sql`, `server/schema_sqlite.sql`: add `part_time` to `CREATE TABLE employees`.
  - `server/store.go`: generalise `addEventHash` into a column helper, and add the `part_time` migration.
  - `server/data.go`: `Employee`, `AddEmployee`, `UpdateEmployee`, `Employees`.
  - `server/admin.go` (`saveEmployee`), `server/templates/employees.html`.
- Test: `server/store_test.go`, `server/admin_test.go`

**Interfaces:**
- Produces:
  - `Employee` gains `PartTime bool`.
  - `AddEmployee(e Employee) error` stores `e.PartTime`.
  - `UpdateEmployee(id, name string, orgID int64, partTime bool) error`. Update every caller, including tests.
  - `Employees(orgID int64) ([]Employee, error)` fills `PartTime`.
- Columns:
  - SQLite: `part_time INTEGER NOT NULL DEFAULT 0`.
  - MySQL: `part_time TINYINT(1) NOT NULL DEFAULT 0`.
- Helper: `ensureColumn(db *sql.DB, driver, table, column, sqliteAdd, mysqlAdd string) error` adds the column only when
  `pragma_table_info` (SQLite) or `information_schema.COLUMNS` (MySQL, `TABLE_SCHEMA = DATABASE()`) lacks it.
  `addEventHash` keeps its SQLite index step and calls `ensureColumn` for the column part.
- `copy-db` is unchanged: copied employees get `part_time = 0`.

- [ ] **Step 1: Write the failing tests**
  - `TestOpenStoreAddsPartTime` (store_test.go):
    1. Create a SQLite file with an old `employees` table without `part_time`, holding one row.
    2. `OpenStore` it twice; no error.
    3. `Employees(0)` returns the old row with `PartTime == false`.
    4. `UpdateEmployee(id, name, org, true)` then `Employees(0)` returns `PartTime == true`.
  - `TestEmployeePartTimeForm` (admin_test.go):
    - POST `/employees` with `part_time=on` creates a part-timer. The list page has a checked checkbox named
      `part_time` for that employee.
    - POST `/employees/{id}` without `part_time` clears it.

- [ ] **Step 2: Run them** with `go test ./server/ -run 'PartTime'`. Expected: FAIL (compile error on
  `UpdateEmployee` arity, or the missing field).

- [ ] **Step 3: Implement** the schema, `ensureColumn` and the migration, the store methods, and `saveEmployee`
  (`part_time` checkbox: present = true).
  - Add the form: `<label><input type="checkbox" name="part_time"> Part-time</label>` in the add form and in each
    row's edit form, checked from `.PartTime`.
  - Add a "Part-time" header to the row form's column: rename the "Name and organisation" header to "Name,
    organisation and part-time".

- [ ] **Step 4: Verify** with `go vet ./... && go test ./...`. Expected: PASS. Also run once with `TEST_MYSQL_DSN`
  against a throwaway MySQL 8.4 container:

  ```bash
  docker run -d --rm --name pt-mysql -e MYSQL_ROOT_PASSWORD=pw -e MYSQL_DATABASE=hhg -p 127.0.0.1:3399:3306 mysql:8.4
  ```

  Then run `TEST_MYSQL_DSN='root:pw@tcp(127.0.0.1:3399)/hhg?charset=utf8mb4&collation=utf8mb4_bin' go test ./server/`
  and `docker stop pt-mysql`.

- [ ] **Step 5: Commit**

```bash
git add server/schema_mysql.sql server/schema_sqlite.sql server/store.go server/data.go server/admin.go server/templates/employees.html server/store_test.go server/admin_test.go
git commit -m "feat: part-time flag on employees"
```

---

### Task 2: Breaks and the wage setting in the store

**Files:**
- Modify: `server/schema_mysql.sql`, `server/schema_sqlite.sql`. Add the new tables there; `CREATE TABLE IF NOT
  EXISTS` upgrades existing databases.
- Create: `server/parttime_store.go`
- Test: `server/parttime_store_test.go`

**Interfaces:**
- Produces:

```go
// Break is a part-timer's unpaid break. Day is the attendance date (midnight, s.loc) of Start.
type Break struct {
	ID         int64
	EmployeeID string
	Day        time.Time
	Start, End time.Time
	By         string
	At         time.Time
}
func (s *Store) AddBreak(b Break) (int64, error)
func (s *Store) Breaks(from, to time.Time) ([]Break, error) // Day from..to inclusive, ordered by Day, EmployeeID, Start
func (s *Store) DeleteBreak(id int64) error
func (s *Store) PartTimeWage() (int64, error)              // 130000 when never set
func (s *Store) SetPartTimeWage(wage int64) error
const defaultPartTimeWage = 130000
```

- Tables:
  - `breaks`: `id` (INTEGER PRIMARY KEY AUTOINCREMENT / INT AUTO_INCREMENT), `employee_id` (FK employees),
    `day VARCHAR(10)`, `start_at VARCHAR(19)`, `end_at VARCHAR(19)`, `created_by VARCHAR(100)`,
    `created_at VARCHAR(19)`, and an index on `day`.
  - `settings`: `name VARCHAR(50) PRIMARY KEY`, `value VARCHAR(200) NOT NULL`. The wage is stored under
    `part_time_wage`.
- Formats: times are `stampFmt` in `s.loc` and days are `dateFmt`, as in the rest of the store.

- [ ] **Step 1: Write the failing tests**
  - `TestBreaksRoundTrip`:
    - Add breaks on 2026-11-02 (10:00→13:00) and 2026-11-05.
    - `Breaks(2026-11-01, 2026-11-03)` returns only the first, with all fields equal.
    - `DeleteBreak` of it, then `Breaks` returns none.
  - `TestPartTimeWage`:
    - A fresh store returns `130000`.
    - After `SetPartTimeWage(150000)` it returns `150000`.
    - After `SetPartTimeWage(140000)` it returns `140000` (the upsert works).

- [ ] **Step 2: Run them** with `go test ./server/ -run 'Breaks|PartTimeWage'`. Expected: FAIL (undefined).

- [ ] **Step 3: Implement** the tables and the methods. The upsert uses `isDuplicate` then `UPDATE`, as `SetRoster`
  does.

- [ ] **Step 4: Verify** with `go vet ./... && go test ./...`. Expected: PASS. Also run the MySQL step from Task 1.

- [ ] **Step 5: Commit**

```bash
git add server/schema_mysql.sql server/schema_sqlite.sql server/parttime_store.go server/parttime_store_test.go
git commit -m "feat: store part-time breaks and the daily wage"
```

---

### Task 3: Pay period, daily hours and pay (pure functions)

**Files:**
- Create: `server/parttime.go`
- Test: `server/parttime_test.go`

**Interfaces:**
- Consumes: `Result` (`server/attendance.go`: `In`, `Out`, `Flags`, `HasHours`), `Break` (Task 2), `FlagNoCheckIn`,
  `FlagNoCheckOut`, `FlagManagerEdit`.
- Produces:

```go
func payPeriod(month time.Time) (first, last time.Time) // month: any time in month M; first = 26th of M-1, last = 25th of M (midnight, month's location)
func payMonthOf(day time.Time) time.Time                // the month M whose period contains day (day <= 25 → its month, else the next), as midnight on the 1st
func defaultPayMonth(today time.Time) time.Time         // monthOf(today)

type ptState int
const (
	ptNone    ptState = iota // – no in and no out
	ptHours                  // both in and out
	ptMissing                // ? one side missing and the day is over
	ptOpen                   // … one side missing and the day is still running
)
type session struct{ Start, End time.Time }
type ptDay struct {
	Date     time.Time
	State    ptState
	In, Out  time.Time // zero when missing
	Sessions []session // in → out split around merged breaks; empty unless ptHours
	Worked   time.Duration // after breaks, rounded to the nearest minute
	Break    time.Duration // merged break time inside [In, Out]
	Hours    int           // rounded up, capped
	Capped   bool          // the uncapped rounded-up hours exceeded 8
	Edited   bool          // a manager correction was used
	Pay      int64
}
func partTimeDay(r Result, breaks []Break, wage int64) ptDay // breaks: that employee's breaks for r.Date
func dayPay(wage int64, hours int) int64                    // wage*hours/8, rounded to the nearest rupiah
func rupiah(n int64) string                                 // "Rp 3.445.000"
const maxPaidHours = 8
```

- [ ] **Step 1: Write the failing tests** (in UTC; each case builds a `Result` with the given `In`/`Out`/`Flags` and
  `HasHours` set when both are present):
  - `TestPayPeriod`:
    - Nov 2026 gives 2026-10-26 … 2026-11-25.
    - Jan 2027 gives 2026-12-26 … 2027-01-25.
  - `TestPayMonthOf`:
    - 2026-11-25 → 2026-11-01.
    - 2026-11-26 → 2026-12-01.
    - 2026-12-31 → 2027-01-01.
  - `TestDefaultPayMonth`:
    - 2026-11-27 → 2026-11-01.
    - 2026-12-01 → 2026-12-01.
  - `TestPartTimeDay`, table-driven:
    - **Over the cap:** in 12:40:59, out 22:23:22, no breaks → `Worked` 9h42m, `Hours` 8, `Capped` true, `Pay`
      130000, one session.
    - **One break:** in 06:00, out 15:00, break 10:00–13:00 → `Worked` 6h, `Break` 3h, `Hours` 6, `Pay` 97500,
      sessions [06:00–10:00, 13:00–15:00].
    - **Overlapping breaks are merged:** breaks 10:00–13:00, 10:00–13:00 and 12:00–14:00 → `Break` 4h, `Worked` 5h,
      `Hours` 5.
    - **A break only counts inside [in, out]:** in 06:00, out 15:00, break 05:00–07:00 → `Break` 1h, `Worked` 8h,
      `Hours` 8, `Capped` false.
    - **Seconds round to the nearest minute:** in 06:00:00, out 14:00:20 → `Worked` 8h, `Hours` 8, `Capped` false.
    - **Rounding up:** in 06:00:00, out 12:00:40 → `Worked` 6h01m, `Hours` 7.
    - **Overnight break:** in 18:00, out 01:30 the next day, break 23:00 → 00:30 the next day → `Worked` 6h, `Hours` 6.
    - **Break covering the whole span:** → `Worked` 0, `Hours` 0, `Pay` 0, `State` `ptHours`.
    - **Missing:** only `In`, with `FlagNoCheckOut` → `ptMissing`, `Hours` 0, `Pay` 0.
    - **Open:** only `In`, no flag → `ptOpen`.
    - **No taps:** → `ptNone`.
    - **Edited:** with `FlagManagerEdit` → `Edited` true.
  - `TestDayPay`:
    - (130000, 6) → 97500.
    - (130000, 7) → 113750.
    - (130001, 3) → 48750.
    - (130000, 0) → 0.
  - `TestRupiah`:
    - 3445000 → "Rp 3.445.000".
    - 97500 → "Rp 97.500".
    - 0 → "Rp 0".

- [ ] **Step 2: Run them** with `go test ./server/ -run 'PayPeriod|PayMonthOf|DefaultPayMonth|PartTimeDay|DayPay|Rupiah'`.
  Expected: FAIL (undefined).

- [ ] **Step 3: Implement** `server/parttime.go`. Sort the breaks clipped to [In, Out], merge overlaps, sum them, and
  build the sessions from the gaps. `Worked = (Out−In−Break).Round(time.Minute)`, floored at 0. Hours =
  `ceil(Worked/time.Hour)`, capped at `maxPaidHours`.

- [ ] **Step 4: Run them again.** Expected: PASS. Then `go vet ./...`.

- [ ] **Step 5: Commit**

```bash
git add server/parttime.go server/parttime_test.go
git commit -m "feat: part-time pay period, daily hours and pay rules"
```

---

### Task 4: Share the correction logic

**Files:**
- Modify: `server/web.go` (`correct`)
- Test: the existing correction tests must still pass unchanged. Add `TestSaveCorrectionShared` in
  `server/web_test.go`.

**Interfaces:**
- Produces:

```go
// applyCorrection validates and saves a manager's fix to one day, exactly as POST /corrections does.
// msg is a user-facing rejection ("bad check-in time", "check-out must be after check-in", ...); err is a server error.
func (srv *server) applyCorrection(emps []Employee, empID, day, in, out, by string) (date time.Time, msg string, err error)
```

- `correct` becomes: parse the form, call `applyCorrection`, re-render the person page with `msg` on rejection (as
  today), or redirect to `personURL` on success. Its messages and status codes don't change.

- [ ] **Step 1: Write the failing test** `TestSaveCorrectionShared`:
  - `applyCorrection` with a check-out before the existing check-in returns
    `msg == "check-out must be after check-in"` and saves nothing.
  - With a valid check-out it returns an empty `msg`, and `Store.Report` for that day shows the corrected `Out` and
    `FlagManagerEdit`.

- [ ] **Step 2: Run it** with `go test ./server/ -run 'Correction'`. Expected: FAIL (undefined).

- [ ] **Step 3: Implement** by moving the body of `correct` into `applyCorrection` without changing behaviour.

- [ ] **Step 4: Verify** with `go vet ./... && go test ./server/`. Expected: PASS, including every existing person and
  correction test.

- [ ] **Step 5: Commit**

```bash
git add server/web.go server/web_test.go
git commit -m "refactor: share the correction logic so other pages save fixes the same way"
```

---

### Task 5: The Part-time summary page (read-only)

**Files:**
- Create: `server/parttime_page.go`, `server/templates/parttime.html`, `server/parttime_page_test.go`
- Modify:
  - `server/web.go`: register `GET /part-time`, and add the `rupiah` template function.
  - `server/templates/layout.html`: a sidebar link after Events, `nav` name `parttime`.
  - `server/parttime_store.go`: `EarliestTap`.
  - `server/static/style.css`: grid cells, if needed.

**Interfaces:**
- Consumes: `Store.Report`, `Store.Breaks`, `Store.PartTimeWage`, and the Task 3 functions.
- Produces:
  - `GET /part-time?month=YYYY-MM` (`monthFmt`). It defaults to `defaultPayMonth(srv.today())`, and answers a bad
    month with 400.
  - `func (s *Store) EarliestTap(employeeIDs []string) (time.Time, bool, error)` returns the earliest `device_time`
    among those employees.
  - Page data (template `parttime.html`):

```go
type partTimePage struct {
	pageBase
	Error        string
	Month        string   // YYYY-MM of the shown period
	Title        string   // "26 Oct – 25 Nov 2026"
	Months       []monthOption // dropdown, oldest first
	Days         []time.Time   // first..last
	Rows         []ptRow
	Total        ptTotal
	Wage         int64
	Breaks       []breakRow    // this period's breaks, for the list
	PartTimers   []Employee    // for the break form's employee dropdown
}
type monthOption struct{ Value, Label string; Selected bool } // Label "November 2026"
type ptRow struct {
	Emp               Employee
	Cells             []ptCell // one per period day
	DaysWorked, Hours int      // days with Hours > 0, sum of Hours
	Pay               int64
	Unsure            bool // any ? or … day
}
type ptCell struct{ Active bool; Day ptDay } // Active false = outside active_from/active_until: render blank, not "–"
type ptTotal struct {
	DaysWorked, Hours int
	Pay               int64
	Unsure            bool
}
type breakRow struct {
	ID               int64
	Emp              Employee
	Day, Start, End  string // "Mon 27 Oct", "15:00", "17:00" (End may be the next calendar day: show "00:30 (+1)")
	By               string
}
```

- **Rows:** part-time employees from `Report(0, "", first, last, srv.now())` with at least one active day in the
  period, ordered by ID. Inactive days get `Active: false` and are excluded from the totals.
- **Dropdown:** from `payMonthOf` of the earliest part-timer tap (or the default month when there are no taps), through
  `payMonthOf(srv.today())`, and always including the default month.
- **Cells:**
  - A number for hours.
  - `?`, `…` and `–` for the other states.
  - Each non-empty cell carries its detail: the sessions "06:00 → 10:00, 13:00 → 15:00", "worked 6h00m · break 3h",
    "(capped at 8)" when capped, "manager edit" when edited. It is in a `title` attribute and also in an element
    revealed on tap/click, so it works on phones. The mechanism is the builder's call (a `<button>` cell with a
    native `popover`, or a shared `<dialog>`).
- **Summary table** (above the grid): Name | Days | Hours | Daily wage | Pay, plus a "Total" row. Unsure rows and the
  total are suffixed `+?`.

- [ ] **Step 1: Write the failing tests** in `parttime_page_test.go`. Use a fixture with a Cafe part-timer `PT1`
  (active from 2026-10-01), a full-timer `FT1`, and a second part-timer `PT2` whose `active_from` is 2026-11-10. `now`
  is 2026-11-27 10:00 in `testLoc`.
  - **Taps:**
    - PT1, 2026-10-25 08:00 / 16:00 (outside the November period).
    - PT1, 2026-10-26 06:00 / 15:00, with a break 10:00–13:00 → 6.
    - PT1, 2026-10-27 12:40:59 / 22:23:22 → 8.
    - PT1, 2026-10-28 09:00 only → `?`.
    - PT1, 2026-10-29 18:00 only → `?` (used by Task 6's after-midnight test).
    - PT2, 2026-11-05 09:00 / 17:00 (before its `active_from`).
    - PT2, 2026-11-12 09:00 / 15:00 → 6.
    - FT1, 2026-10-27 09:00 / 17:00.
  - `TestPartTimePageDefaults`: GET `/part-time` → 200.
    - Contains "26 Oct – 25 Nov 2026" and a selected `November 2026` option.
    - Has no column for 25 Oct.
    - PT1's row shows `6`, `8` and `?`.
    - PT1's summary shows Hours `14`, Pay `Rp 227.500`, marked `+?`.
    - No `FT1`.
    - PT2 shows `6` on 12 Nov and nothing (no hours, no pay) for 5 Nov.
  - `TestPartTimePageMonthParam`:
    - `?month=2026-12` → "26 Nov – 25 Dec 2026", selected `December 2026`.
    - `?month=bad` → 400.
  - `TestPartTimeFlagOff`:
    - After `UpdateEmployee(PT1, …, false)`, PT1 is absent and the break row still exists in the store.
    - After setting the flag again, PT1 is back with the same `6`.
  - Add `parttime` to `TestActiveNav`'s table: GET `/part-time` marks the "Part-time summary" link `aria-current`.

- [ ] **Step 2: Run them** with `go test ./server/ -run 'PartTimePage|PartTimeFlagOff|ActiveNav'`. Expected: FAIL.

- [ ] **Step 3: Implement** the handler, `EarliestTap`, the template and the nav link (label "Part-time summary", an
  inline SVG icon in the style of the others). The grid sits in `.scroll` so it scrolls sideways on a phone, and the
  summary table must fit at phone width.

- [ ] **Step 4: Verify** with `go vet ./... && go test ./server/`. Expected: PASS. Then run the app (`go run ./server`
  with SQLite) and look at `/part-time` at desktop and phone width, in light and dark mode. Say in the report what was
  checked.

- [ ] **Step 5: Commit**

```bash
git add server/parttime_page.go server/parttime_page_test.go server/templates/parttime.html server/templates/layout.html server/web.go server/parttime_store.go server/static/style.css server/web_test.go
git commit -m "feat: part-time summary page with daily hours and pay per period"
```

---

### Task 6: Forms on the page: fill a missing time, breaks, wage

**Files:**
- Modify: `server/parttime_page.go`, `server/templates/parttime.html`, `server/web.go` (routes)
- Test: `server/parttime_page_test.go`

**Interfaces:**
- Consumes: `applyCorrection` (Task 4), `AddBreak`, `DeleteBreak`, `SetPartTimeWage` (Task 2).
- Produces:

| Route | Form fields | On success |
|---|---|---|
| `POST /part-time/corrections` | `month`, `emp`, `day`, `in`, `out` | Calls `applyCorrection`, redirects 303 to `/part-time?month=<month>` |
| `POST /part-time/breaks` | `month`, `emp`, `start_date`, `start_time`, `end_date`, `end_time` | Redirects 303 to the same month |
| `POST /part-time/breaks/{id}/delete` | `month` | Redirects 303 to the same month |
| `POST /part-time/wage` | `month`, `wage` | Redirects 303 to the same month |

- Every rejection re-renders the page for the submitted month with **400** and the message in `Error`.
- For the correction form, the message shows inside the reopened dialog, pre-filled with the submitted values.
- Forms are wrapped with `http.MaxBytesReader(maxForm)` like the admin forms.

**Validation messages (exact):**

| Form | Condition | Message |
|---|---|---|
| Break | unknown employee | "unknown employee" |
| Break | employee isn't part-time | "breaks are only for part-time employees" |
| Break | unparseable date or time | "bad break start" / "bad break end" |
| Break | end not after start | "break must end after it starts" |
| Break | end after the start's attendance day ends (03:00 next morning) | "a break must end by 03:00 the next morning" |
| Break | start's attendance day outside the active period | "employee was not active that day" |
| Wage | not a whole number of rupiah from 1 to 100000000 | "wage must be a whole number of rupiah" |

- **The fill-in dialog:** opened from any `?` or `…` cell (and optionally any cell). It shows the employee and day,
  with `in` and `out` `type=time` inputs pre-filled with the day's current values (`clockFmt`), both editable.
- **The break dialog:** opened by "Add break". The employee `<select>` lists part-timers only. The start/end dates
  default to the period's last day.
- **The break list:** each row has a Delete button with `hx-confirm`.
- **The wage form:** a small form in the summary card header ("Daily wage" input + Save).

- [ ] **Step 1: Write the failing tests** (same fixture as Task 5):
  - `TestPartTimeFillMissing`:
    - POST `/part-time/corrections` (`emp` PT1, `day` 2026-10-28, `in` 09:00, `out` 17:30) → 303. The page then
      shows 28 Oct as `8` (8h30m rounds up to 9, capped at 8), with "(capped at 8)" in its detail. PT1 is still
      `+?` because of 29 Oct.
    - `GET /person?emp=PT1&month=2026-10` shows a manager edit on the 28th.
  - `TestPartTimeFillAfterMidnight`:
    - POST `emp` PT1, `day` 2026-10-29, `in` 18:00, `out` 00:30 → 303. 29 Oct shows `7` (6h30m).
    - After also filling 28 Oct, PT1's summary has no `+?`.
  - `TestPartTimeFillReject`: `out` 08:00 with the existing `in` 09:00 → 400, and the body contains
    "check-out must be after check-in" inside the dialog.
  - `TestPartTimeAddBreak`:
    - A break for PT1 on 2026-10-27, 15:00–17:00 → 303. 27 Oct still shows `8`, with detail "worked 7h42m · break
      2h" and sessions "12:40 → 15:00, 17:00 → 22:23".
    - Delete it via `POST /part-time/breaks/{id}/delete` → 303, and the detail no longer mentions a break.
    - **Overnight:** a break on 2026-11-12 (PT2 active) from 23:00 to 00:30 on 2026-11-13 → 303, attached to day
      2026-11-12.
  - `TestPartTimeBreakRejects`: each validation row above → 400 with its exact message. For example, FT1 → "breaks
    are only for part-time employees", 02:30 → 03:30 → "a break must end by 03:00 the next morning", and PT2 on
    2026-11-05 → "employee was not active that day".
  - `TestPartTimeWage`:
    - POST `wage=150000` → 303. PT1's Pay becomes `Rp 262.500` (14 h × 150000 / 8), and the November period recomputes
      too.
    - `wage=abc` and `wage=-5` → 400 "wage must be a whole number of rupiah".

- [ ] **Step 2: Run them** with `go test ./server/ -run 'PartTime'`. Expected: the new tests FAIL.

- [ ] **Step 3: Implement** the handlers and the dialogs. Use native `<dialog>`, opened by a few lines of inline script
  in the template, as the layout already does for its inline script.

- [ ] **Step 4: Verify** with `go vet ./... && go test ./...`. Expected: PASS. Then repeat Task 5's look at the page in
  the browser, this time exercising each form once.

- [ ] **Step 5: Commit**

```bash
git add server/parttime_page.go server/parttime_page_test.go server/templates/parttime.html server/web.go
git commit -m "feat: fill missing times, record breaks and set the wage on the part-time page"
```

---

### Task 7: Docs

**Files:**
- Modify:
  - `DEPLOY.md`: one short section saying the page exists, the pay rules, that the wage is set on the page, and that
    changing it changes past periods.
  - `docs/superpowers/specs/2026-11-26-part-time-summary-design.md`: set Status to built.

- [ ] **Step 1: Write the section.** Every rule must match Global Constraints.
- [ ] **Step 2: Commit**

```bash
git add DEPLOY.md docs/superpowers/specs/2026-11-26-part-time-summary-design.md
git commit -m "docs: part-time summary and pay rules"
```
