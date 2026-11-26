# Part-time summary: design

Date: 2026-11-26. Status: built.

## Why

Part-time staff, today all at the cafe, are paid per day worked, by the hour. Until now a manager has worked out each
period's pay by hand from a fingerprint export plus a paper recap of missed taps and long breaks. The dashboard
already knows each day's first and last tap and the manager corrections, so it should do that calculation: show every
part-timer's daily hours for the pay period, let a manager fill the gaps and record breaks, and give each person's
pay for the period.

## Goals

1. **Part-time flag.**
   - Each employee has a part-time yes/no flag, editable on the Employees page (add form and per-row edit form).
   - Existing employees start as not part-time.
2. **Pay period.**
   - A period is named by a month and runs from the 26th of the month before through the 25th of that month,
     inclusive. For example, **November 2026 = 26 Oct – 25 Nov 2026**.
   - The page shows exactly one period, never days outside it.
   - **Choosing a period:** a dropdown of months, not previous/next links.
     - It lists every month from the earliest month with a part-timer tap (or, with no taps yet, the current
       calendar month) through the month whose period contains today.
     - Selecting a month reloads the page for that period.
   - **The default period is today's calendar month:**
     - 1–30 Nov opens November (26 Oct – 25 Nov).
     - 1–31 Dec opens December (26 Nov – 25 Dec).

     So on 26–30 Nov the page still opens the November period, which has just closed, for payroll. The new December
     period, already in progress, is in the dropdown.
   - "Today" is today's attendance day (03:00 cutoff), so 01:00 on 1 Dec still counts as 30 Nov.
   - Attendance days follow the existing rule: a day runs 03:00 to 02:59 the next morning.
3. **New page, "Part-time summary".**
   - It is a new item in the sidebar's Overview group, after Events, at `/part-time`.
   - It lists every part-time employee who was active on at least one day of the period, and only the days they were
     active (from `active_from` / `active_until`).
4. **Daily grid.** One row per part-timer, one column per day of the period, one cell per person per day:
   - **`–`:** no taps and no correction that day.
   - **A number of hours:** the day has both a check-in and a check-out.
     - Worked time = (check-out − check-in) minus the part of each of that person's breaks that falls inside
       [check-in, check-out].
     - Hours = worked time rounded **up** to whole hours (8h01m → 9), then capped at **8**.
     - Worked time of zero or less shows `0`.
   - **`?`:** the day has only a check-in or only a check-out.
     - This applies once the attendance day is over (after 03:00 the next morning). Before that the cell shows `…`,
       with the known time on hover, because the person may still be working.
   - Check-in and check-out come from the existing logic: first and last tap after dropping double taps, a lone tap
     classified against the rostered shift, and manager corrections overriding taps. See `checkInOut` and `evaluate` in
     `server/attendance.go`, as used by `Store.Report`.
5. **Detail on hover, or tap on phones.** Every non-empty cell shows its detail:
   - The sessions, e.g. "06:03 → 10:00, 13:00 → 15:45". With no break it is one session.
   - The real worked time and break time, e.g. "worked 6h42m · break 3h".
   - When capped, "(capped at 8)". When a correction was used, "manager edit".
6. **Filling a `?`.**
   - A `?` cell is a button that opens a small form, a modal dialog. The form shows the employee and the day, plus
     check-in and check-out fields pre-filled with the day's current values (see the calls below).
   - Saving stores a normal manager correction (existing `corrections` table, `changed_by`, `changed_at`), so the
     People and Calendar pages show the same fix.
   - Times after midnight belong to the same attendance day, as the existing correction form already handles
     (`attendanceTime`). The same validation applies: check-out after check-in, and the day within the employee's
     active period.
   - On success the page reloads at the same period.
7. **Breaks.**
   - An "Add break" button opens a modal form with:
     - **Employee:** a dropdown of part-time employees only.
     - **Break start:** date and time.
     - **Break end:** date and time.
   - Validation:
     - The employee must exist and be part-time.
     - The end must be after the start.
     - The break may not span more than one attendance day.
   - A break belongs to the attendance day of its start.
   - Below the grid, a list of the period's breaks shows employee, day, start, end, who added it and a Delete button.
     Deleting asks for confirmation, like the existing deactivate button.
   - A break only reduces worked time where it overlaps that day's check-in → check-out.
8. **Pay.**
   - One daily wage applies to all part-timers. Its default is Rp 130.000, and a manager edits it on the Part-time
     summary page.
   - Day's pay = wage × hours ÷ 8, where hours is the capped value from goal 4 (an 8-hour day earns the full wage).
     Round to whole rupiah.
   - A `?` day or `–` day earns nothing.
