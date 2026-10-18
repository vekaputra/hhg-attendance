# Attendance System Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Turn the taps the EHome listener receives into an attendance record. Managers log in and see late, absent, missing-tap and hours information for the office and cafe, in three calendar views.

**Architecture:** One Go program (`package main`) with three parts:
- the existing UDP EHome listener, which now writes taps and device contact times to SQLite;
- a pure rules engine (`attendance.go`) that turns one person-day's inputs into a result;
- an htmx web app (server-rendered `html/template`, `hx-boost` for navigation) that reads reports built from the store.

**Tech Stack:** Go 1.27 (stdlib `net/http`, `html/template`, `crypto/pbkdf2`, `http.CrossOriginProtection`, `embed`), `modernc.org/sqlite` (pure-Go SQLite driver; the only new dependency), htmx 2.0.4 (vendored file).

**Spec:** `docs/superpowers/specs/2026-10-18-attendance-system-design.md`. Read it before starting any task; rule names below refer to its "Rules" section.

## Global Constraints

- **Package:** everything stays in `package main` at the repo root. Tests are `*_test.go` next to the code. Run `go vet ./... && go test ./...` before every commit.
- **Dependencies:** the only new one is `modernc.org/sqlite`. No web framework, router, ORM, or password or session library.
- **Business timezone:** set by `var timezone` in `main.go`, with no default. `main` exits with an error if it is empty or `time.LoadLocation` fails.
  - Every date and time shown or stored is business-local, formatted with these constants:
    - `dateFmt = "2006-01-02"`
    - `clockFmt = "15:04"`
    - `stampFmt = "2006-01-02 15:04:05"`, which is the device's `Time` format.
  - The device sends `Time` without a zone. Parse it with `time.ParseInLocation(stampFmt, s, loc)`.
- **Attendance day:** 03:00 to 02:59 the next morning, as `dayStartHour = 3`. A double tap is one within `doubleTapGap = 2 * time.Minute` of the previous tap. A device is flagged stale after `contactWarnAfter = 2 * time.Minute`.
- **Ports:** UDP `port = "7600"` (existing) for EHome. The web app listens on TCP `webAddr = ":8080"`; the owner's reverse proxy handles HTTPS.
- **Existing listener behaviour stays unchanged:**
  - device-ID gate (`allowedDeviceIDs`), NUL trimming, and the 403 + `<Method>REGISTER</Method>` reply to unregistered senders;
  - `event` log lines for ACS events without an employee number, and `tap` log lines;
  - the REGISTER password is never read or logged;
  - `ehome.log` file logging.
- **Form input:** every form value is validated in its handler. Employee IDs match `^[A-Za-z0-9_-]{1,32}$`, names are 1–100 characters, times parse as `clockFmt` and dates as `dateFmt`. Invalid input re-renders the page with HTTP 400 and a message, and saves nothing.
- **Login:**
  - Every route except `GET/POST /login` and `/static/` requires a session.
  - Cookies use `HttpOnly`, `Secure` and `SameSite=Lax`.
  - The whole mux is wrapped in `http.NewCrossOriginProtection().Handler`.
- **Git:** commit steps assume the repo is a git repository. It isn't one yet: run `git init` once before Task 1, or skip the commit steps if the owner says so.
- **SQLite:** open with `_pragma=foreign_keys(1)`, `_pragma=busy_timeout(5000)` and `_pragma=journal_mode(WAL)`. The listener goroutine and HTTP handlers share one `*sql.DB`.
- **MySQL later:** SQLite is for development; production will move to MySQL.
  - All SQL lives in `Store` methods: `store.go`, `taps.go`, `data.go`, and the store half of `auth.go`. No SQL in handlers, `report.go` logic or templates.
  - No SQLite-only SQL functions (`date()`, `strftime()`, `julianday()`). Dates are `dateFmt` and `stampFmt` strings, compared as strings, with date maths in Go.
  - SQLite-specific upsert and ignore syntax (`INSERT OR IGNORE`, `ON CONFLICT`) is fine. It's confined to those store methods, which are the only places to change.

## Review Focus

1. **Server restarts mid-shift.** The device's next ACS gets a 403 and is not stored. After the device re-registers, the same ACS (same serial and time) must be stored exactly once. *(Test in Task 2.)*
2. **Tap after midnight at a month boundary.** A shift 3 check-out at `2026-11-01 01:00` belongs to 31 October and must count in October's person totals, not November's. *(Test in Task 5.)*
3. **Server clock in UTC.** On a UTC VPS, "is the scheduled end past?" and "today" must use the business timezone. Absent must not appear 7 hours early or late. *(Test in Task 5: the report is given `now` in another zone and must convert.)*
4. **Unusable ACS fields.** An ACS whose `Time` doesn't parse, or whose employee isn't in the employees table, must be stored or logged in full. It must never be silently dropped. *(Test in Task 2: a bad `Time` is acked and logged at Warn with all fields and not stored. An unknown employee is stored, and reports ignore it.)*
5. **A week that crosses a month boundary.** Mon 28 Sep – Sun 4 Oct 2026 must show 7 days across both months. Navigating with `AddDate`, not by adding hours, must not skip or repeat days. *(Test in Task 7.)*

---

### Task 1: SQLite store, schema and seed data

