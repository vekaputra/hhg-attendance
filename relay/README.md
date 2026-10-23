# Installing the Pi relay

The relay (`relay/`) runs on a Raspberry Pi Zero on the fingerprint device's LAN. The device sends its events to
the Pi over UDP; the Pi forwards them to the VPS over HTTPS (`POST /api/events`, basic auth). Events the VPS cannot
take wait in a queue on the Pi's SD card and are retried. The Pi answers the device's REGISTER and KEEPALIVE itself.

This guide sets up Raspberry Pi OS Lite (Trixie, Debian 13) with a read-only system and a separate writable `/data`
partition for the queue and the logs. The VPS side is in `DEPLOY.md`: do that first, and keep its `RELAY_USER` and
`RELAY_PASSWORD`.

**Status:** none of this has been run on the real Pi yet. Every step is marked **untested** except the build
commands, which were run on a Mac. Remove a mark once that step has worked on the Pi.

Placeholders: `<user>` is the Pi login you create in step 1, `<your-domain>` is the domain Nginx Proxy Manager serves
the web app on.

## 1. Flash Raspberry Pi OS Lite (untested)

Use Raspberry Pi Imager. Choose Raspberry Pi OS Lite: the 32-bit one for a Zero W, 64-bit for a Zero 2 W. In the OS
customisation settings:

- Hostname `fp-rpi`.
- A normal username and a password (not `root`; SSH as `root` is refused by design). Enable SSH with password
  authentication.
- Wi-Fi: SSID, password and country. The Zero W and Zero 2 W are 2.4 GHz only.
- Locale and timezone `Asia/Jakarta`.

**Before the first boot, stop the root partition from growing.** Leave the card in the Mac, open the `bootfs`
volume and remove the word `resize` from `cmdline.txt`:

    sed -i '' -E 's/ resize( |$)/\1/' /Volumes/bootfs/cmdline.txt

Then check that the line is unchanged except for the missing word:

    cat /Volumes/bootfs/cmdline.txt

Why: on Trixie the initramfs grows the root partition to the end of the card on every boot whose kernel command line
contains `resize`. Root would then leave no room for the `/data` partition (step 7). Without `resize` the card keeps
its free space after the root partition.

Put the card in the Pi, power it, and log in:

    ssh <user>@fp-rpi.local

Set the timezone (Raspberry Pi OS ships `tzdata`, so no install is needed), even if Imager already did:

    sudo timedatectl set-timezone Asia/Jakarta

