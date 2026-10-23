# Deploying hhg-attendance

This is the VPS half of the system: the web app (`server/`, one binary). It accepts no UDP. The fingerprint device
talks to a Raspberry Pi on its own LAN, and the Pi relays each event here over HTTPS (see `relay/README.md`).
Production stores its data in MySQL (see "MySQL"); development uses a SQLite file beside the binary, which needs
no setup.

## Build

Build with `go build -o hhg-attendance ./server` (or just `go run ./server`). The `-o` is needed: without it Go
tries to write a file called `server` over the `server/` directory. The business timezone defaults to Asia/Jakarta;
for another zone set `TIMEZONE`, e.g. `TIMEZONE=Asia/Makassar go run ./server`. The app exits at start if it is
unknown. `go run` does not read `.env`; only Docker Compose does.

## How events arrive

The Pi relay sends each device event to `POST /api/events` with HTTP basic auth. The login is `RELAY_USER` and
`RELAY_PASSWORD` in `.env`, and **must match** the same two values in the Pi's `/etc/hhg-relay.env`. While either is
empty the endpoint refuses every request, so the relay gets 401 and queues its events on the Pi.

The endpoint goes through the existing Nginx Proxy Manager host for the web app, over HTTPS: the relay refuses a
URL that does not start with `https://`, so basic auth never travels in clear text. The Pi's `RELAY_URL` is
`https://<your-domain>/api/events`. No other proxy setting is needed.

Duplicates are ignored: each event carries a hash of its own fields, kept in `events.event_hash`. An existing
database gets that column added in place the first time the new binary starts.

## Docker (production)

`docker-compose.yml` follows the other services on the VPS: image `hhg-attendance`, container `hhg_attendance`,
`restart: always`, a 64M memory limit, and the external networks `local-app-network` (reverse proxy) and
`local-db-network` (MySQL). The image is a static binary on distroless (about 16 MB, no shell).

1. Copy `.env.example` to `.env` beside `docker-compose.yml` and fill in
   `ATTENDANCE_DSN` (MySQL addressed by its container name on `local-db-network`), `RELAY_USER` and
   `RELAY_PASSWORD`. Generate the password with `openssl rand -base64 24`; base64 is safe in `.env` and in the
   Pi's env file. Keep both values: the Pi needs the same ones. `.env` is git-ignored.
   `TIMEZONE` is optional (Asia/Jakarta by default). Settings are read at start, so after changing `.env` run
   `docker compose up -d` (no rebuild needed).
2. `docker compose build && docker compose up -d`
3. Create a login: `printf '%s\n' '<password>' | docker exec -i hhg_attendance /hhg-attendance adduser <name>`
4. Point the reverse proxy, on `local-app-network`, at `http://hhg-attendance:8080`. The web port is also
   published on the host's loopback only, at a random port (`docker compose port hhg-attendance 8080`).
   The container publishes no UDP port.

Logs: `docker logs hhg_attendance`. To migrate an existing SQLite file:
`docker run --rm --network local-db-network --env-file .env -v "$PWD/attendance.db:/attendance.db:ro" hhg-attendance copy-db /attendance.db`.
Compose warns that the `version` attribute is obsolete; it is kept to match the other services and is ignored.

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

       go build -ldflags "-X main.dbDriver=mysql" -o hhg-attendance ./server

   Or edit `dbDriver` in `server/main.go`.

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
copied, so everyone logs in again. It refuses unless the MySQL database has no employees, taps, events, users or holidays,
so run it before the first start on MySQL. If the app already ran there, the seeded organisations and shifts and
any device contacts it recorded are replaced by the copy. If it fails, nothing is copied: fix the cause and run it
again. The SQLite file is only read; keep it until you have checked the app.

A SQLite file from before the `leave` table became `leaves` is renamed in place the first time the new binary
opens it, and `copy-db` reads the old name too. If a file somehow holds rows in both, `copy-db` refuses; copy the
rows into `leaves`, drop `leave`, and retry.

## Users

    printf '%s\n' '<password>' | ./hhg-attendance adduser <name>

Usernames are lowercase (`a-z 0-9 _ -`, up to 32); passwords are at least 10 characters. Running it again for
an existing user resets the password and logs that user out everywhere.

## Firewall (Oracle)

Open the VCN security list or NSG, and check the instance's own firewall too:

- **TCP 443** (HTTPS, through the proxy) and **TCP 22** (SSH) are all that is needed. If Nginx Proxy Manager gets
  its certificates over HTTP, TCP 80 as well.
- **UDP 7600**: delete the old ingress rule. The server no longer listens for UDP, so nothing is behind it. Do
  this last (see "Cut-over").
- **TCP 8080**: not open to the internet.

## Cut-over

Switching an existing install from devices talking to the VPS to the Pi relay, in this order:

1. Deploy this version of the server with `RELAY_USER` and `RELAY_PASSWORD` set (see "Docker").
2. Bring the Pi up following `relay/README.md` and point the device at the Pi.
3. Scan a fingerprint and check that the "Last event received" time in the header of any page moves.
4. Only then delete the Oracle UDP 7600 rule.

## Reverse proxy

The web app speaks plain HTTP, so put a proxy in front of it:

- **HTTPS is required.** The session cookie is `Secure`, so on plain HTTP login appears to succeed and loops
  back to the login page.
- Pass the `Host` header through unchanged; cross-origin POSTs are checked against it.
- Add a login rate limit at the proxy (the app's own lockout is per username only).
- `webAddr` defaults to `127.0.0.1:8080`, so the proxy must run on the same host. To listen elsewhere, build
  with `-X main.webAddr=<addr>`.

## Files and backups

Logs go to stderr only (run it under systemd so the journal keeps them). Every access event the Pi relay forwards is
stored in the database and shown on the Events page. With MySQL, back up the database:

    mysqldump --single-transaction -u hhg -p hhg > backup-$(date +%F).sql

With SQLite (development), `attendance.db` (with `-wal` and `-shm` beside it while running) is created in the
working directory. Back up with

    sqlite3 attendance.db "VACUUM INTO 'backup-$(date +%F).db'"

which is safe while the app runs; it fails if the target file already exists.

## Notes

- Pages follow the OS or browser dark-mode setting.
- All SQL lives in the `Store` methods (`server/store.go`, `server/taps.go`, `server/data.go`, `server/auth.go`) and runs unchanged on SQLite
  and MySQL. A schema change goes in both `server/schema_sqlite.sql` and `server/schema_mysql.sql`.