**Files:**
- Create: `store.go`, `schema.sql`, `store_test.go`
- Modify: `main.go` (add `timezone` and `dbPath` vars; load the location and open the store at startup)

**Interfaces:**
- Produces:
  - `type Store struct { db *sql.DB; loc *time.Location }`
  - `func OpenStore(path string, loc *time.Location) (*Store, error)`: opens the DB, executes the embedded `schema.sql`, and seeds when the `orgs` table is empty.
  - `func (s *Store) Close() error`
  - `type Org struct { ID int64; Name, Kind string; GraceMinutes int; Start, End, Weekdays string }`
    - `Kind` is `"fixed"` or `"roster"`.
    - `Start` and `End` are in `clockFmt`.
    - `Weekdays` holds ISO weekday digits, for example `"12345"` (1 = Monday, 7 = Sunday).
    - `Start`, `End` and `Weekdays` are empty for roster organisations.
  - `type Shift struct { ID, OrgID int64; Name, Start, End string }`
  - `func (s *Store) Orgs() ([]Org, error)`, ordered by ID.
  - `func (s *Store) Shifts(orgID int64) ([]Shift, error)`, ordered by start time.
  - The constants `dateFmt`, `clockFmt` and `stampFmt` (Global Constraints).
- **Schema tables** (column names are fixed; types and index details are yours):
  - `orgs(id, name UNIQUE, kind, grace_minutes, start_time, end_time, weekdays)`
  - `shifts(id, org_id, name, start_time, end_time)`
  - `employees(id TEXT PK, name, org_id, active_from, active_until NULL)`
  - `taps(device_id, serial_no, device_time, employee_id, received_at, PK(device_id, serial_no, device_time))`, with an index on `(employee_id, device_time)`.
  - `devices(device_id PK, last_contact)`
  - `roster(employee_id, day, shift_name NULL, start_time NULL, end_time NULL, changed_by, changed_at, PK(employee_id, day))`. A row with a `NULL` shift is "off".
  - `leave(id, employee_id, from_day, to_day)`
  - `holidays(day PK)`
  - `corrections(employee_id, day, check_in NULL, check_out NULL, changed_by, changed_at, PK(employee_id, day))`
  - `users(name PK, salt, hash)`
  - `sessions(token_hash PK, user_name, expires_at)`
- **Seed data:**
  - `Office`: kind `fixed`, grace 0, `08:30`–`17:30`, weekdays `12345`.
  - `Cafe`: kind `roster`, grace 0, with shifts `1` `06:00`–`15:00`, `2` `11:00`–`20:00` and `3` `15:00`–`22:00`.

- [ ] **Step 1: Write the failing test** `TestOpenStoreSeedsOnce`. It opens a store at `t.TempDir()+"/a.db"`, then closes and reopens it. Assertions:
  - `Orgs()` returns exactly 2 orgs: `Office` (fixed, `08:30`, `17:30`, `12345`) and `Cafe` (roster).
  - `Shifts(cafe.ID)` returns names `1`, `2` and `3` with the start and end times above.
  - The counts are unchanged after the reopen.
- [ ] **Step 2: Run** `go test -run TestOpenStoreSeedsOnce ./...`. Expected: FAIL (`OpenStore` undefined).
- [ ] **Step 3: Implement** `OpenStore`, `Close`, `Orgs`, `Shifts` and `schema.sql`. Add the driver with `go get modernc.org/sqlite`, and embed the schema with `//go:embed schema.sql`.
- [ ] **Step 4: Wire `main.go`.**
  - Add `var timezone = ""` with the comment `// Business timezone, e.g. "Asia/Jakarta". Fill this in.`
  - Add `var dbPath = "attendance.db"`.
  - In `main`, exit with `slog.Error` when the timezone is empty or invalid, then call `OpenStore`.
  - Keep the listener call as it is for now.
- [ ] **Step 5: Run** `go vet ./... && go test ./...`. Expected: PASS.
- [ ] **Step 6: Commit** `feat: add SQLite store with seeded Office and Cafe`.

### Task 2: Listener stores taps and device contact

**Files:**
- Modify: `ehome.go`, `ehome_test.go`, `main.go` (pass the store to `listenEHome`)
- Create: `taps.go` (store methods for taps and devices), `taps_test.go`

**Interfaces:**
- Consumes: `Store`, `stampFmt` (Task 1).
- Produces:
  - `type Tap struct { DeviceID, SerialNo, EmployeeID string; Time time.Time }`
  - `func (s *Store) InsertTap(t Tap, receivedAt time.Time) error`: `INSERT OR IGNORE`, so a duplicate is not an error.
  - `func (s *Store) Taps(employeeID string, from, to time.Time) ([]time.Time, error)`: the employee's device times with `from <= t < to`, ascending.
  - `func (s *Store) TouchDevice(deviceID string, at time.Time) error`: upsert into `devices`.
  - `func (s *Store) DeviceContacts() (map[string]time.Time, error)`
  - `func listenEHome(addr string, st *Store) error`
  - `func handleEHome(pkt []byte, from net.Addr, registered map[string]bool, st *Store) []byte`
