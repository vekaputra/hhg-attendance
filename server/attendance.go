package main

import (
	"slices"
	"time"
)

const (
	dayStartHour = 3               // an attendance day runs 03:00 to 02:59 the next morning
	doubleTapGap = 2 * time.Minute // a tap this close to the previous kept one is accidental
)

// attendanceDate returns midnight, in t's location, of the attendance day containing t.
func attendanceDate(t time.Time) time.Time {
	y, m, d := t.Date()
	day := time.Date(y, m, d, 0, 0, 0, 0, t.Location())
	if t.Hour() < dayStartHour {
		day = day.AddDate(0, 0, -1)
	}
	return day
}

// Window is a scheduled window on one attendance day. Shift is empty for fixed organisations.
// End may fall after midnight.
type Window struct {
	Start, End time.Time
	Shift      string
}

// windowOn builds a window from clockFmt strings on date; an end at or before the start is the next day.
// start and end must be valid clockFmt: they come from validated storage.
func windowOn(date time.Time, start, end, shift string) Window {
	onDate := func(clock string) time.Time {
		c, _ := time.Parse(clockFmt, clock)
		return time.Date(date.Year(), date.Month(), date.Day(), c.Hour(), c.Minute(), 0, 0, date.Location())
	}
	w := Window{Start: onDate(start), End: onDate(end), Shift: shift}
	if !w.End.After(w.Start) {
		w.End = w.End.AddDate(0, 0, 1)
	}
	return w
}

// checkInOut derives check-in and check-out from a day's taps; a zero result is missing.
// A lone tap is a check-in unless it falls at or after the window's midpoint.
func checkInOut(taps []time.Time, w *Window) (in, out time.Time) {
	sorted := slices.SortedFunc(slices.Values(taps), time.Time.Compare)
	var kept []time.Time
	for _, t := range sorted {
		if len(kept) == 0 || t.Sub(kept[len(kept)-1]) > doubleTapGap {
			kept = append(kept, t)
		}
	}
	switch {
	case len(kept) == 0:
		return time.Time{}, time.Time{}
	case len(kept) > 1:
		return kept[0], kept[len(kept)-1]
	case w == nil || kept[0].Before(w.Start.Add(w.End.Sub(w.Start)/2)):
		return kept[0], time.Time{}
	default:
		return time.Time{}, kept[0]
	}
}

// Fix is a manager correction; a zero field keeps the tap-derived value.
type Fix struct{ In, Out time.Time }

// Day is everything known about one person's attendance day.
// Window is nil when they aren't scheduled: weekend, off, holiday, leave or outside their active period.
// Roster is true when their organisation is a roster type.
type Day struct {
	Date                   time.Time
	Window                 *Window
	Leave, Holiday, Roster bool
	Taps                   []time.Time
	Fix                    *Fix
}

type Status string

const (
	StatusPending Status = ""
	StatusOff     Status = "off"
	StatusOnTime  Status = "on time"
	StatusLate    Status = "late"
	StatusAbsent  Status = "absent"
	StatusLeave   Status = "leave"
	StatusHoliday Status = "holiday"
	StatusPresent Status = "present" // worked, but lateness can't be judged
)

type Flag string

const (
	FlagNoCheckIn   Flag = "no check-in"
	FlagNoCheckOut  Flag = "no check-out"
	FlagUnrostered  Flag = "unrostered"
	FlagManagerEdit Flag = "manager edit"
)

// Result is the evaluated day. Actual and InShift are meaningful only when HasHours.
type Result struct {
	Date     time.Time
	Window   *Window
	In, Out  time.Time
	Status   Status
	LateBy   time.Duration
	Flags    []Flag
	Actual   time.Duration // out - in
	InShift  time.Duration // overlap of [in, out] with the window
	HasHours bool
}

// evaluate applies the attendance rules to a day. A person is late once they check in
// after window start + grace; a day with no taps is absent only once now is past the window end.
func evaluate(d Day, grace time.Duration, now time.Time) Result {
	r := Result{Date: d.Date, Window: d.Window}
	r.In, r.Out = checkInOut(d.Taps, d.Window)
	if d.Fix != nil {
		if !d.Fix.In.IsZero() {
			r.In = d.Fix.In
		}
		if !d.Fix.Out.IsZero() {
			r.Out = d.Fix.Out
		}
	}
	hasIn, hasOut := !r.In.IsZero(), !r.Out.IsZero()

	// A missing tap is only a flag once the day is over; until then the person may still be working.
	y, m, dd := d.Date.Date()
	if now.After(time.Date(y, m, dd+1, dayStartHour, 0, 0, 0, d.Date.Location())) {
		switch {
		case hasIn && !hasOut:
			r.Flags = append(r.Flags, FlagNoCheckOut)
		case hasOut && !hasIn:
			r.Flags = append(r.Flags, FlagNoCheckIn)
		}
	}

	switch {
	case d.Leave:
		r.Status = StatusLeave
	case d.Holiday:
		r.Status = StatusHoliday
	case d.Window == nil:
		r.Status = StatusOff
		if d.Roster && (len(d.Taps) > 0 || d.Fix != nil) {
			r.Flags = append(r.Flags, FlagUnrostered)
		}
	case !hasIn && !hasOut:
		if now.After(d.Window.End) {
			r.Status = StatusAbsent
		}
	case !hasIn:
		r.Status = StatusPresent
	case r.In.After(d.Window.Start.Add(grace)):
		r.Status = StatusLate
		r.LateBy = r.In.Sub(d.Window.Start)
	default:
		r.Status = StatusOnTime
	}

	if d.Fix != nil {
		r.Flags = append(r.Flags, FlagManagerEdit)
	}

	// A correction can put the check-in after a tap-derived check-out; that is no hours, not negative ones.
	if hasIn && hasOut && r.Out.After(r.In) {
		r.HasHours = true
		r.Actual = r.Out.Sub(r.In)
		if w := d.Window; w != nil {
			start, end := r.In, r.Out
			if w.Start.After(start) {
				start = w.Start
			}
			if w.End.Before(end) {
				end = w.End
			}
			r.InShift = max(0, end.Sub(start))
		}
	}
	return r
}
