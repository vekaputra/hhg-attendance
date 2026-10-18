// Command hhg-attendance records a Hikvision EHome 4.0 device's taps over UDP (ehome.go) and serves the
// attendance web app (web.go). "hhg-attendance adduser <name>" sets a login password, read from stdin.
package main

import (
	"io"
	"log"
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

// dbPath is the SQLite database file.
var dbPath = "attendance.db"

func main() {
	f, err := os.OpenFile("ehome.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		slog.Error("open log file", "err", err)
		os.Exit(1)
	}
	// slog's default logger writes through the log package, so this tees every log line to the file.
	log.SetOutput(io.MultiWriter(os.Stderr, f))

	loc, err := time.LoadLocation(timezone)
	if timezone == "" || err != nil {
		slog.Error("set a valid business timezone in main.go", "timezone", timezone, "err", err)
		os.Exit(1)
	}
	st, err := OpenStore(dbPath, loc)
	if err != nil {
		slog.Error("open store", "path", dbPath, "err", err)
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

	go func() {
		slog.Error("ehome listener stopped", "err", listenEHome(":"+port, st))
		os.Exit(1)
	}()
	web := &http.Server{Addr: webAddr, Handler: newServer(st, loc, time.Now), ReadHeaderTimeout: 10 * time.Second}
	slog.Error("web server stopped", "err", web.ListenAndServe())
	os.Exit(1)
}