- Behaviour to add to `handleEHome`:
  - **Device contact:** after a successful REGISTER, and for every packet from a registered sender, call `TouchDevice(p.DeviceID, now)`. Log a failure at Warn and carry on.
  - **Storing taps:** for an ACS with an employee number, from a registered sender, parse `p.Time` in `st.loc` and call `InsertTap` **before** returning the 200 reply.
    - If `InsertTap` fails, log at Error and return `nil` (no ack), so the device resends.
    - If `Time` doesn't parse, log at Warn with employeeNo, time, serialNo and deviceID, store nothing, and still return 200. Resending wouldn't fix it.
  - The `tap` log line stays.

- [ ] **Step 1: Update the existing `TestHandleEHome`** to pass a store opened in `t.TempDir()` (with `time.UTC` as `loc`). It must still pass unchanged otherwise.
- [ ] **Step 2: Write the failing tests:**
  - `TestHandleEHomeStoresTapOnce`: register, then send `acsPkt` twice. Both replies are status 200. `Taps("HHG0001", 2026-10-18 03:00, 2026-10-19 03:00)` returns exactly `[2026-10-18 10:42:03]`.
  - `TestHandleEHomeRestart` (Review Focus 1): with an empty `registered` map, send `acsPkt` and expect 403 with nothing stored. Then register and resend `acsPkt`: expect 200 and exactly one stored tap.
  - `TestHandleEHomeBadTime` (Review Focus 4): register, then send `acsPkt` with `<Time>garbage</Time>`. Expect 200 and nothing stored.
  - `TestHandleEHomeUnknownEmployeeStored`: an ACS for `ZZZ9999`, who isn't in the employees table, is stored.
  - `TestTouchDevice`: after REGISTER and one KEEPALIVE from the registered address, `DeviceContacts()["admin"]` is set and later than or equal to its value after REGISTER.
- [ ] **Step 3: Run** `go test -run 'TestHandleEHome|TestTouchDevice' ./...`. Expected: FAIL.
- [ ] **Step 4: Implement** the store methods in `taps.go`, the `handleEHome` and `listenEHome` changes, and in `main.go` pass the store to `listenEHome`.
- [ ] **Step 5: Run** `go vet ./... && go test ./...`. Expected: PASS.
- [ ] **Step 6: Commit** `feat: store taps and device contact from the EHome listener`.

### Task 3: Rules engine, part 1: attendance day and check-in/out

**Files:**
- Create: `attendance.go`, `attendance_test.go`

**Interfaces:**
- Produces:
  - `const dayStartHour = 3`
  - `const doubleTapGap = 2 * time.Minute`
  - `func attendanceDate(t time.Time) time.Time`: midnight, in `t`'s location, of the attendance day containing `t`. Use `time.Date` and `AddDate`, never adding hours.
  - `type Window struct { Start, End time.Time; Shift string }`: a scheduled window on one attendance day. `Shift` is empty for fixed organisations. `End` may fall after midnight.
  - `func windowOn(date time.Time, start, end, shift string) Window`: builds a window from `clockFmt` strings on `date`. If `end <= start`, the end moves to the next day.
  - `func checkInOut(taps []time.Time, w *Window) (in, out time.Time)`. Rules:
    - Sort the taps and drop any within `doubleTapGap` of the previous kept tap.
    - Two or more taps left: the first is in and the last is out.
    - Exactly one tap left: it's `in` if `w == nil` or the tap is before `w`'s midpoint, otherwise `out`.
    - Zero means missing.

- [ ] **Step 1: Write the failing tests.** Use `loc := time.FixedZone("T", 7*3600)` and a helper `at("2026-10-05 08:25:00")`.
  - `TestAttendanceDate`:
    - `2026-10-20 01:00` → `2026-10-19`
    - `2026-10-20 03:00` → `2026-10-20`
    - `2026-11-01 02:59` → `2026-10-31`
  - `TestCheckInOut`, table-driven, with the office window `windowOn(2026-10-05, "08:30", "17:30", "")` unless a case says otherwise:

    | Taps | Expected |
    |---|---|
    | `08:25`, `17:40` | in `08:25`, out `17:40` |
    | `08:20`, `12:00`, `17:35` | in `08:20`, out `17:35` |
    | `08:25:00`, `08:25:25` (double tap) | in `08:25`, out zero |
    | `15:10` only | in zero, out `15:10` (after the 13:00 midpoint) |
    | Shift 3 window (`15:00`–`22:00`), tap `18:00` | in `18:00` |
    | Shift 3 window, tap `19:00` | out `19:00` |
    | Window `nil`, tap `19:00` | in `19:00` |
    | Shift 3 window, `14:40` and `2026-10-06 01:00` | in `14:40`, out `2026-10-06 01:00` |
    | No taps | both zero |

- [ ] **Step 2: Run** `go test -run 'TestAttendanceDate|TestCheckInOut' ./...`. Expected: FAIL.
- [ ] **Step 3: Implement** the functions above in `attendance.go`.
- [ ] **Step 4: Run** `go test ./...`. Expected: PASS.
- [ ] **Step 5: Commit** `feat: attendance day and check-in/out rules`.

### Task 4: Rules engine, part 2: status, flags and hours

**Files:**
- Modify: `attendance.go`, `attendance_test.go`

