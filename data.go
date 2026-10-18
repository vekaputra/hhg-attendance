package main

import (
	"fmt"
	"time"
)

// Day arguments are calendar dates, formatted as given (not converted to the store's location);
// timestamps are converted to it. Optional columns are NULL when empty (NULLIF in, COALESCE out).

// SaveOrg inserts the organisation when o.ID is 0, otherwise updates it, and returns its ID.
func (s *Store) SaveOrg(o Org) (int64, error) {
	if o.ID == 0 {
		res, err := s.db.Exec(`INSERT INTO orgs (name, kind, grace_minutes, start_time, end_time, weekdays) VALUES (?, ?, ?, ?, ?, ?)`,
			o.Name, o.Kind, o.GraceMinutes, o.Start, o.End, o.Weekdays)
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}
	_, err := s.db.Exec(`UPDATE orgs SET name = ?, kind = ?, grace_minutes = ?, start_time = ?, end_time = ?, weekdays = ? WHERE id = ?`,
		o.Name, o.Kind, o.GraceMinutes, o.Start, o.End, o.Weekdays, o.ID)
	return o.ID, err
}

// SaveShift inserts the shift when sh.ID is 0, otherwise updates it, and returns its ID.
// Roster rows keep the times they were set with, so an update doesn't rewrite past days.
func (s *Store) SaveShift(sh Shift) (int64, error) {
	if sh.ID == 0 {
		res, err := s.db.Exec(`INSERT INTO shifts (org_id, name, start_time, end_time) VALUES (?, ?, ?, ?)`,
			sh.OrgID, sh.Name, sh.Start, sh.End)
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}
	_, err := s.db.Exec(`UPDATE shifts SET org_id = ?, name = ?, start_time = ?, end_time = ? WHERE id = ?`,
		sh.OrgID, sh.Name, sh.Start, sh.End, sh.ID)
	return sh.ID, err
}

// Employee dates are dateFmt; ActiveUntil is empty while active and inclusive once set.
type Employee struct {
	ID, Name                string
	OrgID                   int64
	ActiveFrom, ActiveUntil string
}

func (s *Store) AddEmployee(e Employee) error {
	_, err := s.db.Exec(`INSERT INTO employees (id, name, org_id, active_from, active_until) VALUES (?, ?, ?, ?, NULLIF(?, ''))`,
		e.ID, e.Name, e.OrgID, e.ActiveFrom, e.ActiveUntil)
	return err
}

// DeactivateEmployee makes day the employee's last active day.
func (s *Store) DeactivateEmployee(id string, day time.Time) error {
	_, err := s.db.Exec(`UPDATE employees SET active_until = ? WHERE id = ?`, day.Format(dateFmt), id)
	return err
}

// UpdateEmployee changes the employee's name and organisation. The organisation applies to all of their days.
func (s *Store) UpdateEmployee(id, name string, orgID int64) error {
	_, err := s.db.Exec(`UPDATE employees SET name = ?, org_id = ? WHERE id = ?`, name, orgID, id)
	return err
}

// ReactivateEmployee clears the employee's last active day.
func (s *Store) ReactivateEmployee(id string) error {
	_, err := s.db.Exec(`UPDATE employees SET active_until = NULL WHERE id = ?`, id)
	return err
}

