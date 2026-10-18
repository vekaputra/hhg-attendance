# Deploying hhg-attendance

One binary: the EHome UDP listener and the web app, with a SQLite file beside it.

## Build

Set the business timezone and the server's public IP when you build:

    go build -ldflags "-X main.timezone=Asia/Jakarta -X main.alarmIP=<VPS public IP>" -o hhg-attendance .

Or edit `timezone` (main.go) and `alarmIP` (ehome.go) and run `go build`. The app exits at start if the
timezone is empty or unknown. `alarmIP` is where the devices are told to send events; on a VPS behind NAT
(Oracle's public IP is NAT'd) it must be the public IP. If it is empty and a device registers from the internet,
the log shows an error at REGISTER.

## Devices

`allowedDeviceIDs` in `ehome.go` lists the Device IDs allowed to register. It holds only `gshhgfp`; **add the
cafe device's ID** and rebuild, or it is rejected with 401 (see "register rejected" in `ehome.log`).

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

`attendance.db` (with `-wal` and `-shm` beside it while running) and `ehome.log` are created in the working
directory, so run it from a directory you back up. Back up with

    sqlite3 attendance.db "VACUUM INTO 'backup-$(date +%F).db'"

which is safe while the app runs; it fails if the target file already exists.

## Notes

- Pages follow the OS or browser dark-mode setting.
- The database is SQLite for now; production is planned to move to MySQL. All SQL lives in the `Store` methods
  (`store.go`, `taps.go`, `data.go`, `auth.go`) so only those change.