**Interfaces:**
- Consumes: `Window`, `windowOn`, `checkInOut` (Task 3).
- Produces:
  - `type Fix struct { In, Out time.Time }`: a manager correction. A zero field keeps the tap-derived value.
  - `type Day struct { Date time.Time; Window *Window; Leave, Holiday, Roster bool; Taps []time.Time; Fix *Fix }`
    - `Window` is nil when the person isn't scheduled: weekend, off, holiday, leave, or outside their active period.
    - `Roster` is true when the employee's organisation is a roster type.
  - `type Status string`, with constants:
    - `StatusPending = ""`
    - `StatusOff = "off"`
    - `StatusOnTime = "on time"`
    - `StatusLate = "late"`
    - `StatusAbsent = "absent"`
    - `StatusLeave = "leave"`
    - `StatusHoliday = "holiday"`
    - `StatusPresent = "present"`: worked, but lateness can't be judged.
  - `type Flag string`, with constants:
    - `FlagNoCheckIn = "no check-in"`
    - `FlagNoCheckOut = "no check-out"`
    - `FlagUnrostered = "unrostered"`
    - `FlagManagerEdit = "manager edit"`
  - `type Result struct { Date time.Time; Window *Window; In, Out time.Time; Status Status; LateBy time.Duration; Flags []Flag; Actual, InShift time.Duration; HasHours bool }`
  - `func evaluate(d Day, grace time.Duration, now time.Time) Result`
- **Status order:**
  1. `Leave` → `StatusLeave`.
  2. Else `Holiday` → `StatusHoliday`.
  3. Else no window → `StatusOff`. Add `FlagUnrostered` if `Roster` and there's at least one tap or a fix.
  4. Else no in and no out → `StatusAbsent` if `now` is after `Window.End`, otherwise `StatusPending`.
  5. Else no in → `StatusPresent`.
  6. Else in after `Start + grace` → `StatusLate` with `LateBy = in − Start`.
  7. Else `StatusOnTime`.
- **Flags:**
  - `FlagNoCheckIn` or `FlagNoCheckOut` when exactly one of in and out is set.
  - `FlagManagerEdit` whenever `Fix != nil`.
- **Hours:** set only when both in and out are set.
  - `Actual = out − in`.
  - `InShift` is the overlap of `[in, out]` with the window, or 0 with no window.
  - `HasHours = true`.

- [ ] **Step 1: Write the failing test** `TestEvaluate`, table-driven. Defaults: grace 10m, office window on Mon 2026-10-05 `08:30`–`17:30`, `now` 2026-10-06 12:00.

  | Case | Expected |
  |---|---|
  | Taps `08:40`, `17:31` | on time, Actual `8h51m`, InShift `8h50m` |
  | Taps `08:41`, `17:30` | late, LateBy `11m` |
  | No taps, `now` 2026-10-05 18:00 | absent |
  | No taps, `now` 2026-10-05 10:00 | pending |
  | Tap `16:00` only | present, flags `[no check-in]`, HasHours false |
  | Tap `08:35` only | on time, flags `[no check-out]`, HasHours false |
  | Leave, no window, taps `09:00`, `17:00` | leave, Actual `8h`, InShift `0` |
  | Holiday, no window, no taps | holiday |
  | Roster, no window, taps `11:00`, `20:00` | off, flags `[unrostered]`, Actual `9h` |
  | Shift 3 window, taps `14:40`, `2026-10-06 01:00` | on time, Actual `10h20m`, InShift `7h` |
  | Fix `{In: 08:30}`, taps `17:35` | in `08:30`, on time, flags `[manager edit]`, Actual `9h05m` |
  | Fix `{In: 08:30, Out: 17:30}`, no taps, `now` past the end | on time, flags `[manager edit]`, Actual `9h` |

- [ ] **Step 2: Run** `go test -run TestEvaluate ./...`. Expected: FAIL.
- [ ] **Step 3: Implement** `evaluate` and the types. Apply the fix's non-zero fields over `checkInOut`'s output before the status logic.
- [ ] **Step 4: Run** `go test ./...`. Expected: PASS.
- [ ] **Step 5: Commit** `feat: attendance status, flags and hours`.

### Task 5: Data store methods and the report

**Files:**
- Create: `data.go`, `report.go`, `report_test.go`

**Interfaces:**
- Consumes: Tasks 1–4.
- Produces (store methods in `data.go`):
  - **Organisations and shifts:**
    - `func (s *Store) SaveOrg(o Org) (int64, error)`: inserts when `o.ID == 0`, otherwise updates.
    - `func (s *Store) SaveShift(sh Shift) (int64, error)`: same rule.
  - **Employees:**
    - `type Employee struct { ID, Name string; OrgID int64; ActiveFrom, ActiveUntil string }`: dates in `dateFmt`; `ActiveUntil` is empty while active.
    - `func (s *Store) AddEmployee(e Employee) error`
    - `func (s *Store) DeactivateEmployee(id string, day time.Time) error`
    - `func (s *Store) Employees(orgID int64) ([]Employee, error)`: `orgID == 0` means all; ordered by ID.
  - **Roster:**
    - `func (s *Store) SetRoster(employeeID string, day time.Time, shiftID int64, by string, at time.Time) error`: `shiftID == 0` means off. Copies the shift's name, start and end into the row.
    - `type RosterDay struct { Shift, Start, End, ChangedBy, ChangedAt string }`
    - `func (s *Store) Roster(employeeID string, from, to time.Time) (map[string]RosterDay, error)`: keyed by `dateFmt`, inclusive of both ends.
  - **Leave:**
    - `type Leave struct { ID int64; EmployeeID, From, To string }`
    - `func (s *Store) AddLeave(employeeID string, from, to time.Time) error`: errors if `to` is before `from`.
    - `func (s *Store) DeleteLeave(id int64) error`
    - `func (s *Store) Leaves(from, to time.Time) ([]Leave, error)`: leave overlapping the range.
  - **Holidays:**
    - `func (s *Store) AddHoliday(day time.Time) error`
    - `func (s *Store) DeleteHoliday(day time.Time) error`
    - `func (s *Store) Holidays(from, to time.Time) (map[string]bool, error)`
  - **Corrections:**
    - `type Correction struct { EmployeeID string; Day time.Time; In, Out time.Time; By string; At time.Time }`
    - `func (s *Store) SaveCorrection(c Correction) error`: upsert. If both `In` and `Out` are zero, the correction is deleted.
    - `func (s *Store) Corrections(employeeID string, from, to time.Time) (map[string]Fix, error)`
