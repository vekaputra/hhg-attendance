# Part-time export to Excel: design

Date: 2026-11-26. Status: approved.

## Why

Managers pay part-timers from the Part-time page. They need the period's numbers in a spreadsheet to share, file or
adjust, instead of copying from the screen.

## What

- **The button:** an **Export** button on the Part-time page, next to the Period dropdown. It downloads the shown pay
  period as `Part-time <Month> <Year>.xlsx`, e.g. `Part-time November 2026.xlsx`.
- **Sheet 1, "Summary":** one row per part-timer shown on the page, then a "Total" row.
  - Columns: ID, Name, Days, Hours, Daily wage, Pay, Final.
  - Final is "No" when the page marks the row `+?`, else "Yes".
- **Sheet 2, "Daily hours":** one row per part-timer, then a total column.
  - Columns: ID, Name, then one per period date (header "26 Oct" … "25 Nov"), then Total hours.
  - Each date cell holds the capped hours as a number, or the text `?` or `…`. It is blank for no taps and for days
    outside the employee's active period.
- **Sheet 3, "Check-in & out":** one row per part-timer per active day that has a check-in or a check-out, ordered by
  date, then employee.
  - Columns: Date, ID, Name, Check-in, Check-out, Breaks, Break time, Worked, Hours, Pay, Note.
  - Breaks are written like `10:00–13:00`, with several joined by `, `. Break time and Worked use the page's format
    (`3h00m`).
  - Note lists "manager edit", "missing check-in", "missing check-out", "not over yet" and "capped at 8", as they
    apply.
- **Number cells:** Days, Hours, Daily wage and Pay are numeric cells, so they can be summed in Excel. Everything else
  is text.
- **The numbers match the page exactly.** The export uses the same per-day results and totals the page renders.

## Approaches

- **Standard library (chosen by the user).** An .xlsx file is a zip of XML parts. Write the minimum parts by hand:
  `[Content_Types].xml`, `_rels/.rels`, `xl/workbook.xml`, `xl/_rels/workbook.xml.rels`, and one worksheet per sheet.
  Use inline strings, so no shared-strings part is needed. This uses `archive/zip`, `encoding/xml` escaping and
  `strconv`, with no new dependency.
- **excelize library (rejected):** less code, but a large dependency for three plain tables.

## Constraints

- **Route:** `GET /part-time/export?month=YYYY-MM`, behind the existing login. A bad month gives 400, as on the page.
- **Response headers:** Content-Type `application/vnd.openxmlformats-officedocument.spreadsheetml.sheet` and
  `Content-Disposition: attachment` with the file name.
- **Compatibility:** the file must open without a repair prompt in Excel, Google Sheets and LibreOffice.
- **No styling** beyond what's needed.

## Calls made (not discussed)

- **Sheet 3 omits days with no taps (`–`).** It does list `?` and `…` days, with the missing time left blank.
- **Dates and times are text** ("26 Oct", "06:03"), not Excel date serials. This keeps them unambiguous, with no
  time-zone or format surprises.
