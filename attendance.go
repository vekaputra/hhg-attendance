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
