# Attendance system for office and cafe: design

Date: 2026-10-18. Status: draft for review.

## Why

Managers need to see, after the fact, who was late or absent in the office
and in the cafe, with holidays and leave excluded. Part-time cafe staff are paid
by hours worked, so managers also need each person's approximate hours.

Fingerprint devices (Hikvision DS-K1A8503MF, EHome 4.0) already push every tap
to the listener in this repo (`ehome.go`). Today it only writes the taps to a
log. This project turns those taps into an attendance record that managers can
read in a calendar.

## Who uses it

Managers, on phones and laptops, from anywhere over the internet. The owner
creates their accounts.

## Goals

1. **Store every tap** the listener receives. Store at least the employee number,
   the device's time, the device ID and the device's `serialNo`. Store it before
   the device gets its 200 ack.
2. **A calendar per organisation** (Office, Cafe), with two switchable views:
   - **Month grid**: one month at a time, moving by month. Each day lists only
     the people who were late, absent or flagged, for example
     "HHG0012 Gita · shift 2 · 8m late".
   - **People × days**: one week (Monday to Sunday) at a time, moving by week.
     It has one row per employee in the organisation and a cell for every day.
     Each cell shows status (on time / minutes late / absent / leave / holiday /
     off / flags), the rostered shift for roster organisations, and both hour
     figures (below). Each row ends with weekly totals of both hour figures.
   - Mockups: `.superpowers/brainstorm/56725-1792298000/content/` (`calendar-both-cafe.html`,
     `people-week-v2.html`).
3. **A calendar per person**, for working out part-time pay. Pick an employee to
   see one month at a time, moving by month. Each day shows status and flags,
   the shift, check-in and check-out times, and both hour figures. The month
   shows totals of both figures, plus counts of days late, absent and flagged.
   Part-timers are paid per calendar month, so the month is the period. You can
   reach it by clicking a name in the other two views.
4. **Hand-entered data**, with a page for each:
   - **Employees**: ID, name, organisation. The ID must equal the device's
     `employeeNoString`, for example `HHG0001`. Name and organisation can be
     edited later. An employee can be deactivated, after a confirmation, and
     reactivated.
   - **Organisations** and their settings (below). More can be added later,
     for example a second cafe.
   - **Shifts** for each roster organisation: add new ones and change times.
   - **Weekly roster**: the cafe manager sets the coming week ahead of time,
     giving each person, for each day, a shift or off. Today's and past days can
     be edited too, for example to record a swapped shift.
   - **Check-in/out corrections**: a manager can set a person-day's check-in,
     check-out or both by hand. The day is then flagged **manager edit** (see
     Rules).
   - **Leave**: a person and a date range.
   - **Holidays**: a date only, with no name.
5. **Manager login**.
6. **Works on phones**. On a phone, the month grid becomes a list of only the
   days that have problems. People × days keeps the names fixed and scrolls the
   days sideways. The person calendar becomes a list of days. *(My call.)*

## Non-goals (for now)

- Working out pay. The system gives hours; pay is calculated elsewhere.
- Deducting breaks, detecting early leave, overtime rules.
- A full change history of rosters. Each roster day records only who last
  changed it and when. *(My call.)*
- An approval workflow for leave, or leave types.
- Per-manager permissions: every manager can see and edit everything. *(My call.)*
- Self sign-up, password reset by email.
- Syncing employees to or from the devices.
- Clicking a cell to see its individual taps.
- Hosting, HTTPS and the reverse proxy. The owner runs this on an Oracle VPS.

## Anti-goals: failure even if it "works"

- Wrong attendance shown with confidence. Examples: someone on leave shown as
  absent, or past weeks quietly changing after a shift's times are edited.
- Taps lost: the device was acked but the tap was never stored.
- Hours, rosters or names visible without login, or fake taps accepted from
  the internet and counted towards pay.

## Organisations

Each organisation has:

- **Name**
- **Type**: `fixed` (fixed hours) or `roster` (weekly roster)
- **Grace minutes**: how late someone can be and still count as on time
- For `fixed`: start time, end time, working weekdays
- For `roster`: a list of shifts (name, start, end), which can be edited

