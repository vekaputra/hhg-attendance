# Raspberry Pi relay for the fingerprint device: design

Date: 2026-10-23. Status: built. The Pi setup guide (relay/README.md) is untested on real hardware.

## Why

The single fingerprint device (Hikvision DS-K1A8503MF, EHome 4.0, device ID `gshhgfp`) serves both the office and
the cafe from one building. That building's ISP uses carrier-grade NAT (the router's WAN address is `10.123.143.18`,
the public one `103.136.59.137`), and it drops IP fragments. Every access event the device sends is one 9216-byte UDP
packet, which must be fragmented on the internet, so no event from this building ever reaches the VPS. REGISTER and
KEEPALIVE are small, so they do arrive, which is why the device looks registered but no taps show up.

What was established while debugging:

- A laptop on the building's network sending a 9000-byte UDP packet to the VPS: nothing arrives, not even the first
  fragment. A 100-byte packet arrives.
- The same device pointed at a server on the building's LAN works: events arrive and are stored.
- The firmware always sends events over UDP. Replying with `AlarmServerType` 1 (UDP and TCP) or 2 (MQTT) changed
  nothing.
- ISUP 5.0 does use TCP, but its login is an undocumented key-based handshake that needs Hikvision's closed SDK. Not
  pursued.
- The device keeps events it has not had acked and resends them after it registers again.

So a Raspberry Pi Zero on the building's LAN receives the device's UDP and pushes events to the VPS over HTTPS, which
the ISP carries fine.

## Shape

```
fingerprint device --UDP 7600, LAN--> Pi relay (relay/) --HTTPS POST, basic auth--> VPS app (server/) --> MySQL
                                        |
                                        +-- /data on the SD card: unsent-event queue, daily log files
```

## Goals

### Repo and programs

1. Two separate programs in this repo, each in its own directory: `server/` (the VPS app: today's code, minus the UDP
   listener) and `relay/` (the Pi program). Two binaries, not one binary with modes. The Dockerfile, compose file,
   and DEPLOY.md follow the new paths.

### Pi relay (`relay/`)

2. **Receive the device's EHome 4.0 UDP** on port 7600 on the LAN. It handles REGISTER and KEEPALIVE itself, exactly
   as `ehome.go` does today:
   - Check the device ID against an allow-list.
   - Echo the device's `RegisterServer` (the Pi's LAN IP) as `AlarmServerIP`.
   - Answer KEEPALIVE.
   - Answer requests from unregistered senders with 403 and `<Method>REGISTER</Method>`.
3. **Forward every other message** from a registered device to the VPS: all ACS events, with or without an employee
   number, and any other command the device sends. KEEPALIVE and REGISTER are never forwarded.
   - Only ACS events get the guarantees in goals 4–6 (ack only when safe, queue, retry).
   - Other commands are best-effort: ack them as today, try one POST carrying the raw packet, and on failure only log
     it. They are never queued.
   - Only REGISTER, KEEPALIVE and ACS have been seen from this device.
4. **Ack the device only once the event is safe:** after the VPS confirmed it, or after it was durably written to the
   queue on `/data` (written and fsynced). If both fail, send no ack, so the device keeps the event and resends it.
5. **Queue what could not be sent.** When the POST fails (network error, timeout, any non-success answer), write the
   event to the queue on `/data`. The queue survives reboots and power cuts.
6. **Retry the queue**:
   - hourly;
   - immediately after any live event is POSTed successfully, so the dashboard catches up as soon as the internet is
     back.

   Delete each queued event once the VPS confirms it. Send oldest first.
7. **Logs**:
   - Keep log lines in memory and append them to `/data/logs/YYYY-MM-DD.log` once an hour, one file per day.
   - Also flush on a clean shutdown (SIGTERM, e.g. reboot or `systemctl restart`).
   - Delete log files older than 30 days.
   - Losing up to an hour of log lines on a power cut or crash is accepted. Events never depend on the logs.
8. **Configuration from the environment**: the VPS endpoint URL, the basic-auth username and password, and the allowed
   device IDs. The listen port and `/data` path have sensible defaults. The Pi holds no database credentials.

### VPS app (`server/`)

9. **Stop listening for UDP entirely.** Remove the EHome listener; compose no longer publishes `7600/udp`; DEPLOY.md
   tells the user to close the Oracle UDP rule.
10. **Receive events via a POST endpoint** protected by HTTP basic auth. The username and password come from
    environment variables and must match the Pi's. If they are unset, the endpoint refuses everything (fail closed).
    It is reached over HTTPS through the existing Nginx Proxy Manager. It bypasses the managers' session login and the
    cross-origin protection, which are for browsers.
11. **Store events as today:**
    - every ACS event goes into `events`;
    - those with an employee number also go into `taps`;
    - device time is read in the business timezone;
    - an event with an unreadable time, or a value too long for its column, is logged and dropped, but still counts
      as accepted, so the Pi does not retry it forever.
12. **Deduplicate by hash.** Each event carries a SHA-256 hash of its own fields:
    - The fields are device ID, serial number, device time, major, minor, employee number and card number, taken as
      the device sent them.
    - The hash never covers the raw packet, because the device's message `Sequence` changes on every resend.
    - The Pi computes it. The VPS stores it in a new unique column on `events` and ignores any event whose hash it
      already has.
    - The existing primary keys stay, so data from before this change keeps deduplicating as it does now.
