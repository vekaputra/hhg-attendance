package main

import (
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
)

// knownEvents names the device's major/minor codes (hex, lower case) and gives each a pill tone.
var knownEvents = map[string]struct{ label, tone string }{
	"0x5/0x1":  {"Card accepted", "ok"},
	"0x5/0x6":  {"Card has no permission", "bad"},
	"0x5/0x9":  {"Card not recognised", "bad"},
	"0x5/0x15": {"Door unlocked", "muted"},
	"0x5/0x16": {"Door locked", "muted"},
	"0x5/0x26": {"Fingerprint matched", "ok"},
	"0x5/0x27": {"Fingerprint didn't match", "bad"},
	"0x5/0x31": {"Fingerprint not recognised", "bad"},
	"0x5/0x4b": {"Face matched", "ok"},
	"0x5/0x4c": {"Face didn't match", "bad"},
	"0x3/0x50": {"Device menu login", "muted"},
	"0x3/0x51": {"Device menu logout", "muted"},
}

// describeEvent returns the label and pill tone ("ok", "bad", "warn" or "muted") for a device's hex codes.
// An unknown minor falls back to its major's name.
func describeEvent(major, minor string) (label, tone string) {
	major = strings.ToLower(major)
	if k, ok := knownEvents[major+"/"+strings.ToLower(minor)]; ok {
		return k.label, k.tone
	}
	switch major {
	case "0x1":
		return "Alarm", "warn"
	case "0x2":
		return "Device problem", "warn"
	case "0x3":
		return "Operation", "muted"
	case "0x5":
		return "Access event", "muted"
	}
	return "Event", "muted"
}

// eventRow is one line of the events table. PersonURL is empty unless the employee exists.
type eventRow struct {
	Time, DeviceID, Label, Tone, Codes string
	EmployeeID, Name, PersonURL        string
	SerialNo                           string
}

// eventSummary counts the events shown by tone: Accepted is "ok", Failed is "bad" and Other is everything else,
// so the three add up to Total. Whether the event has an employee does not matter: a denied card is a failure.
type eventSummary struct{ Total, Accepted, Failed, Other int }

// deviceIDRE is what a device ID in the filter may look like, unless the ID sent an event that day.
var deviceIDRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

type eventsPage struct {
	pageBase
	Title             string // the day shown
	Day               string // dateFmt, kept by the device form
	Device            string // the device filtered on, or empty for all
	Devices           []string
	Prev, Next, Today navLink
	Summary           eventSummary
	Rows              []eventRow
}

// events lists the access events of one attendance day, newest first, for all devices or one.
func (srv *server) events(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	today := attendanceDate(srv.now().In(srv.loc))
	day := today
	if v := q.Get("day"); v != "" {
		var err error
		if day, err = time.ParseInLocation(dateFmt, v, srv.loc); err != nil {
			http.Error(w, "bad date", http.StatusBadRequest)
			return
		}
	}
	from := time.Date(day.Year(), day.Month(), day.Day(), dayStartHour, 0, 0, 0, srv.loc)
	events, err := srv.st.Events(from, from.AddDate(0, 0, 1), "")
	if err != nil {
		serverError(w, "events", err)
		return
	}
	// The filter offers the devices that sent an event that day.
	var devices []string
	for _, e := range events {
		if !slices.Contains(devices, e.DeviceID) {
			devices = append(devices, e.DeviceID)
		}
	}
	device := q.Get("device")
	if device != "" {
		if !slices.Contains(devices, device) {
			if !deviceIDRE.MatchString(device) {
				http.Error(w, "bad device", http.StatusBadRequest)
				return
			}
			devices = append(devices, device) // it sent nothing that day, but stays selectable
		}
		events = slices.DeleteFunc(events, func(e Event) bool { return e.DeviceID != device })
	}
	emps, err := srv.st.Employees(0)
	if err != nil {
		serverError(w, "employees", err)
		return
	}
	byID := make(map[string]Employee, len(emps))
	for _, emp := range emps {
		byID[emp.ID] = emp
	}

	link := func(label string, d time.Time) navLink {
		qs := url.Values{"day": {d.Format(dateFmt)}}
		if device != "" {
			qs.Set("device", device)
		}
		return navLink{Label: label, URL: "/events?" + qs.Encode()}
	}
	page := eventsPage{Title: day.Format("Mon 2 Jan 2006"), Day: day.Format(dateFmt), Device: device, Devices: devices,
		Prev: link("previous day", day.AddDate(0, 0, -1)), Next: link("next day", day.AddDate(0, 0, 1)), Today: link("Today", today)}
	for _, e := range events {
		label, tone := describeEvent(e.Major, e.Minor)
		row := eventRow{Time: e.Time.Format("15:04:05"), DeviceID: e.DeviceID, Label: label, Tone: tone,
			Codes: e.Major + " / " + e.Minor, EmployeeID: e.EmployeeID, SerialNo: e.SerialNo}
		if emp, ok := byID[e.EmployeeID]; ok {
			row.Name, row.PersonURL = emp.Name, personURL(emp.ID, day)
		}
		page.Rows = append(page.Rows, row)

		page.Summary.Total++
		switch tone {
		case "ok":
			page.Summary.Accepted++
		case "bad":
			page.Summary.Failed++
		default:
			page.Summary.Other++
		}
	}
	if page.pageBase, err = srv.base(r); err != nil {
		serverError(w, "last received", err)
		return
	}
	srv.render(w, http.StatusOK, "events.html", page)
}