// Employees returns the organisation's employees (all when orgID is 0) ordered by ID.
func (s *Store) Employees(orgID int64) ([]Employee, error) {
	rows, err := s.db.Query(`SELECT id, name, org_id, active_from, COALESCE(active_until, '') FROM employees
		WHERE ? = 0 OR org_id = ? ORDER BY id`, orgID, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var emps []Employee
	for rows.Next() {
		var e Employee
		if err := rows.Scan(&e.ID, &e.Name, &e.OrgID, &e.ActiveFrom, &e.ActiveUntil); err != nil {
			return nil, err
		}
		emps = append(emps, e)
	}
	return emps, rows.Err()
}

// SetRoster schedules the employee on a shift for a day, or off when shiftID is 0.
// The shift's name and times are copied, so later edits to the shift don't change this day.
func (s *Store) SetRoster(employeeID string, day time.Time, shiftID int64, by string, at time.Time) error {
	var name, start, end string // empty means off
	if shiftID != 0 {
		err := s.db.QueryRow(`SELECT name, start_time, end_time FROM shifts WHERE id = ?`, shiftID).Scan(&name, &start, &end)
		if err != nil {
			return fmt.Errorf("shift %d: %w", shiftID, err)
		}
	}
	d, changed := day.Format(dateFmt), at.In(s.loc).Format(stampFmt)
	_, err := s.db.Exec(`INSERT INTO roster (employee_id, day, shift_name, start_time, end_time, changed_by, changed_at)
		VALUES (?, ?, NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), ?, ?)`,
		employeeID, d, name, start, end, by, changed)
	if isDuplicate(err) {
		_, err = s.db.Exec(`UPDATE roster SET shift_name = NULLIF(?, ''), start_time = NULLIF(?, ''), end_time = NULLIF(?, ''),
			changed_by = ?, changed_at = ? WHERE employee_id = ? AND day = ?`, name, start, end, by, changed, employeeID, d)
	}
	return err
}

// RosterDay is one scheduled day. Shift, Start and End are empty when the employee is off.
type RosterDay struct{ Shift, Start, End, ChangedBy, ChangedAt string }

// Roster returns the employee's roster rows from through to inclusive, keyed by dateFmt.
func (s *Store) Roster(employeeID string, from, to time.Time) (map[string]RosterDay, error) {
	rows, err := s.db.Query(`SELECT day, COALESCE(shift_name, ''), COALESCE(start_time, ''), COALESCE(end_time, ''), changed_by, changed_at
		FROM roster WHERE employee_id = ? AND day >= ? AND day <= ?`, employeeID, from.Format(dateFmt), to.Format(dateFmt))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	roster := map[string]RosterDay{}
	for rows.Next() {
		var day string
		var r RosterDay
		if err := rows.Scan(&day, &r.Shift, &r.Start, &r.End, &r.ChangedBy, &r.ChangedAt); err != nil {
			return nil, err
		}
		roster[day] = r
	}
	return roster, rows.Err()
}

// Leave is a run of days, From and To inclusive (dateFmt).
type Leave struct {
	ID         int64
	EmployeeID string
	From, To   string
}

func (s *Store) AddLeave(employeeID string, from, to time.Time) error {
	if to.Format(dateFmt) < from.Format(dateFmt) {
		return fmt.Errorf("leave ends %s, before it starts %s", to.Format(dateFmt), from.Format(dateFmt))
	}
	_, err := s.db.Exec(`INSERT INTO leaves (employee_id, from_day, to_day) VALUES (?, ?, ?)`,
		employeeID, from.Format(dateFmt), to.Format(dateFmt))
	return err
}

func (s *Store) DeleteLeave(id int64) error {
	_, err := s.db.Exec(`DELETE FROM leaves WHERE id = ?`, id)
	return err
}

// Leaves returns the leave overlapping from..to inclusive, ordered by start.
func (s *Store) Leaves(from, to time.Time) ([]Leave, error) {
	rows, err := s.db.Query(`SELECT id, employee_id, from_day, to_day FROM leaves WHERE from_day <= ? AND to_day >= ? ORDER BY from_day, id`,
		to.Format(dateFmt), from.Format(dateFmt))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var leaves []Leave
	for rows.Next() {
		var l Leave
		if err := rows.Scan(&l.ID, &l.EmployeeID, &l.From, &l.To); err != nil {
			return nil, err
		}
		leaves = append(leaves, l)
	}
	return leaves, rows.Err()
}

func (s *Store) AddHoliday(day time.Time) error {
	_, err := s.db.Exec(`INSERT INTO holidays (day) VALUES (?)`, day.Format(dateFmt))
	if isDuplicate(err) {
		return nil
	}
	return err
}

func (s *Store) DeleteHoliday(day time.Time) error {
	_, err := s.db.Exec(`DELETE FROM holidays WHERE day = ?`, day.Format(dateFmt))
	return err
}

// Holidays returns the holidays from through to inclusive, keyed by dateFmt.
func (s *Store) Holidays(from, to time.Time) (map[string]bool, error) {
	rows, err := s.db.Query(`SELECT day FROM holidays WHERE day >= ? AND day <= ?`, from.Format(dateFmt), to.Format(dateFmt))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	holidays := map[string]bool{}
	for rows.Next() {
		var day string
		if err := rows.Scan(&day); err != nil {
			return nil, err
		}
		holidays[day] = true
	}
	return holidays, rows.Err()
}

// Correction is a manager's fix to one day's check-in and/or check-out; a zero time keeps the tap-derived value.
type Correction struct {
	EmployeeID string
	Day        time.Time
	In, Out    time.Time
	By         string
	At         time.Time
}

// SaveCorrection stores the correction, replacing any for that employee and day; with both In and Out zero it deletes it.
func (s *Store) SaveCorrection(c Correction) error {
	day := c.Day.Format(dateFmt)
	if c.In.IsZero() && c.Out.IsZero() {
		_, err := s.db.Exec(`DELETE FROM corrections WHERE employee_id = ? AND day = ?`, c.EmployeeID, day)
		return err
	}
	stamp := func(t time.Time) string {
		if t.IsZero() {
			return ""
		}
		return t.In(s.loc).Format(stampFmt)
	}
	_, err := s.db.Exec(`INSERT INTO corrections (employee_id, day, check_in, check_out, changed_by, changed_at)
		VALUES (?, ?, NULLIF(?, ''), NULLIF(?, ''), ?, ?)`,
		c.EmployeeID, day, stamp(c.In), stamp(c.Out), c.By, stamp(c.At))
	if isDuplicate(err) {
		_, err = s.db.Exec(`UPDATE corrections SET check_in = NULLIF(?, ''), check_out = NULLIF(?, ''), changed_by = ?, changed_at = ?
			WHERE employee_id = ? AND day = ?`, stamp(c.In), stamp(c.Out), c.By, stamp(c.At), c.EmployeeID, day)
	}
	return err
}

// Corrections returns the employee's corrections from through to inclusive, keyed by dateFmt.
func (s *Store) Corrections(employeeID string, from, to time.Time) (map[string]Fix, error) {
	rows, err := s.db.Query(`SELECT day, COALESCE(check_in, ''), COALESCE(check_out, '') FROM corrections
		WHERE employee_id = ? AND day >= ? AND day <= ?`, employeeID, from.Format(dateFmt), to.Format(dateFmt))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	parse := func(v string) (time.Time, error) {
		if v == "" {
			return time.Time{}, nil
		}
		return time.ParseInLocation(stampFmt, v, s.loc)
	}
	fixes := map[string]Fix{}
	for rows.Next() {
		var day, in, out string
		if err := rows.Scan(&day, &in, &out); err != nil {
			return nil, err
		}
		var f Fix
		if f.In, err = parse(in); err != nil {
			return nil, err
		}
		if f.Out, err = parse(out); err != nil {
			return nil, err
		}
		fixes[day] = f
	}
	return fixes, rows.Err()
}
