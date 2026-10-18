-- MySQL 8.0.16 or newer (older versions parse CHECK but do not enforce it). Mirrors schema_sqlite.sql: keep them in step.
-- Dates are dateFmt, clock times clockFmt, timestamps stampFmt, all business-local text.
-- utf8mb4_bin keeps comparisons case-sensitive, like SQLite. Foreign keys are table-level because InnoDB ignores inline REFERENCES.
CREATE TABLE IF NOT EXISTS orgs (
	id            INT AUTO_INCREMENT PRIMARY KEY,
	name          VARCHAR(100) NOT NULL UNIQUE,
	kind          VARCHAR(10) NOT NULL CHECK (kind IN ('fixed', 'roster')),
	grace_minutes INT NOT NULL DEFAULT 0,
	start_time    VARCHAR(5) NOT NULL DEFAULT '', -- fixed orgs only
	end_time      VARCHAR(5) NOT NULL DEFAULT '', -- fixed orgs only
	weekdays      VARCHAR(7) NOT NULL DEFAULT ''  -- fixed orgs only, ISO digits: 1 = Monday .. 7 = Sunday
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS shifts (
	id         INT AUTO_INCREMENT PRIMARY KEY,
	org_id     INT NOT NULL,
	name       VARCHAR(100) NOT NULL,
	start_time VARCHAR(5) NOT NULL,
	end_time   VARCHAR(5) NOT NULL,
	FOREIGN KEY (org_id) REFERENCES orgs (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS employees (
	id           VARCHAR(32) PRIMARY KEY,
	name         VARCHAR(100) NOT NULL,
	org_id       INT NOT NULL,
	active_from  VARCHAR(10) NOT NULL,
	active_until VARCHAR(10),
	FOREIGN KEY (org_id) REFERENCES orgs (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

-- No foreign key on employee_id: taps from unknown employees are kept.
CREATE TABLE IF NOT EXISTS taps (
	device_id   VARCHAR(64) NOT NULL,
	serial_no   VARCHAR(20) NOT NULL,
	device_time VARCHAR(19) NOT NULL,
	employee_id VARCHAR(32) NOT NULL,
	received_at VARCHAR(19) NOT NULL,
	PRIMARY KEY (device_id, serial_no, device_time),
	INDEX taps_employee_time (employee_id, device_time)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS devices (
	device_id    VARCHAR(64) PRIMARY KEY,
	last_contact VARCHAR(19) NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

-- A NULL shift_name means the employee is off that day.
CREATE TABLE IF NOT EXISTS roster (
	employee_id VARCHAR(32) NOT NULL,
	day         VARCHAR(10) NOT NULL,
	shift_name  VARCHAR(100),
	start_time  VARCHAR(5),
	end_time    VARCHAR(5),
	changed_by  VARCHAR(100) NOT NULL,
	changed_at  VARCHAR(19) NOT NULL,
	PRIMARY KEY (employee_id, day),
	FOREIGN KEY (employee_id) REFERENCES employees (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS leaves (
	id          INT AUTO_INCREMENT PRIMARY KEY,
	employee_id VARCHAR(32) NOT NULL,
	from_day    VARCHAR(10) NOT NULL,
	to_day      VARCHAR(10) NOT NULL,
	FOREIGN KEY (employee_id) REFERENCES employees (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS holidays (
	day VARCHAR(10) PRIMARY KEY
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS corrections (
	employee_id VARCHAR(32) NOT NULL,
	day         VARCHAR(10) NOT NULL,
	check_in    VARCHAR(19),
	check_out   VARCHAR(19),
	changed_by  VARCHAR(100) NOT NULL,
	changed_at  VARCHAR(19) NOT NULL,
	PRIMARY KEY (employee_id, day),
	FOREIGN KEY (employee_id) REFERENCES employees (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

-- salt and hash are hex of 16 and 32 bytes (auth.go).
CREATE TABLE IF NOT EXISTS users (
	name VARCHAR(100) PRIMARY KEY,
	salt VARCHAR(32) NOT NULL,
	hash VARCHAR(64) NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

-- token_hash is hex SHA-256.
CREATE TABLE IF NOT EXISTS sessions (
	token_hash VARCHAR(64) PRIMARY KEY,
	user_name  VARCHAR(100) NOT NULL,
	expires_at VARCHAR(19) NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
