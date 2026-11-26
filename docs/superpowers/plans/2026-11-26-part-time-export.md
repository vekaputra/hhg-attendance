# Part-time Export Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** An Export button on the Part-time page that downloads the shown pay period as a three-sheet .xlsx.

**Architecture:**
- A small dependency-free .xlsx writer (`server/xlsx.go`): zip plus XML, with inline-string and number cells.
- A function that turns the page's existing `partTimePage` data into the three sheets.
- A `GET /part-time/export` handler that writes the result.

**Tech Stack:** Go standard library (`archive/zip`, `encoding/xml`, `strconv`).

**Spec:** `docs/superpowers/specs/2026-11-26-part-time-export-design.md`

## Global Constraints

- **No new dependencies.**
- **Sheet names:** exactly "Summary", "Daily hours", "Check-in & out".
- **File name:** `Part-time <January 2006>.xlsx`, e.g. `Part-time November 2026.xlsx`.
- **Response headers:** Content-Type `application/vnd.openxmlformats-officedocument.spreadsheetml.sheet`, and
  `Content-Disposition: attachment; filename="..."`.
- **Cell types:** Days, Hours, Daily wage and Pay are number cells. Dates, times, durations and notes are text.
- **The numbers come from the page data** (`partTimeData`), so they always match the page.
- **Commits:** stage files by name; never stage `relay/README.md`, `relay/fakedevice/` or the `.xlsx` in the repo
  root. Every commit message ends with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.

## Review Focus

- **A name with `&`, `<` or `"` (e.g. "B&B CAFE"):** the file must still open. XML-escape every text cell (Task 1
  test).
- **A grid wider than 26 columns:** column letters beyond Z (`AA`, `AB` …) must be correct, since a 31-day period
  plus 3 more columns needs them (Task 1 test).
- **A day whose breaks cover the whole span:** it has no sessions. The Breaks column must still show the break (Task 2
  test).

---

### Task 1: The .xlsx writer

**Files:**
- Create: `server/xlsx.go`, `server/xlsx_test.go`

**Interfaces:**
- Produces:

```go
// xlsxSheet is one worksheet: Rows[r][c] is a string (inline text), an int/int64 (number), or nil (empty).
type xlsxSheet struct {
	Name string
	Rows [][]any
}
func writeXLSX(w io.Writer, sheets []xlsxSheet) error
func xlsxColumn(i int) string // 0 → "A", 25 → "Z", 26 → "AA", 27 → "AB"
```

- **Zip parts:**
  - `[Content_Types].xml`, `_rels/.rels`, `xl/workbook.xml` and `xl/_rels/workbook.xml.rels`.
  - `xl/worksheets/sheetN.xml` for each sheet, numbered from 1.
- **Cells:** each cell carries an `r="B3"` reference. Text is `t="inlineStr"><is><t xml:space="preserve">…</t></is>`;
  numbers are `<v>…</v>`.
- **Escaping:** text is escaped with `xml.EscapeText`. Sheet names in `workbook.xml` are escaped too, because
  "Check-in & out" contains `&`.

- [ ] **Step 1: Write the failing tests**
  - `TestXlsxColumn`: 0→A, 25→Z, 26→AA, 27→AB, 52→BA.
  - `TestWriteXLSX`: write two sheets. Sheet 1 is "Check-in & out" with rows
    `{{"ID","Name"},{"P1","B&B <x>"},{nil, int64(97500)}}`; sheet 2 has 30 columns in its first row. Then reopen the
    zip and check:
    - all six parts exist;
    - every part parses as XML with `encoding/xml` (decode tokens until EOF, no error);
    - `workbook.xml` lists both sheet names, the first escaped as `Check-in &amp; out`;
    - sheet 1 contains `r="B2"` with text `B&amp;B &lt;x&gt;` and `<c r="B3"><v>97500</v></c>`, and has no cell for
      the nil;
    - sheet 2 contains a cell `r="AD1"`.

- [ ] **Step 2: Run them** with `go test ./server/ -run 'Xlsx|WriteXLSX'`. Expected: FAIL (undefined).

- [ ] **Step 3: Implement** `server/xlsx.go`, using `archive/zip` and `bufio`-free direct writes.