9. **Monthly summary.** A table above the grid with one row per part-timer and a grand-total row:
   - Days worked: days with hours > 0.
   - Total hours: the sum of the capped daily hours.
   - Daily wage.
   - Total pay.
   - Rows with any `?` or `…` day are marked `+?`, so the pay isn't mistaken for final.
   - Amounts are formatted `Rp 3.445.000`, with dot thousands separators.

## Non-goals

- The Calendar and People pages, and lateness rules, are unchanged. They ignore breaks and the part-time flag.
- No per-employee wage, no wage history per period, no overtime, no allowances, no export.
- Full-time staff get no breaks and no pay calculation.

## Anti-goals (failures even if it works)

- **Inconsistent fixes:** filling a `?` on this page produces a different result than the same correction made on the
  People page.
- **Silently wrong pay:** a period total looks final while a day is still `?`.
- **Breaks that touch the wrong people:** a break changes anyone's hours other than its own employee's on its own
  attendance day, or changes hours on Calendar or People.

## Constraints

- Go, server-rendered `html/template` and htmx as in the rest of `server/`. Standard library first, no new
  dependencies.
- The same SQL runs on MySQL in production and on SQLite in development. Schema changes go in both
  `schema_mysql.sql` and `schema_sqlite.sql`. Existing production tables gain columns and tables in place, as
  `addEventHash` in `server/store.go` does.
- The dashboard's existing style (`server/static/style.css`): cards, tables, the phone layout, and dark mode. The grid
  scrolls horizontally on a phone; the summary table must fit.
- Manager actions require the existing login. Forms are protected by the existing cross-origin protection.

## Approaches

1. **New breaks table plus existing corrections (chosen).** Breaks get their own table. Missing times are saved as
   normal corrections. Only the new page subtracts breaks. It's small, and the other pages are untouched.
2. **Replace corrections with a list of sessions per day.** It's flexible, but it changes how Calendar, People and
   lateness are calculated. That's a bigger, riskier change for the same result.
3. **Save breaks as extra taps.** This mixes manual entries into device data and breaks the first/last and
   double-tap rules.

The user chose 1.

## Decided by the user

- Hours round up per day, then cap at 8. The period total is the sum of the daily values.
- Pay = daily wage × capped hours ÷ 8. One wage for all part-timers, default 130.000.
- Breaks are stored separately (approach 1), and only for part-time employees.
- Someone who starts or leaves mid-period shows only their active days.
- A `?` means only a check-in or only a check-out. It has a button to fill in the missing time.
- The pay period runs from the 26th through the 25th.

## Calls made in the design (not discussed)

- Missing times are saved as normal corrections, so they appear on People and Calendar too.
- A `?` appears only once the attendance day is over.
- **The wage is a single stored setting.** Changing it changes the pay shown for every period, past ones included.
  There is no history.
- A break belongs to the attendance day of its start, and may not cross into the next attendance day.
- A day's pay is rounded to whole rupiah, and the total is the sum of the rounded days.
- **Seconds:** worked time is rounded to the nearest whole minute before rounding up to hours, as the spreadsheet did.
  8h00m20s is 8h00m, giving 8 hours, not 9.
- **Unfinished days count as `+?` too.** A day that is still in progress (only one time, before 03:00 the next
  morning) also marks the row `+?`, and the grid shows it as `…` with the known time on hover. The current period's
  total is therefore never mistaken for final.
- **The fill-in dialog shows both check-in and check-out, pre-filled with the day's current values, all editable.**
  A lone tap on a day with no rostered shift is read as a check-in (existing rule). If it was really a check-out, the
  manager moves it to the right field.
  - Saving goes through the same validation and save path as the People page's correction form. That logic is
    refactored into a shared function, so both pages can't disagree.
  - Errors show inside the dialog.
- **Overlapping breaks are merged:** the union of a day's break intervals is subtracted once, so a duplicate entry
  can't double-subtract.
- **Break validation also requires the day to fall within the employee's active period,** like corrections. A break
  on a day with no taps is allowed but has no effect until there are taps.
- **The part-time flag is a plain column with no history,** like the wage. Unflagging someone removes them from every
  period's summary and break list, past ones included, and re-flagging brings it all back. Their breaks are not
  deleted.

## Left to the builder

- Table and column names, and the storage for the wage setting: e.g. a small key/value `settings` table, or a
  one-row table.
- Route names beyond `/part-time`, and the query parameter for the period.
- How the modal dialogs are built: a native `<dialog>` with htmx, matching the existing pages.
- How the grid's hover and tap detail is built. It must work on touch screens, where `title` alone does not.
- Whether the page reuses `Store.Report` per part-timer, or a narrower query that builds the same `Day` values for
  `evaluate`.