The relay names its log files by the Pi's local date, so this decides which day an event's log lines land in. The Pi
has no clock chip and the read-only system (step 9) stops `fake-hwclock` from saving the time, so for a short while
after each boot the clock may be wrong until NTP syncs. Check it with `timedatectl` ("System clock synchronized:
yes"). Only log timestamps, log file names and the queue's file order depend on it. Event times come from the
device and are unaffected.

**If the Pi has already booted once** (root already fills the card), re-flashing as above is the simplest fix, since
nothing is set up on it yet. The alternative is to shrink root offline on a Linux machine with a card reader, with the
card unmounted (**untested**; replace `/dev/sdX`, check it with `lsblk` first, and do not skip `e2fsck`):

    sudo e2fsck -f /dev/sdX2

    sudo resize2fs /dev/sdX2 4G

    sudo parted /dev/sdX resizepart 2 5GiB

Then remove `resize` from `cmdline.txt` on the boot partition, as above.

## 2. Wi-Fi (untested)

Imager set the first network. To add another (a phone hotspot for setup, say):

    sudo nmcli device wifi connect "<ssid>" password "<password>"

When several saved networks are in range, NetworkManager takes the one with the highest priority (default 0). Raise
the one you want first:

    sudo nmcli connection modify "<ssid>" connection.autoconnect-priority 10

    nmcli -f NAME,AUTOCONNECT-PRIORITY connection show

Give the Pi a fixed address with a DHCP reservation on the router, not a static IP on the Pi. Find the Wi-Fi MAC
address:

    cat /sys/class/net/wlan0/address

and reserve an IP for it on the router. This IP goes into the device's settings in step 8.

Do all Wi-Fi changes now: once the read-only system is on (step 9), they are lost on reboot.

## 3. Swap (untested)

Trixie's `rpi-swap` defaults to zram with a writeback file at `/var/swap`. Under the read-only system that file would
live in RAM, so use plain zram:

    sudo mkdir -p /etc/rpi/swap.conf.d

    printf '[Main]\nMechanism=zram\n' | sudo tee /etc/rpi/swap.conf.d/80-zram-only.conf

    sudo reboot

After the reboot, `swapon --show` must list only `/dev/zram0`:

    swapon --show

(The setting removes an existing swap file. On an image that still uses `dphys-swapfile` instead, run
`sudo systemctl disable --now dphys-swapfile`.)

## 4. Build and install

Build on your computer; the Pi never needs Go. Run from the repo root. The `-o` is needed because the output name
`relay` would collide with the `relay/` directory.

Pi Zero W (32-bit ARMv6; `uname -m` on the Pi says `armv6l`):

    GOOS=linux GOARCH=arm GOARM=6 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o hhg-relay ./relay

Pi Zero 2 W with the 64-bit OS (`uname -m` says `aarch64`):

    GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o hhg-relay ./relay

(A Zero 2 W running the 32-bit OS takes the first command.) Copy it over and install it (untested):

    scp hhg-relay <user>@fp-rpi.local:/tmp/hhg-relay

    ssh -t <user>@fp-rpi.local sudo install -m 755 /tmp/hhg-relay /usr/local/bin/hhg-relay

## 5. Configure (untested)

Create the environment file, readable by root only, and fill it in:

    sudo install -m 600 /dev/null /etc/hhg-relay.env

    sudo nano /etc/hhg-relay.env

with these lines:

    RELAY_URL=https://<your-domain>/api/events
    RELAY_USER=<same as RELAY_USER in the VPS .env>
    RELAY_PASSWORD=<same as RELAY_PASSWORD in the VPS .env>
    RELAY_DEVICE_IDS=gshhgfp

- `RELAY_URL` must start with `https://`; the relay refuses to start otherwise.
- `RELAY_DEVICE_IDS` is the list of device IDs allowed to register, separated by commas or spaces.
- Optional: `RELAY_PORT` (UDP port, default `7600`) and `RELAY_DATA` (default `/data`).

Check that the VPS answers over HTTPS. Without credentials it must reply 401, which proves the request reached the
app:

    curl -s -o /dev/null -w '%{http_code}\n' -X POST https://<your-domain>/api/events

## 6. Run it under systemd (untested)

The relay runs as its own user, not root:

    sudo useradd --system --no-create-home --shell /usr/sbin/nologin hhg-relay

Create the unit:

    sudo nano /etc/systemd/system/hhg-relay.service

with:

    [Unit]
    Description=HHG attendance relay
    Wants=network-online.target
    After=network-online.target
    RequiresMountsFor=/data

    [Service]
    User=hhg-relay
    EnvironmentFile=/etc/hhg-relay.env
    ExecStart=/usr/local/bin/hhg-relay
    Restart=always
    RestartSec=10

    [Install]
    WantedBy=multi-user.target

Enable it, but do not start it yet: it needs `/data` first.

    sudo systemctl enable hhg-relay

`RequiresMountsFor=/data` makes the service wait for the `/data` mount and refuse to start if it fails. That is
deliberate. Under the read-only system, a missing partition would leave `/data` as a plain directory in RAM; the
relay would acknowledge events to the device, queue them in RAM, and lose them at the next reboot. See step 7 for how
the mount is set up so a missing partition does not stop the Pi from booting.

## 7. The `/data` partition (untested)

`/data` is a separate ext4 partition on the same card, outside the read-only system. It holds only the queue
(`/data/queue`) and the logs (`/data/logs`).

If you did step 1 as written, the card has free space after the root partition. Check:

    lsblk

You should see `mmcblk0p1` (about 512 MB, boot), `mmcblk0p2` (root, a few GB) and nothing else. Create the third
partition in the free space (all of it, which is plenty for a queue and logs):

    echo ',,L' | sudo sfdisk --append /dev/mmcblk0

    lsblk

`mmcblk0p3` must now be listed. If it is not, run `sudo partprobe /dev/mmcblk0` and look again. Make the file
system, with a label so `/etc/fstab` does not depend on a device name:

    sudo mkfs.ext4 -L hhgdata /dev/mmcblk0p3

Make the mount point, add the mount, and mount it:

    sudo mkdir /data

    echo 'LABEL=hhgdata /data ext4 defaults,noatime,nofail,x-systemd.device-timeout=10s 0 2' | sudo tee -a /etc/fstab

    sudo systemctl daemon-reload

    sudo mount /data

Let the relay's user own it, then start the relay:

    sudo chown hhg-relay: /data

    sudo systemctl start hhg-relay

    systemctl status hhg-relay

What the mount options do:

- `noatime` stops reads from writing to the card.
- `nofail` lets the Pi boot (and SSH work) even if the partition is missing or damaged, instead of dropping to
  emergency mode. `x-systemd.device-timeout=10s` makes systemd give up on the missing device after 10 seconds
  instead of the default 90.
- With the partition missing the relay does not start, because of `RequiresMountsFor=/data` in step 6. `systemctl
  status hhg-relay` then says a dependency failed. Fix the card, then run `sudo systemctl start hhg-relay`. The
  device keeps its unacknowledged events meanwhile and sends them again when the relay is back.

## 8. The device (untested)

Open the fingerprint device's web page and change its platform access settings (the menu names vary by firmware;
this is the same page you used before for the VPS):

