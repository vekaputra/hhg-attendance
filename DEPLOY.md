# Deploying hhg-attendance

One binary: the EHome UDP listener and the web app. Production stores its data in MySQL (see "MySQL");
development uses a SQLite file beside the binary, which needs no setup.

## Build

Set the business timezone and the server's public IP when you build:

    go build -ldflags "-X main.timezone=Asia/Jakarta -X main.alarmIP=<VPS public IP>" -o hhg-attendance .

Or edit `timezone` (main.go) and `alarmIP` (ehome.go) and run `go build`. The app exits at start if the
timezone is empty or unknown. `alarmIP` is where the devices are told to send events; on a VPS behind NAT
(Oracle's public IP is NAT'd) it must be the public IP. If it is empty and a device registers from the internet,
the log shows an error at REGISTER.

## MySQL

Needs MySQL 8.0.16 or newer (older versions ignore the `CHECK` on `orgs.kind`) in its default strict `sql_mode`
(`STRICT_TRANS_TABLES`). Otherwise overlong values are truncated with only a warning, which could make distinct
taps collide as duplicates.

1. Create the database and a user that owns it. The binary collation keeps comparisons case-sensitive, as in SQLite:

       CREATE DATABASE hhg CHARACTER SET utf8mb4 COLLATE utf8mb4_bin;
       CREATE USER 'hhg'@'127.0.0.1' IDENTIFIED BY '<password>';
       GRANT ALL PRIVILEGES ON hhg.* TO 'hhg'@'127.0.0.1';

   The app creates its tables on start, which these privileges cover.

2. Build with the driver set (default is `sqlite`):

       go build -ldflags "-X main.timezone=Asia/Jakarta -X main.alarmIP=<VPS public IP> -X main.dbDriver=mysql" -o hhg-attendance .

   Or edit `dbDriver` in `main.go`.

3. Pass the DSN in the environment, not the binary, so the password is never compiled in. `ATTENDANCE_DSN`
   overrides `dbDSN` (the SQLite file name by default):

       ATTENDANCE_DSN='hhg:<password>@tcp(127.0.0.1:3306)/hhg?charset=utf8mb4&collation=utf8mb4_bin' ./hhg-attendance

   Keep `charset` and `collation` as shown. `parseTime` and `loc` are not needed: times are stored as text.
   Under systemd, put the line in an `EnvironmentFile=` readable only by the service user. The same variable
   applies to `adduser` and `copy-db`.

### Moving an existing SQLite database

With the app stopped (so no tap lands in the old file), run the new binary once with the SQLite file as the argument:

    ATTENDANCE_DSN='...' ./hhg-attendance copy-db attendance.db

It copies every table with the same IDs, in one transaction, and logs the row count per table. Sessions are not
copied, so everyone logs in again. It refuses unless the MySQL database has no employees, taps, users or holidays,
so run it before the first start on MySQL. If the app already ran there, the seeded organisations and shifts and
any device contacts it recorded are replaced by the copy. If it fails, nothing is copied: fix the cause and run it
again. The SQLite file is only read; keep it until you have checked the app.

A SQLite file from before the `leave` table became `leaves` is renamed in place the first time the new binary
opens it, and `copy-db` reads the old name too. If a file somehow holds rows in both, `copy-db` refuses; copy the
rows into `leaves`, drop `leave`, and retry.

## Devices

`allowedDeviceIDs` in `ehome.go` lists the Device IDs allowed to register. It holds only `gshhgfp`; **add the
cafe device's ID** and rebuild, or it is rejected with 401 (see "register rejected" in the server log).

## Users

    printf '%s\n' '<password>' | ./hhg-attendance adduser <name>

Usernames are lowercase (`a-z 0-9 _ -`, up to 32); passwords are at least 10 characters. Running it again for
an existing user resets the password and logs that user out everywhere.

## Firewall (Oracle)

Open the VCN security list or NSG, and check the instance's own firewall too:

- **UDP 7600**: only from the office and cafe public IPs. The listener trusts the UDP source address, which can
  be spoofed, so this allow-list is the real protection.
- **TCP 8080**: not open to the internet.

## Reverse proxy

The web app speaks plain HTTP, so put a proxy in front of it:

- **HTTPS is required.** The session cookie is `Secure`, so on plain HTTP login appears to succeed and loops
  back to the login page.
- Pass the `Host` header through unchanged; cross-origin POSTs are checked against it.
- Add a login rate limit at the proxy (the app's own lockout is per username only).
- `webAddr` defaults to `127.0.0.1:8080`, so the proxy must run on the same host. To listen elsewhere, build
  with `-X main.webAddr=<addr>`.

## Files and backups

Logs go to stderr only (run it under systemd so the journal keeps them). Every access event the devices send is
stored in the database and shown on the Events page. With MySQL, back up the database:

    mysqldump --single-transaction -u hhg -p hhg > backup-$(date +%F).sql

With SQLite (development), `attendance.db` (with `-wal` and `-shm` beside it while running) is created in the
working directory. Back up with

    sqlite3 attendance.db "VACUUM INTO 'backup-$(date +%F).db'"

which is safe while the app runs; it fails if the target file already exists.

## Notes

- Pages follow the OS or browser dark-mode setting.
- All SQL lives in the `Store` methods (`store.go`, `taps.go`, `data.go`, `auth.go`) and runs unchanged on SQLite
  and MySQL. A schema change goes in both `schema_sqlite.sql` and `schema_mysql.sql`.
