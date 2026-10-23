package main

import (
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	sqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// All dates and times are business-local text in these layouts; stampFmt is the device's Time format.
const (
	dateFmt  = "2006-01-02"
	clockFmt = "15:04"
	stampFmt = "2006-01-02 15:04:05"
)

//go:embed schema_sqlite.sql
var schemaSQLite string

//go:embed schema_mysql.sql
var schemaMySQL string

// Store owns the database. All SQL lives in its methods and runs unchanged on SQLite (development) and MySQL (production).
type Store struct {
	db  *sql.DB
	loc *time.Location
}

// Org is a group of employees. Start, End and Weekdays are empty for roster orgs.
type Org struct {
	ID           int64
	Name, Kind   string // Kind is "fixed" or "roster"
	GraceMinutes int
	Start, End   string // clockFmt
	Weekdays     string // ISO digits, 1 = Monday .. 7 = Sunday
}

// Shift is a named start/end pair a roster org's employees are scheduled onto.
type Shift struct {
	ID, OrgID  int64
	Name       string
	Start, End string // clockFmt
}

// OpenStore opens the database, applies the schema and seeds an empty one. driver is "sqlite" (dsn is the
// file, created if needed) or "mysql" (dsn is the go-sql-driver DSN, used as given).
func OpenStore(driver, dsn string, loc *time.Location) (*Store, error) {
	var schema string
	switch driver {
	case "sqlite":
		schema = schemaSQLite
		dsn += "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	case "mysql":
		schema = schemaMySQL
	default:
		return nil, fmt.Errorf("unknown database driver %q, want sqlite or mysql", driver)
	}
	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, err
	}
	s := &Store{db: db, loc: loc}
	if driver == "sqlite" {
		if err := renameOldLeave(db); err != nil {
			db.Close()
			return nil, err
		}
	}
	// MySQL's driver rejects multi-statement scripts, so apply the schema one statement at a time.
	for _, stmt := range strings.Split(schema, ";\n") {
		if strings.TrimSpace(stmt) == "" {
			continue
		}
		if _, err := db.Exec(stmt); err != nil {
			db.Close()
			return nil, fmt.Errorf("apply schema: %w", err)
		}
	}
	if err := addEventHash(db, driver); err != nil {
		db.Close()
		return nil, err
	}
	if err := s.seed(); err != nil {
		db.Close()
		return nil, fmt.Errorf("seed: %w", err)
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

// hasTable reports whether the SQLite database has the table.
func hasTable(db *sql.DB, name string) (bool, error) {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&n)
	return n > 0, err
}

// renameOldLeave renames the table of a SQLite file from before leave became leaves, which the schema would
// otherwise leave behind while creating an empty leaves beside it.
func renameOldLeave(db *sql.DB) error {
	hasOld, err := hasTable(db, "leave")
	if err != nil {
		return err
	}
	hasNew, err := hasTable(db, "leaves")
	if err != nil || !hasOld || hasNew {
		return err
	}
	if _, err := db.Exec(`ALTER TABLE leave RENAME TO leaves`); err != nil {
		return fmt.Errorf("rename leave to leaves: %w", err)
	}
	return nil
}

// addEventHash adds events.event_hash and its unique index to a database from before the column existed. Old
// rows keep a NULL hash, which a unique index allows many of.
func addEventHash(db *sql.DB, driver string) error {
	columns := `SELECT COUNT(*) FROM pragma_table_info('events') WHERE name = 'event_hash'`
	add := `ALTER TABLE events ADD COLUMN event_hash TEXT`
	if driver == "mysql" {
		columns = `SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'events' AND COLUMN_NAME = 'event_hash'`
		add = `ALTER TABLE events ADD COLUMN event_hash CHAR(64) NULL, ADD UNIQUE KEY events_hash (event_hash)`
	}
	var n int
	if err := db.QueryRow(columns).Scan(&n); err != nil {
		return fmt.Errorf("check events.event_hash: %w", err)
	}
	if n == 0 {
		if _, err := db.Exec(add); err != nil {
			return fmt.Errorf("add events.event_hash: %w", err)
		}
	}
	if driver == "sqlite" {
		if _, err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS events_hash ON events (event_hash)`); err != nil {
			return fmt.Errorf("index events.event_hash: %w", err)
		}
	}
	return nil
}

// isDuplicate reports whether err is a duplicate-key violation from either driver.
func isDuplicate(err error) bool {
	var se *sqlite.Error
	if errors.As(err, &se) {
		return se.Code() == sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY || se.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE
	}
	var me *mysql.MySQLError
	return errors.As(err, &me) && me.Number == 1062
}

// isTooLong reports whether err is MySQL's "data too long for column" error; SQLite has no such limit.
func isTooLong(err error) bool {
	var me *mysql.MySQLError
	return errors.As(err, &me) && me.Number == 1406
}

// seed inserts the starting organisations unless any exist.
func (s *Store) seed() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM orgs`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	if _, err := tx.Exec(`INSERT INTO orgs (name, kind, grace_minutes, start_time, end_time, weekdays)
		VALUES ('Office', 'fixed', 0, '08:30', '17:30', '12345')`); err != nil {
		return err
	}
	res, err := tx.Exec(`INSERT INTO orgs (name, kind, grace_minutes) VALUES ('Cafe', 'roster', 0)`)
	if err != nil {
		return err
	}
	cafe, err := res.LastInsertId()
	if err != nil {
		return err
	}
	for _, sh := range []Shift{{Name: "1", Start: "06:00", End: "15:00"}, {Name: "2", Start: "11:00", End: "20:00"}, {Name: "3", Start: "15:00", End: "22:00"}} {
		if _, err := tx.Exec(`INSERT INTO shifts (org_id, name, start_time, end_time) VALUES (?, ?, ?, ?)`, cafe, sh.Name, sh.Start, sh.End); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Orgs returns all organisations ordered by ID.
func (s *Store) Orgs() ([]Org, error) {
	rows, err := s.db.Query(`SELECT id, name, kind, grace_minutes, start_time, end_time, weekdays FROM orgs ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var orgs []Org
	for rows.Next() {
		var o Org
		if err := rows.Scan(&o.ID, &o.Name, &o.Kind, &o.GraceMinutes, &o.Start, &o.End, &o.Weekdays); err != nil {
			return nil, err
		}
		orgs = append(orgs, o)
	}
	return orgs, rows.Err()
}

// Shifts returns an organisation's shifts ordered by start time.
func (s *Store) Shifts(orgID int64) ([]Shift, error) {
	rows, err := s.db.Query(`SELECT id, org_id, name, start_time, end_time FROM shifts WHERE org_id = ? ORDER BY start_time, id`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var shifts []Shift
	for rows.Next() {
		var sh Shift
		if err := rows.Scan(&sh.ID, &sh.OrgID, &sh.Name, &sh.Start, &sh.End); err != nil {
			return nil, err
		}
		shifts = append(shifts, sh)
	}
	return shifts, rows.Err()
}