- Platform access mode: EHome, protocol version 4.0.
- Server address: the Pi's IP from step 2.
- Port: `7600`.
- Device ID: `gshhgfp`.
- The EHome key can stay as it is; the relay does not check it.

Save. Within about a minute the relay's log shows the device registering:

    journalctl -u hhg-relay -n 20

Look for `device registered`. Then scan a fingerprint. The journal shows a `tap` line, and the VPS web app shows
the event on the Events page and a new "Last event received" time in the page header. The relay does not log a
successful post, only failures, so the web app is where you confirm delivery.

## 9. The read-only system (untested)

Under Raspberry Pi OS's overlay file system, the root file system is read-only and all writes go to RAM and vanish
at reboot, so the SD card is only written under `/data`. First finish everything that must persist: Wi-Fi (step 2),
swap (step 3), the relay and its files (steps 4 to 7), and check the relay works.

Turn it on. In `raspi-config` go to Performance Options, then Overlay File System, answer Yes to enabling the overlay
and Yes to write-protecting the boot partition. The same from the command line:

    sudo raspi-config nonint enable_overlayfs

    sudo raspi-config nonint enable_bootro

Then one more edit, which `raspi-config` does not do. By default `overlayroot` also puts every other mount in
`/etc/fstab` under an in-RAM overlay, which would make `/data` quietly volatile. `recurse=0` keeps `/data` a real,
writable partition:

    sudo sed -i 's/overlayroot=tmpfs /overlayroot=tmpfs:recurse=0 /' /boot/firmware/cmdline.txt

    cat /boot/firmware/cmdline.txt

`cmdline.txt` must stay a single line. Reboot, then check:

    sudo reboot

    findmnt /

    findmnt /data

`/` must show type `overlay`, and `/data` must show `/dev/mmcblk0p3`, `ext4` and `rw`. If `/data` shows `overlay`,
`recurse=0` did not take effect: turn the overlay off (below) and fix `cmdline.txt` before relying on the Pi. Also
check `sudo systemctl status hhg-relay`, and do a final test: scan a fingerprint, `sudo reboot`, and confirm
`ls /data/logs` still has the day's file after the reboot.

**Changing things later** (a new relay binary, `/etc/hhg-relay.env`, Wi-Fi, `apt`). Turn the overlay off, reboot,
change, then turn it on again. With the overlay on, `/boot/firmware` is read-only and `raspi-config` cannot change
it, so remount it writable once and edit `cmdline.txt` by hand:

    sudo mount -o remount,rw /boot/firmware

    sudo sed -i 's/overlayroot=tmpfs:recurse=0 //' /boot/firmware/cmdline.txt

    sudo reboot

Make your changes. For a new relay binary, repeat the `scp`/`install` from step 4 and run
`sudo systemctl restart hhg-relay`. To update the OS with `apt`, make `/boot/firmware` writable first:

    sudo raspi-config nonint disable_bootro

    sudo mount -o remount,rw /boot/firmware

