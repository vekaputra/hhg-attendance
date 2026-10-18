// Command hhg-attendance records a Hikvision EHome 4.0 device's taps over UDP (ehome.go) and serves the
// attendance web app (web.go). "hhg-attendance adduser <name>" sets a login password, read from stdin.
// "hhg-attendance copy-db <sqlite-file>" copies an existing SQLite database into the configured store.
package main

import (
	"log/slog"
	"net/http"
	"os"
	"time"
)

// port is the EHome server port configured on the device.
var port = "7600"

// webAddr is where the web app listens; the reverse proxy in front of it handles HTTPS.
// It is loopback-only, so the proxy must run on this host.
var webAddr = "127.0.0.1:8080"

// Business timezone, e.g. "Asia/Jakarta". Fill this in.
var timezone = ""

// dbDriver is "sqlite" (development) or "mysql" (production).
var dbDriver = "sqlite"

// dbDSN is the SQLite file, or the MySQL DSN (see DEPLOY.md). The ATTENDANCE_DSN environment variable
// overrides it, so a MySQL password never has to be compiled into the binary.
var dbDSN = "attendance.db"

func main() {
	loc, err := time.LoadLocation(timezone)
	if timezone == "" || err != nil {
		slog.Error("set a valid business timezone in main.go", "timezone", timezone, "err", err)
		os.Exit(1)
	}
	if v := os.Getenv("ATTENDANCE_DSN"); v != "" {
		dbDSN = v
	}
	st, err := OpenStore(dbDriver, dbDSN, loc)
	if err != nil {
		slog.Error("open store", "driver", dbDriver, "err", err) // not the DSN: it may hold a password
		os.Exit(1)
	}

	if len(os.Args) == 3 && os.Args[1] == "adduser" {
		if err := runAddUser(st, os.Args[2], os.Stdin); err != nil {
			slog.Error("add user", "err", err)
			os.Exit(1)
		}
		slog.Info("user saved", "name", os.Args[2])
		os.Exit(0)
	}
	if len(os.Args) == 3 && os.Args[1] == "copy-db" {
		if err := copyDB(st, os.Args[2]); err != nil {
			slog.Error("copy db", "err", err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	go func() {
		slog.Error("ehome listener stopped", "err", listenEHome(":"+port, st))
		os.Exit(1)
	}()
	web := &http.Server{Addr: webAddr, Handler: newServer(st, loc, time.Now), ReadHeaderTimeout: 10 * time.Second}
	slog.Error("web server stopped", "err", web.ListenAndServe())
	os.Exit(1)
}
