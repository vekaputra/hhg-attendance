package main

import (
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"strings"
)

// copyTables are the tables copyDB moves and their columns, parents before the tables that reference them.
// sessions is left out: users log in again.
var copyTables = []struct{ name, cols string }{
	{"orgs", "id, name, kind, grace_minutes, start_time, end_time, weekdays"},
	{"shifts", "id, org_id, name, start_time, end_time"},
	{"employees", "id, name, org_id, active_from, active_until"},
	{"taps", "device_id, serial_no, device_time, employee_id, received_at"},
	{"devices", "device_id, last_contact"},
	{"roster", "employee_id, day, shift_name, start_time, end_time, changed_by, changed_at"},
	{"leaves", "id, employee_id, from_day, to_day"},
	{"holidays", "day"},
	{"corrections", "employee_id, day, check_in, check_out, changed_by, changed_at"},
	{"users", "name, salt, hash"},
}

// copyDB copies the SQLite database at srcPath into st, keeping IDs, all in one transaction. It refuses unless
// st has no employees, taps or users, and replaces st's seeded orgs and shifts with the source's.
func copyDB(st *Store, srcPath string) error {
	if _, err := os.Stat(srcPath); err != nil {
		return err // sql.Open would otherwise create an empty file
	}
	src, err := sql.Open("sqlite", srcPath)
	if err != nil {
		return err
	}
	defer src.Close()

	// A database from before the table was renamed has leave, not leaves.
	leaves := "leaves"
	var n int
	if err := src.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'leaves'`).Scan(&n); err != nil {
		return fmt.Errorf("read %s: %w", srcPath, err)
	}
	if n == 0 {
		leaves = "leave"
	}

	tx, err := st.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, table := range []string{"employees", "taps", "users"} {
		if err := tx.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return fmt.Errorf("target is not empty (%d rows in %s); copy-db needs a fresh database", n, table)
		}
	}
	for _, table := range []string{"shifts", "orgs"} { // the seeded ones; the source's IDs would collide
		if _, err := tx.Exec("DELETE FROM " + table); err != nil {
			return err
		}
	}

	for _, t := range copyTables {
		from := t.name
		if from == "leaves" {
			from = leaves
		}
		rows, err := src.Query("SELECT " + t.cols + " FROM " + from)
		if err != nil {
			return fmt.Errorf("read %s: %w", from, err)
		}
		n, err := copyRows(tx, t.name, rows)
		rows.Close()
		if err != nil {
			return fmt.Errorf("copy %s: %w", t.name, err)
		}
		slog.Info("copied", "table", t.name, "rows", n)
	}
	return tx.Commit()
}

// copyRows inserts every row, with the same columns, into table and returns how many.
func copyRows(tx *sql.Tx, table string, rows *sql.Rows) (int, error) {
	cols, err := rows.Columns()
	if err != nil {
		return 0, err
	}
	marks := strings.TrimSuffix(strings.Repeat("?, ", len(cols)), ", ")
	ins, err := tx.Prepare("INSERT INTO " + table + " (" + strings.Join(cols, ", ") + ") VALUES (" + marks + ")")
	if err != nil {
		return 0, err
	}
	defer ins.Close()
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	n := 0
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return n, err
		}
		if _, err := ins.Exec(vals...); err != nil {
			return n, err
		}
		n++
	}
	return n, rows.Err()
}