Then turn the overlay back on, with all three commands, and reboot:

    sudo raspi-config nonint enable_overlayfs

    sudo raspi-config nonint enable_bootro

    sudo sed -i 's/overlayroot=tmpfs /overlayroot=tmpfs:recurse=0 /' /boot/firmware/cmdline.txt

`/data` is writable the whole time, with the overlay on or off, so the queue and logs are never lost to a reboot.
Anything else written while the overlay is on (a stray file, a package) is gone after the next reboot. The journal
(`journalctl`) lives in RAM too.

## 10. Checking on it

Live and recent log lines (the journal is in RAM, so it starts empty after each boot):

    journalctl -u hhg-relay -f

The relay also appends its log to one file per day, written once an hour and when the service stops, so up to an
hour of lines can be missing after a power cut. Files older than 30 days are deleted. The directory belongs to the
`hhg-relay` user:

    sudo ls /data/logs

    sudo tail /data/logs/$(date +%F).log

The queue holds events the VPS has not confirmed yet. Empty is healthy:

    sudo ls /data/queue

Events pile up there while the VPS or the Pi's internet is down, and drain by themselves: an hour after the last try,
and right after the next successful post. If the queue stays full while the VPS is up, `journalctl -u hhg-relay`
says why (`VPS answered 401` means `RELAY_USER` or `RELAY_PASSWORD` differs from the VPS `.env`).

## Sources

What this guide relied on:

- Raspberry Pi OS Trixie's boot resize (the initramfs script and the `resize` word in `cmdline.txt`; partition number
  2 is grown to the end of the card):
  [`resize_early`](https://github.com/RPi-Distro/raspberrypi-sys-mods/blob/pios/trixie/initramfs-tools/scripts/local-premount/resize_early),
  [`rpi-resize.service`](https://github.com/RPi-Distro/raspberrypi-sys-mods/blob/pios/trixie/debian/raspberrypi-sys-mods.rpi-resize.service),
  [image `cmdline.txt`](https://github.com/RPi-Distro/pi-gen/blob/master/stage1/00-boot-files/files/cmdline.txt),
  [image layout (512 MiB boot, then a root sized to the content)](https://github.com/RPi-Distro/pi-gen/blob/master/export-image/prerun.sh).
- Overlay file system in `raspi-config` (`enable_overlayfs`, `enable_bootro`, the `overlayroot=tmpfs` command-line
  word, and that boot write-protection cannot change while the overlay runs):
  [raspi-config, trixie branch](https://github.com/RPi-Distro/raspi-config/blob/trixie/raspi-config),
  [Raspberry Pi configuration docs](https://www.raspberrypi.com/documentation/computers/configuration.html).
- `overlayroot` and its `recurse` option (default 1 overlays other mounts, 0 leaves them writable; the kernel command
  line overrides `/etc/overlayroot.conf`):
  [overlayroot.conf](https://sources.debian.org/data/main/c/cloud-initramfs-tools/0.18.debian14/overlayroot/etc/overlayroot.conf),
  [init-bottom script](https://sources.debian.org/data/main/c/cloud-initramfs-tools/0.18.debian14/overlayroot/scripts/init-bottom/overlayroot).
- Trixie swap (`rpi-swap`, default zram plus a writeback file, `Mechanism=zram`):
  [rpi-swap README](https://github.com/raspberrypi/rpi-swap/blob/main/README.md),
  [swap.conf(5)](https://github.com/raspberrypi/rpi-swap/blob/main/man/man5/swap.conf.5).
- `nofail`, `x-systemd.device-timeout` and `RequiresMountsFor=`:
  [systemd.mount](https://www.freedesktop.org/software/systemd/man/latest/systemd.mount.html),
  [systemd.unit](https://www.freedesktop.org/software/systemd/man/latest/systemd.unit.html).
- Adding Wi-Fi and setting priority with `nmcli` on Trixie:
  [Classmethod, Raspberry Pi (Trixie) Wi-Fi with nmcli](https://dev.classmethod.jp/articles/raspberry-pi-trixie-wi-fi-nmcli/).

Not found in any source and therefore only reasoned from the above: the `sfdisk --append` partitioning, the
`recurse=0` edit on top of `raspi-config`'s `overlayroot=tmpfs`, and the offline shrink in step 1.
