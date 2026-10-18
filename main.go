// Command hhg-attendance answers Hikvision EHome 4.0 registration over UDP and
// logs what the device sends (ehome.go).
package main

import (
	"io"
	"log"
	"log/slog"
	"os"
	"time"
)

// port is the EHome server port configured on the device.
var port = "7600"

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
	store, err := OpenStore(dbPath, loc)
	if err != nil {
		slog.Error("open store", "path", dbPath, "err", err)
		os.Exit(1)
	}
	defer store.Close()

	slog.Error("ehome listener stopped", "err", listenEHome(":"+port, store))
	os.Exit(1)
}