- [ ] **Step 4: Run them again** with `go test ./server/ -run 'Xlsx|WriteXLSX' && go vet ./...`. Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add server/xlsx.go server/xlsx_test.go
git commit -m "feat: minimal dependency-free xlsx writer"
```

---

### Task 2: Export route, sheets and button

**Files:**
- Modify: `server/parttime_page.go` (the handler and `partTimeWorkbook`), `server/web.go` (route),
  `server/templates/parttime.html` (button)
- Test: `server/parttime_page_test.go`

**Interfaces:**
- Consumes: `writeXLSX`, `xlsxSheet` (Task 1); `partTimeData`, `payMonthParam`, `partTimePage`, `ptRow`, `ptCell`,
  `ptDay`, `ptState`, `hours`, `clock`, `maxPaidHours` (existing).
- Produces:
  - `func partTimeWorkbook(p partTimePage) []xlsxSheet`.
  - `GET /part-time/export?month=YYYY-MM`, registered next to `GET /part-time`.

**Sheet contents:**

- **Summary**
  - Header row: `ID, Name, Days, Hours, Daily wage, Pay, Final`.
  - One row per `p.Rows`, then a `Total` row (ID column "Total", Name blank) from `p.Total`.
  - Final is "No" when `Unsure`, else "Yes".
- **Daily hours**
  - Header row: `ID, Name`, then each `p.Days` as "2 Jan", then `Total hours`.
  - In each row:
    - inactive cell → nil;
    - `ptHours` → `int64(Hours)`;
    - `ptMissing` → "?";
    - `ptOpen` → "…";
    - `ptNone` → nil.
- **Check-in & out**
  - Header row: `Date, ID, Name, Check-in, Check-out, Breaks, Break time, Worked, Hours, Pay, Note`.
  - Rows: for each date in order, then each employee in `p.Rows` order, every active cell whose state is not
    `ptNone`.
  - **Check-in / Check-out:** `clock(...)`, or "" when zero.
  - **Breaks:** the gaps between consecutive `Sessions`, written `"10:00–13:00"` and joined with `", "`. When there are
    no sessions but `Break > 0`, write `In–Out`.
  - **Break time:** `hours(Break)` or "".
  - **Worked:** `hours(Worked)`, only for `ptHours`.
  - **Hours and Pay:** numbers, only for `ptHours`.
  - **Note:** the parts that apply, joined with `", "`: "manager edit" when `Edited`; "missing check-in" or
    "missing check-out" for `ptMissing`; "not over yet" for `ptOpen`; "capped at 8" when `Capped`.
- **Button:** an `<a class="button" href="/part-time/export?month={{.Month}}">Export</a>` (use the existing button or
  link styling) inside the `tools` block next to Show.

- [ ] **Step 1: Write the failing tests** in `parttime_page_test.go`, with the existing `partTimeFixture` and a small
  helper that unzips the response and returns each sheet's XML.
  - `TestPartTimeExport`: GET `/part-time/export?month=2026-11` → 200, with the Content-Type above and
    `Content-Disposition` containing `Part-time November 2026.xlsx`.
    - **Summary:** PT1's row has `<v>14</v>` and `<v>227500</v>` and the text `No`.
    - **Daily hours:** PT1's row has 26 Oct = `<v>6</v>` and 28 Oct = text `?`.
    - **Check-in & out:** a PT1 26 Oct row with texts `06:00`, `15:00`, `10:00–13:00`, `3h00m`, `6h00m` and numbers
      `<v>6</v>`, `<v>97500</v>`. No row exists for PT1's `–` day 30 Oct.
  - `TestPartTimeExportBadMonth`: `?month=bad` → 400.
  - `TestPartTimeWorkbookWholeDayBreak`: a `partTimePage` built by hand with one `ptHours` day, `In` 09:00, `Out`
    10:00, no sessions and `Break` 1h. Its Check-in & out row has Breaks `09:00–10:00` and Hours 0.
  - The page has an Export link with `href="/part-time/export?month=2026-11"`.

- [ ] **Step 2: Run them** with `go test ./server/ -run 'PartTimeExport|PartTimeWorkbook'`. Expected: FAIL.

- [ ] **Step 3: Implement** `partTimeWorkbook`, the handler (`payMonthParam` → `partTimeData(r, month,
  srv.today())` → headers → `writeXLSX`), the route and the button.

- [ ] **Step 4: Verify** with `go vet ./... && go test ./...`. Expected: PASS. Then open an exported file once in a
  real spreadsheet app (LibreOffice, Numbers or Google Sheets) and confirm it opens without a repair prompt, with the
  three sheets.

- [ ] **Step 5: Commit**

```bash
git add server/parttime_page.go server/parttime_page_test.go server/web.go server/templates/parttime.html
git commit -m "feat: export the part-time period to Excel"
```