- Produces (in `report.go`):
  - `type Row struct { Employee Employee; Org Org; Days []Result; Actual, InShift time.Duration; Late, Absent, Flagged int }`: totals over `Days` with `HasHours`; counts of late, absent and flagged days.
  - `func (s *Store) Report(orgID int64, employeeID string, from, to time.Time, now time.Time) ([]Row, error)`. Builds one `Row` per employee: one employee if `employeeID` is set, otherwise everyone in `orgID`.
    - **Employees included:** those with `ActiveUntil` empty or on or after `from`.
    - **Days:** one `Result` per date from `from` to `to` inclusive.
    - **Taps:** read from `[from 03:00, to+1 03:00)`, and each tap goes to its `attendanceDate`.
    - **Window:**
      - fixed organisations: only on the org's weekdays, and not on holidays;
      - roster organisations: from the roster row's snapshot times, and none for an "off" row or a missing row;
      - for both types: none on leave days, and none outside the active period (`ActiveFrom` to `ActiveUntil`).
    - `Day.Holiday` is set only for fixed organisations. Roster organisations ignore holidays.
    - **Time zone:** convert `now` to `s.loc` first.

- [ ] **Step 1: Write the failing test** `TestReport`.
  - **Setup:**
    - A temp store with loc `time.FixedZone("T", 7*3600)`.
    - Employees: `HHG0001` (Office, active from `2026-10-01`) and `HHG0012` (Cafe, active from `2026-10-01`).
    - Holiday `2026-10-07`.
    - Leave for HHG0001 on `2026-10-08`–`2026-10-09`.
    - Roster for HHG0012: shift 2 on `2026-10-05` (no taps), shift 3 on `2026-10-31` (taps `2026-10-31 14:55` and `2026-11-01 01:00`), and shift 2 on `2026-10-06`. After setting all three roster days, edit shift 2 to `12:00`–`21:00` with `SaveShift`.
    - `now` = `2026-11-02 05:00 UTC`.
  - **Assertions on `Report(office, "", 2026-09-28, 2026-10-11, now)`, row HHG0001:**
    - `09-30` is off, not absent (before the active period).
    - `10-03` is off (Saturday).
    - `10-07` is holiday.
    - `10-08` is leave.
    - `10-05` with no taps is absent.
  - **Assertions on `Report(0, "HHG0012", 2026-10-01, 2026-10-31, now)`:**
    - `10-05` is absent.
    - `10-06` has window `11:00`–`20:00` (the snapshot survived the edit).
    - `10-31` has out `2026-11-01 01:00`, and the row's `Actual` includes its `10h05m` (Review Focus 2).
  - **Review Focus 3:** with `now` = `2026-10-05 12:00 UTC` (19:00 local), HHG0012's `10-05` (shift 2 ends 20:00) is pending, not absent.
- [ ] **Step 2: Run** `go test -run TestReport ./...`. Expected: FAIL.
- [ ] **Step 3: Implement** `data.go` and `report.go`. Load each table once per `Report` call, not once per day.
- [ ] **Step 4: Run** `go vet ./... && go test ./...`. Expected: PASS.
- [ ] **Step 5: Commit** `feat: data store methods and attendance report`.

### Task 6: Accounts, sessions and login

**Files:**
- Create: `auth.go`, `auth_test.go`, `templates/login.html`

**Interfaces:**
- Consumes: `Store` (Task 1).
- Produces:
  - Constants:
    - `pbkdf2Iter = 600_000`
    - `sessionCookie = "session"`
    - `sessionTTL = 7 * 24 * time.Hour`
    - `maxFailures = 5`
    - `lockout = 15 * time.Minute`
  - **Users and sessions:**
    - `func (s *Store) SetUser(name, password string) error`: creates the user, or resets their password. Uses a random 16-byte salt and `pbkdf2.Key(sha256.New, password, salt, pbkdf2Iter, 32)`. Errors if the password is shorter than 10 characters.
    - `func (s *Store) CheckUser(name, password string) (bool, error)`: compares in constant time.
    - `func (s *Store) NewSession(user string, now time.Time) (string, error)`: the token comes from `crypto/rand.Text()`. Only `sha256(token)` hex is stored.
    - `func (s *Store) SessionUser(token string, now time.Time) (string, error)`: returns `""` when the session is missing or expired.
    - `func (s *Store) DeleteSession(token string) error`
  - **Login rate limit:** `type limiter struct { mu sync.Mutex; fails map[string][]time.Time }`, keyed by username, with:
    - `func (l *limiter) locked(user string, now time.Time) bool`: true when the user has `maxFailures` failures within `lockout`;
    - `func (l *limiter) fail(user string, now time.Time)`;
    - `func (l *limiter) reset(user string)`.
  - **HTTP handlers:**
    - `func loginHandlers(st *Store, lim *limiter, now func() time.Time, tmpl *template.Template) (get, post, logout http.HandlerFunc)`
    - `func requireLogin(st *Store, now func() time.Time, next http.Handler) http.Handler`: redirects to `/login` with 303 when there's no valid session, and puts the user in the request context.
    - `func currentUser(r *http.Request) string`
  - **Command-line account setup:** `func runAddUser(st *Store, name string, in io.Reader) error`. Reads the password from the first line of `in`; `main` calls it for `hhg-attendance adduser <name>` (Task 11).
