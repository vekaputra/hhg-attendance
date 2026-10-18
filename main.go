// Command hhg-attendance answers Hikvision EHome 4.0 registration over UDP and
// logs what the device sends (ehome.go).
package main

import (
	"io"
	"log"
	"log/slog"
	"os"
)

// port is the EHome server port configured on the device.
var port = "7600"

func main() {
	f, err := os.OpenFile("ehome.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		slog.Error("open log file", "err", err)
		os.Exit(1)
	}
	// slog's default logger writes through the log package, so this tees every log line to the file.
	log.SetOutput(io.MultiWriter(os.Stderr, f))

	slog.Error("ehome listener stopped", "err", listenEHome(":"+port))
	os.Exit(1)
}
