-- Dates are dateFmt, clock times clockFmt, timestamps stampFmt; all business-local text.
CREATE TABLE IF NOT EXISTS orgs (
	id            INTEGER PRIMARY KEY,
	name          TEXT NOT NULL UNIQUE,
	kind          TEXT NOT NULL CHECK (kind IN ('fixed', 'roster')),
	grace_minutes INTEGER NOT NULL DEFAULT 0,
	start_time    TEXT NOT NULL DEFAULT '', -- fixed orgs only
	end_time      TEXT NOT NULL DEFAULT '', -- fixed orgs only
	weekdays      TEXT NOT NULL DEFAULT ''  -- fixed orgs only, ISO digits: 1 = Monday .. 7 = Sunday
);

CREATE TABLE IF NOT EXISTS shifts (
	id         INTEGER PRIMARY KEY,
	org_id     INTEGER NOT NULL REFERENCES orgs (id),
	name       TEXT NOT NULL,
	start_time TEXT NOT NULL,
	end_time   TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS employees (
	id           TEXT PRIMARY KEY,
	name         TEXT NOT NULL,
	org_id       INTEGER NOT NULL REFERENCES orgs (id),
	active_from  TEXT NOT NULL,
	active_until TEXT
);

-- No foreign key on employee_id: taps from unknown employees are kept.
CREATE TABLE IF NOT EXISTS taps (
	device_id   TEXT NOT NULL,
	serial_no   INTEGER NOT NULL,
	device_time TEXT NOT NULL,
	employee_id TEXT NOT NULL,
	received_at TEXT NOT NULL,
	PRIMARY KEY (device_id, serial_no, device_time)
);
CREATE INDEX IF NOT EXISTS taps_employee_time ON taps (employee_id, device_time);

CREATE TABLE IF NOT EXISTS devices (
	device_id    TEXT PRIMARY KEY,
	last_contact TEXT NOT NULL
);

-- A NULL shift_name means the employee is off that day.
CREATE TABLE IF NOT EXISTS roster (
	employee_id TEXT NOT NULL REFERENCES employees (id),
	day         TEXT NOT NULL,
	shift_name  TEXT,
	start_time  TEXT,
	end_time    TEXT,
	changed_by  TEXT NOT NULL,
	changed_at  TEXT NOT NULL,
	PRIMARY KEY (employee_id, day)
);

CREATE TABLE IF NOT EXISTS leave (
	id          INTEGER PRIMARY KEY,
	employee_id TEXT NOT NULL REFERENCES employees (id),
	from_day    TEXT NOT NULL,
	to_day      TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS holidays (
	day TEXT PRIMARY KEY
);

CREATE TABLE IF NOT EXISTS corrections (
	employee_id TEXT NOT NULL REFERENCES employees (id),
	day         TEXT NOT NULL,
	check_in    TEXT,
	check_out   TEXT,
	changed_by  TEXT NOT NULL,
	changed_at  TEXT NOT NULL,
	PRIMARY KEY (employee_id, day)
);

CREATE TABLE IF NOT EXISTS users (
	name TEXT PRIMARY KEY,
	salt TEXT NOT NULL,
	hash TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS sessions (
	token_hash TEXT PRIMARY KEY,
	user_name  TEXT NOT NULL,
	expires_at TEXT NOT NULL
);