- **Login behaviour:**
  - A wrong password or a locked user re-renders the form with HTTP 401 and the same message, "Wrong username or password", so the response doesn't reveal which usernames exist.
  - A successful login calls `reset` and sets the cookie (`HttpOnly`, `Secure`, `SameSite=Lax`, `Path=/`, `MaxAge` matching `sessionTTL`), then redirects 303 to `/calendar`.
  - Logout deletes the session and clears the cookie.

- [ ] **Step 1: Write the failing tests:**
  - `TestUserPassword`: `SetUser("ana", "correct horse")`. Then `CheckUser` gives true for that password and false for `"wrong"`. `SetUser("ana", "short")` errors.
  - `TestLoginFlow`, using `httptest`:
    - POST `/login` with wrong credentials gives 401.
    - The correct ones give 303 with a `session` cookie that has `HttpOnly` and `Secure`.
    - A GET to a `requireLogin`-wrapped handler with that cookie gives 200.
    - Without it, 303 to `/login`.
  - `TestLoginLockout`: 5 wrong POSTs, then the correct password, all within a minute: the 6th gets 401. With `now` +16 minutes, the correct password gets 303.
  - `TestSessionExpiry`: `SessionUser` returns `""` at `now + sessionTTL + 1s`.
- [ ] **Step 2: Run** `go test -run 'TestUser|TestLogin|TestSession' ./...`. Expected: FAIL.
- [ ] **Step 3: Implement** `auth.go` and a minimal `templates/login.html` (username and password form, POST `/login`).
- [ ] **Step 4: Run** `go vet ./... && go test ./...`. Expected: PASS.
- [ ] **Step 5: Commit** `feat: manager accounts, sessions and login rate limit`.

### Task 7: Web shell and organisation calendars (month grid, people × week)

**Files:**
- Create: `web.go`, `web_test.go`, `templates/layout.html`, `templates/calendar.html`, `static/style.css`, `static/htmx.min.js`

**Interfaces:**
- Consumes: `Report`, `Row`, `Result` (Tasks 4–5); `requireLogin`, `loginHandlers` and `limiter` (Task 6); `DeviceContacts` (Task 2); `allowedDeviceIDs` (ehome.go).
- Produces:
  - `const contactWarnAfter = 2 * time.Minute`
  - `func deviceWarnings(contacts map[string]time.Time, allowed []string, now time.Time) []string`: messages such as `"gshhgfp: no contact for 3h"` or `"gshhgfp: no contact yet"`, in `allowed` order.
  - `type server struct { st *Store; loc *time.Location; now func() time.Time; tmpl *template.Template }`: shared by every handler in Tasks 7–10.
  - Template funcs:
    - `hours(d time.Duration) string` gives `"7h00m"` or `"10h20m"`.
    - `mins(d time.Duration) string` gives `"12m"`.
    - `clock(t time.Time) string` gives `"14:40"`.
  - `func newServer(st *Store, loc *time.Location, now func() time.Time) http.Handler`: builds the mux, wraps it in `http.NewCrossOriginProtection().Handler`, and registers the login routes and `GET /static/`. Everything else goes through `requireLogin`.
  - `func (srv *server) render(w http.ResponseWriter, status int, page string, data any)`: executes `layout.html` with the page's `content` block. The layout always shows `deviceWarnings`, the current user, logout, and links to every page.
  - Routes:
    - `GET /` redirects to `/calendar`.
    - `GET /calendar?org=<id>&view=month|week&at=<dateFmt>`. Defaults: the first org, `month`, today. The week view shows the Monday-to-Sunday week containing `at`; the month view shows `at`'s month.
- **Views** (match the mockups `calendar-both-cafe.html` and `people-week-v2.html`):
  - **Page header:** Office/Cafe tabs, a Month/People × days switch, and previous/next links (month ±1, week ±7 days, using `AddDate`).
  - **Month grid:** each day cell lists only results that are late, absent or flagged, as "ID Name · shift · 11m late / absent / flag".
  - **People × week:** column headers like `Mon 5`. Every employee row has 7 cells, and each cell shows the shift, status or minutes late, flags, and "actual / in-shift" hours. The row ends with weekly totals, and each name links to `/person?emp=<id>&month=<YYYY-MM>`.
  - **htmx:** `<body hx-boost="true">` and the vendored `htmx.min.js`, with no other htmx in this task.
  - **Phones** (`static/style.css`, `@media (max-width: 640px)`):
    - The month grid shows only days that have entries, as a list.
    - People × week sits in an `overflow-x: auto` wrapper with a sticky first column.