The two organisations to start with:

| Organisation | Type | Schedule | Notes |
|---|---|---|---|
| Office | fixed | 08:30–17:30 | Working days Monday–Friday *(my guess, confirm)* |
| Cafe | roster | 1: 06:00–15:00; 2: 11:00–20:00; 3: 15:00–22:00 | Open 7 days, follows its roster |

Each employee belongs to exactly one organisation. The employee's
organisation decides which schedule applies, not which device they tapped.

## Rules

**Attendance day.** A day runs from 03:00 to 02:59 the next morning, for every
organisation. A tap at 01:00 on Tuesday belongs to Monday, which covers cafe
staff cleaning after the 22:00 close.

**Tap time.** Use the device's `Time` field, which is local time, not the time
the server received the tap. This keeps resent or delayed events on the right
day. All times are in one business timezone, set in config.

**Check-in and check-out.** The first tap of the attendance day is check-in and
the last is check-out. The device's status (checkIn, checkOut, breakOut and so
on) is ignored.

**One tap only.** If the tap is before the middle of the person's scheduled
window, it is a check-in and the day is flagged **no check-out**. Otherwise it
is a check-out and the day is flagged **no check-in**. Examples: the office
middle is 13:00; shift 3's middle is 18:30. On a day with no schedule (an
unrostered tap, below), a single tap counts as a check-in, flagged no check-out.
*(My call.)*

**Scheduled to work.**
- Office: a working weekday that isn't a holiday.
- Cafe: rostered to a shift. Holidays don't apply to roster organisations.
- Nobody is scheduled on a day they are on leave.

**Late.** Check-in is after the scheduled start plus the organisation's grace
minutes. Minutes late are measured from the scheduled start: with 10 grace
minutes, 08:40 is on time and 08:41 is "11m late". *(My guess, confirm.)* A day
flagged no check-in is not judged late.

**Absent.** Scheduled to work, no taps, and not on leave. A day only shows
absent once its scheduled end has passed. Before that, the person has no
status yet, so a shift 3 person is not "absent" at 10:00. *(My call.)*

**Leave.** A leave day is never late or absent. If the person taps anyway, the
hours still show and the status shows leave. *(My call.)*

**Holiday.** Applies to `fixed` organisations only. Nobody is absent. Any taps
are still recorded and their hours shown.

**Unrostered tap.** In a roster organisation, a day with taps but no shift is
flagged **unrostered**, for example someone covering a swapped shift. It shows
actual hours only.

**Hours.** Each person-day with both a check-in and a check-out gets two
figures:
- **Actual**: check-out minus check-in.
- **Within shift**: only the part of check-in to check-out that falls inside the
  scheduled window (the office hours, or the rostered shift).

Example, shift 3 (15:00–22:00), taps 14:40 and 01:00: actual is 10h 20m, within
shift is 7h. A flagged day (missing tap) shows no hours. Weekly totals add up
both figures over the days that have them.

**Editing shifts.** When someone is rostered, the roster entry stores that
shift's start and end times. Editing or adding shifts later only affects
rosters made afterwards, so past weeks never change.

