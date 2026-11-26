package main

import (
	"database/sql"
	"strconv"
	"time"
)

const defaultPartTimeWage = 130000

// Break is a part-timer's unpaid break. Day is the attendance date (midnight, s.loc) of Start.
type Break struct {
	ID         int64
	EmployeeID string
	Day        time.Time
	Start, End time.Time
	By         string
	At         time.Time
}

func (s *Store) AddBreak(b Break) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO breaks (employee_id, day, start_at, end_at, created_by, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		b.EmployeeID, b.Day.Format(dateFmt), b.Start.In(s.loc).Format(stampFmt), b.End.In(s.loc).Format(stampFmt),
		b.By, b.At.In(s.loc).Format(stampFmt))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// Breaks returns the breaks with Day from through to inclusive, ordered by day, employee and start.
func (s *Store) Breaks(from, to time.Time) ([]Break, error) {
	rows, err := s.db.Query(`SELECT id, employee_id, day, start_at, end_at, created_by, created_at FROM breaks
		WHERE day >= ? AND day <= ? ORDER BY day, employee_id, start_at`, from.Format(dateFmt), to.Format(dateFmt))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var breaks []Break
	for rows.Next() {
		var b Break
		var day, start, end, at string
		if err := rows.Scan(&b.ID, &b.EmployeeID, &day, &start, &end, &b.By, &at); err != nil {
			return nil, err
		}
		for _, f := range []struct {
			dst    *time.Time
			layout string
			v      string
		}{{&b.Day, dateFmt, day}, {&b.Start, stampFmt, start}, {&b.End, stampFmt, end}, {&b.At, stampFmt, at}} {
			if *f.dst, err = time.ParseInLocation(f.layout, f.v, s.loc); err != nil {
				return nil, err
			}
		}
		breaks = append(breaks, b)
	}
	return breaks, rows.Err()
}

func (s *Store) DeleteBreak(id int64) error {
	_, err := s.db.Exec(`DELETE FROM breaks WHERE id = ?`, id)
	return err
}

// PartTimeWage is the daily wage for a full 8-hour day, shared by all part-timers.
func (s *Store) PartTimeWage() (int64, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE name = 'part_time_wage'`).Scan(&v)
	if err == sql.ErrNoRows {
		return defaultPartTimeWage, nil
	}
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(v, 10, 64)
}

func (s *Store) SetPartTimeWage(wage int64) error {
	v := strconv.FormatInt(wage, 10)
	_, err := s.db.Exec(`INSERT INTO settings (name, value) VALUES ('part_time_wage', ?)`, v)
	if isDuplicate(err) {
		_, err = s.db.Exec(`UPDATE settings SET value = ? WHERE name = 'part_time_wage'`, v)
	}
	return err
}