- [ ] **Step 1: Write the failing tests:**
  - `TestDeviceWarnings`:
    - Contacts `{"a": now−30s, "b": now−3h}` with allowed `["a", "b", "c"]` give `["b: no contact for 3h", "c: no contact yet"]`.
  - `TestCalendarViews`. The server has a seeded store, a logged-in session cookie, and `now` fixed at 2026-10-12 12:00 local.
    - Seeded data: `HHG0002` and `HHG0004`, both Office and active from `2026-10-01`. HHG0002 taps `08:42`/`17:30` on 2026-10-05, giving 12m late with grace 0. HHG0004 has no taps on 2026-10-05.
    - `GET /calendar?org=<office>&view=month&at=2026-10-05` returns 200 and contains `HHG0002`, `12m late`, `HHG0004` and `absent`.
    - `GET /calendar?org=<office>&view=week&at=2026-10-07` contains `Mon 5` through `Sun 11` and the link `/person?emp=HHG0002&amp;month=2026-10`.
    - `GET /calendar?org=<office>&view=week&at=2026-09-30` contains `Mon 28` and `Sun 4` (Review Focus 5).
    - Without a cookie, `GET /calendar` gives 303.
  - `TestCrossOriginPostRejected`: a POST to `/logout` with header `Sec-Fetch-Site: cross-site` gives 403.
- [ ] **Step 2: Run** `go test -run 'TestDeviceWarnings|TestCalendarViews|TestCrossOrigin' ./...`. Expected: FAIL.
- [ ] **Step 3: Vendor htmx:** `curl -fsSL -o static/htmx.min.js https://unpkg.com/htmx.org@2.0.4/dist/htmx.min.js`. Embed `templates/*.html` and `static/*` with `//go:embed`.
- [ ] **Step 4: Implement** `web.go`, `layout.html`, `calendar.html` and `style.css`. Add template funcs for durations (`8h51m`) and clocks.
- [ ] **Step 5: Run** `go vet ./... && go test ./...`. Expected: PASS.
- [ ] **Step 6: Commit** `feat: organisation calendars with month and week views`.

### Task 8: Person calendar and manager corrections

**Files:**
- Create: `templates/person.html`
- Modify: `web.go`, `web_test.go`

**Interfaces:**
- Consumes: `Report` (one employee), `SaveCorrection`, `currentUser`, `render`.
- Produces:
  - `GET /person?emp=<id>&month=<YYYY-MM>`: an employee picker and previous/next month links.
    - **Totals:** actual and in-shift hours, days with hours, and counts of late, absent and missing-tap days (`Row` fields).
    - **Each day:** shift, check-in → check-out, status, flags, and "actual / in-shift" hours.
    - **Phones:** `@media` turns the grid into a list.
  - `POST /corrections` with form fields `emp`, `day` (`dateFmt`), and optional `in` and `out` (`clockFmt`).
    - A time before `03:00` means the next calendar day.
    - `By` is `currentUser(r)` and `At` is `now()`.
    - Redirects 303 back to `/person?emp=…&month=…`.
    - Unknown employee or invalid values: 400 (Global Constraints).
  - The person page includes a small "Correct a day" form posting to `/corrections`.

- [ ] **Step 1: Write the failing test** `TestPersonPageAndCorrection`.
  - Seed `HHG0012` (Cafe) rostered on shift 3 for 2026-10-04, with taps `14:40` and `2026-10-05 01:00`. Also roster shift 2 on 2026-10-07 with a single tap `10:55`.
  - `GET /person?emp=HHG0012&month=2026-10` contains `10h20m`, `7h00m` and `no check-out`.
  - POST `/corrections` with `emp=HHG0012`, `day=2026-10-07`, `out=20:00` gives 303. A second GET then contains `manager edit` and the 07 Oct hours (`9h05m`), and no longer contains `no check-out`.
  - POST `/corrections` with `emp=NOPE` gives 400.
- [ ] **Step 2: Run** `go test -run TestPersonPageAndCorrection ./...`. Expected: FAIL.
- [ ] **Step 3: Implement** the route, the handler and `person.html`.
- [ ] **Step 4: Run** `go vet ./... && go test ./...`. Expected: PASS.
- [ ] **Step 5: Commit** `feat: per-person month calendar and manager corrections`.

### Task 9: Data entry pages: employees, organisations and shifts, leave, holidays

**Files:**
- Create: `templates/employees.html`, `templates/orgs.html`, `templates/leave.html`, `templates/holidays.html`, `admin_test.go`
- Modify: `web.go`

**Interfaces:**
- Consumes: the `data.go` methods (Task 5) and `render`.
- Produces routes (each POST redirects 303 to its page; invalid input gives 400 and nothing saved):
  - **Employees:**
    - `GET /employees` lists employees with their organisation and active dates.
    - `POST /employees` takes `id`, `name` and `org`. `ActiveFrom` is today's business date. An existing ID gives 400.
    - `POST /employees/{id}/deactivate` sets `ActiveUntil` to today.
  - **Organisations and shifts:**
    - `GET /orgs` lists organisations, with an edit form per org.
    - `POST /orgs` takes `id` (empty means new), `name`, `kind`, `grace`, `start`, `end` and `weekdays`, where `weekdays` is checkboxes 1–7.
    - `POST /orgs/{id}/shifts` takes `shift_id` (empty means new), `name`, `start` and `end`. Roster organisations only.
  - **Leave:**
    - `GET /leave` lists leave.
    - `POST /leave` takes `emp`, `from` and `to`.
    - `POST /leave/{id}/delete`
  - **Holidays:**
    - `GET /holidays` lists holidays.
    - `POST /holidays` takes `day`.
    - `POST /holidays/{day}/delete`