**Manager corrections.** A manager can add a correction for one person-day,
setting check-in, check-out or both. A field set by the correction replaces the
value from the taps, and a field left empty keeps the tap-derived value. Late,
hours and the missing-tap flags are then worked out from the corrected values.
The day is flagged **manager edit**, which shows in every view. The original
taps are kept unchanged. A correction records which manager made it and when.
Corrections are not stored as taps, so the dedup and double-tap rules don't
apply to them. *(Mechanics are my call; the flag is the owner's.)*

**Device contact warning.** The listener records each device's last contact
time from any packet (REGISTER, KEEPALIVE or ACS). Keepalives arrive every 15
seconds. If a device in `allowedDeviceIDs` hasn't been heard from for more than
2 minutes, the calendar pages show a warning such as "gshhgfp: no contact for
3h". Absences on those days may only be taps the device hasn't sent yet. The
warning doesn't link devices to organisations; it just names the device.
*(The warning is the owner's choice; the 2-minute threshold is my call.)*

**Editing other settings.** These apply to every day, past days included:
organisation settings (office start and end, working weekdays, grace minutes)
and an employee's organisation. They rarely change, and keeping no history of
them keeps the queries simple. Holidays and leave are also retroactive on
purpose, because entering them is how managers correct the record. *(My call.)*

**Employee active period.** An employee counts from the day they are added to
the system until the day they are deactivated. Deactivating keeps their history
instead of deleting it. Nobody is late or absent outside that period, so adding
someone today doesn't mark them absent for every past weekday. Their taps from
before they were added still show with hours. There are no extra form fields:
the dates come from adding and deactivating. *(My call.)*

**Double taps.** A tap within 2 minutes of the same person's previous tap is
stored but ignored for check-in and check-out. Otherwise an accidental double
tap on arrival would look like a 25-second working day with no flag.
*(My call.)*

**Duplicate events.** The device resends events it thinks weren't delivered.
A tap is stored once per (device ID, `serialNo`, device time). Time is part of
the key so that a counter reset after a reboot can't silently drop real taps.
*(My call.)*

## Constraints

- **Language and shape:** Go, extending this repo as one program. The EHome
  listener in `ehome.go` stays as it is: registration gate, NUL trimming, 403
  for unregistered senders, and `event` lines for non-tap ACS events. It now
  also stores taps and records each device's last contact time.
- **UI:** htmx (the owner's choice), with server-rendered `html/template`.
  The base stylesheet is mvp.css (the owner's choice), a classless CSS file
  vendored in `static/`. The app follows the OS dark-mode preference.
- **Storage:** SQLite for development. Production will move to MySQL later
  (the owner's decision), so keep every SQL statement inside the store's
  methods. Avoid SQLite-only SQL functions such as `date()` and `strftime()`,
  and do date maths in Go. The driver is a pure-Go one, `modernc.org/sqlite`.
- **Devices over the internet.** Devices reach the VPS over the internet with
  EHome 4.0 on UDP 7600. EHome 4.0 is unencrypted and has no usable device
  password, so the only gate in code is `allowedDeviceIDs` plus the registered
  source address.
  - The owner should limit UDP 7600 in Oracle's firewall to the office's and
    cafe's public IPs.
  - `alarmIP` must be set to the VPS public IP. Oracle instances are behind NAT,
    so automatic detection would give the device a private IP.
- **Login security** (the site is public):
  - Passwords are hashed with a slow hash; stdlib `crypto/pbkdf2` is enough.
  - Repeated wrong passwords are limited.
  - Session cookies are `HttpOnly`, `Secure` and `SameSite`.
  - Every form is protected against cross-site requests (stdlib
    `http.CrossOriginProtection`).
  - No page except login is served without a session.
  - Accounts are created by the owner, for example from a command line.
- **Logging:** keep the existing `ehome.log` file logging.

## Left to the builder

- Database schema, migrations, routes, template layout, htmx patterns.
- How the roster page works. My guess is a week grid of people × days with a
  shift picker per cell, and "copy last week" as a time-saver if cheap.
- How the owner creates manager accounts.
- Exact colours and styling, within the mockups' look.

## My calls (please check)

1. Office working days are Monday to Friday.
2. Grace: minutes late are counted from the scheduled start, not from the end of
   the grace period.
3. Every manager sees and edits everything; there are no per-organisation
   permissions.
4. A day only shows absent after its scheduled end has passed.
5. Someone on leave who taps anyway shows hours, with status leave.
6. A single tap on an unrostered day counts as a check-in.
7. The 03:00 day boundary applies to every organisation, not just the cafe.
8. Cafe staff work at most one shift per day.
9. SQLite in a single Go program, with phone layouts as described.
10. Organisation settings, an employee's organisation, holidays and leave all
    apply retroactively. Only shift times are snapshotted, in the roster.
11. An employee's active period runs from when they are added until they are
    deactivated, with no extra form fields.
12. Taps within 2 minutes of the previous one count as one.
13. Taps are deduplicated on (device ID, serialNo, device time).
14. Corrections: a field left empty keeps the tap value, original taps are kept,
    and the correction records the manager and time.
15. The device warning appears after 2 minutes without contact.
16. Rosters record only who last changed each day, not a full history.