13. **Show the last-received timestamp.** The dashboard shows "Last event received: <time>": the latest `received_at`
    of any stored event, in the business timezone. It is a plain timestamp, with no threshold and no warning.
    `received_at` is the VPS's time when it stored the event, so the timestamp shows when data last reached the VPS,
    which is what the user asked to see. After an outage, a queue flush shows the flush time. It never depends on the
    Pi's clock.
14. **Replace the device-contact warning.** It is removed, together with the KEEPALIVE-based device contact recording
    behind it.
15. **Website and dashboard otherwise unchanged.**

### Raspberry Pi setup (documented, not code)

16. **Document installing the relay on Raspberry Pi OS Lite** (Trixie) on a Pi Zero:
    - the build command for its CPU;
    - a systemd unit with automatic restart;
    - the environment file, readable by root only;
    - a fixed LAN IP via the router's DHCP reservation;
    - pointing the device at the Pi's IP, port 7600, protocol 4.0.
17. **Document the read-only system and the writable `/data`:**
    - The system runs with Raspberry Pi OS's Overlay File System, so the SD card is never written except under
      `/data`.
    - `/data` is a separate ext4 partition on the same card, outside the overlay, holding only the queue and the logs.
    - The guide explains how to create that partition, which is non-trivial because the root filesystem grows to
      fill the card on first boot, and how to turn the overlay off and on for changes.

## Non-goals

- Supporting devices that talk to the VPS directly. There is one device and one Pi.
- ISUP 5.0, MQTT, TCP alarm delivery, or a VPN or WireGuard tunnel.
- Tracking whether the device or the Pi is online beyond the last-received timestamp: no heartbeat, no alerting.
- A database, web UI or dashboard on the Pi.
- Shipping the Pi's logs to the VPS.

## Anti-goals (failure even if it works)

- **A tap is lost:** the device got an ack, but the event is neither on the VPS nor in the Pi's queue.
- **A tap is counted twice** after a retry or a resend.
- **The Pi wears the SD card** by writing to it outside `/data`, or writing to `/data` more than the queue and the
  hourly log flush need.
- **The endpoint accepts events without valid credentials**, or works with the credentials unset.

## Constraints

- **Pi Zero:** 512 MB RAM, single core, 2.4 GHz Wi-Fi only. The model (Zero W, ARMv6, or Zero 2 W, ARM64) is not
  confirmed, so the build instructions cover both.
- **Go, standard library first**, matching the existing code. The VPS stays a static binary on distroless in Docker.
- **The Pi's clock** comes from NTP only, with no RTC, and the overlay prevents `fake-hwclock` from saving it. So right
  after boot, the Pi's clock may be wrong until NTP syncs. This affects only log file names and timestamps. Event times
  come from the device.
- **MySQL in production** (strict `sql_mode`) and SQLite in development. Schema changes go in both
  `schema_mysql.sql` and `schema_sqlite.sql`, and the existing production database must keep working: add the hash
  column to existing tables, do not recreate them.

## Decided by the user

- One Pi and one device for both organisations. The VPS accepts no UDP at all.
- Separate programs in separate directories in the same repo.
- POST over HTTPS with basic auth from env vars on both sides (not a token scheme).
- Forward all events except KEEPALIVE and REGISTER.
- Queue unsent events on the SD card. Retry hourly, and also flush after any successful live POST. Delete on
  confirmation.
- Logs buffered in RAM for an hour, then appended to a daily file on `/data`. Delete after 30 days.
- Track only the Pi, as the last-received timestamp. No heartbeat and no warning.
- Deduplicate by a hash of the event.

## Calls made in the design (not discussed with the user)

- The hash covers the event's fields, not the raw packet (see goal 12).
- The Pi parses events and sends structured fields. Parsing therefore moves to the Pi along with the EHome code, and
  the VPS no longer reads XML.
- An event the VPS cannot store (unreadable time, value too long) is acknowledged as accepted and logged, as today.
- The device-contact warning goes, and the `devices` table is left in place unused.
- The relay is installed as a plain binary under systemd, not Docker.
- `received_at` is the VPS's storage time (goal 13).
- Non-ACS commands are best-effort and never queued (goal 3).
- **Testing:**
  - The code is tested with a fake device (UDP) and a fake VPS (HTTP), including the outage, queue, restart and
    duplicate cases.
  - The Pi setup guide, including `/data` and the overlay, is checked by the user running it on the real Pi with
    the builder.
  - Any step not yet run on real hardware is marked untested in the guide.

## Left to the builder

- **The endpoint:** its path, the JSON shape, and whether one request can carry several events (useful for flushing
  the queue). The response must let the Pi tell "stored or already had" from "try again".
- **The queue format** on `/data`, e.g. one file per event named by its hash, written via temp file, fsync and
  rename.
- **Package layout** within `server/` and `relay/`, and whether the repo stays one Go module.
- **Timeouts and HTTP client settings** on the Pi.
- **What the VPS does with non-ACS commands** it receives: at least log them.
- **The new hash column's migration** on the existing MySQL and SQLite databases (MySQL has no
  `ADD COLUMN IF NOT EXISTS`). Whether `copy-db` carries the column.
- **The `/data` partition procedure.** Verify it on real Raspberry Pi OS Trixie before documenting it.