- [ ] **Step 1: Write the failing test** `TestAdminPages`, logged in, with `now` at 2026-10-18.
  - POST `/employees` `id=HHG0020&name=Rina&org=<cafe>` gives 303, and `Employees(cafe)` has HHG0020 with `ActiveFrom=2026-10-18`.
  - Repeating it gives 400.
  - `id=bad id!` gives 400.
  - Deactivating sets `ActiveUntil=2026-10-18`.
  - POST `/leave` with `to` before `from` gives 400.
  - Holiday add then delete leaves `Holidays` empty.
  - POST `/orgs` updating Office's grace to `10` gives `Orgs()[office].GraceMinutes == 10`.
  - POST `/orgs/<cafe>/shifts` adding `4` `08:00`–`14:00` makes `Shifts(cafe)` length 4.
- [ ] **Step 2: Run** `go test -run TestAdminPages ./...`. Expected: FAIL.
- [ ] **Step 3: Implement** the handlers and templates.
- [ ] **Step 4: Run** `go vet ./... && go test ./...`. Expected: PASS.
- [ ] **Step 5: Commit** `feat: pages for employees, organisations, shifts, leave and holidays`.

### Task 10: Weekly roster page

**Files:**
- Create: `templates/roster.html`
- Modify: `web.go`, `admin_test.go`

**Interfaces:**
- Consumes: `SetRoster`, `Roster`, `Shifts` and `Employees` (Task 5); `currentUser`.
- Produces:
  - `GET /roster?org=<roster org id>&week=<dateFmt>`: a grid of active employees × Monday–Sunday, with previous/next week links. The default week is next week, since the manager sets it ahead of time.
    - Each cell is a `<select name="shift">` of off plus the org's shifts. It carries `hx-post="/roster" hx-trigger="change" hx-vals='{"emp":"…","day":"…"}' hx-swap="outerHTML"`.
    - Past and today's cells are editable too.
  - `POST /roster` takes `emp`, `day` and `shift` (shift ID, or `0` for off).
    - Calls `SetRoster(emp, day, shift, currentUser(r), now())`.
    - Responds 200 with the re-rendered cell, which includes its "changed by X at HH:MM" title.
    - A shift not belonging to the employee's organisation gives 400.

- [ ] **Step 1: Write the failing test** `TestRosterPage`, logged in.
  - POST `/roster` `emp=HHG0012&day=2026-10-19&shift=<shift 2 id>` gives 200, and the body contains `selected` on shift 2 and the user's name.
  - `Roster("HHG0012", 10-19, 10-19)["2026-10-19"]` has `Start="11:00"` and `ChangedBy` set to the user.
  - After `SaveShift` changes shift 2 to `12:00`–`21:00`, the stored roster still has `Start="11:00"`.
  - Posting an Office org's shift ID gives 400.
  - `GET /roster?org=<cafe>&week=2026-10-19` contains `HHG0012` and 7 `<select`s for that row.
- [ ] **Step 2: Run** `go test -run TestRosterPage ./...`. Expected: FAIL.
- [ ] **Step 3: Implement** the handlers and `roster.html`.
- [ ] **Step 4: Run** `go vet ./... && go test ./...`. Expected: PASS.
- [ ] **Step 5: Commit** `feat: weekly roster page`.

### Task 11: Wire up `main` and check end to end

**Files:**
- Modify: `main.go`

**Interfaces:**
- Consumes: `OpenStore`, `listenEHome`, `newServer`, `runAddUser`.
- Produces:
  - Startup:
    - `var webAddr = ":8080"`.
    - `main` loads the timezone and opens the store.
    - If `os.Args` is `adduser <name>`, it runs `runAddUser(st, name, os.Stdin)` and exits.
    - Otherwise it starts `listenEHome` in a goroutine, as today, and serves `newServer(st, loc, time.Now)` with `ReadHeaderTimeout: 10 * time.Second`.
  - Either server stopping logs at Error and exits 1.

- [ ] **Step 1: Implement** the wiring.
- [ ] **Step 2: Run** `go vet ./... && go test ./...`. Expected: PASS.
- [ ] **Step 3: Check end to end** with a scratch build. Use a scratch directory and port 17600 so the owner's running server isn't affected.
  - Build with `go build -ldflags "-X main.port=17600 -X main.timezone=Asia/Jakarta" -o <scratch>/hhg .`.
  - In the scratch directory, run `printf 'test-password-1\n' | <scratch>/hhg adduser demo`.
  - Start `<scratch>/hhg`.
  - Send a REGISTER for `gshhgfp` plus one ACS tap for an employee added via `/employees`, using the replay approach from the listener work.
  - Log in at `http://localhost:8080/login`.
  - Expected:
    - The tap appears in the person calendar.
    - The week view shows the row.
    - The device warning disappears after the REGISTER.
- [ ] **Step 4: Commit** `feat: serve the web app alongside the EHome listener`.
