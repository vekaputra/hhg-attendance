package main

import (
	"reflect"
	"testing"
	"time"
)

var testLoc = time.FixedZone("T", 7*3600)

// at parses "2006-01-02 15:04:05" in testLoc.
func at(s string) time.Time {
	t, err := time.ParseInLocation(stampFmt, s, testLoc)
	if err != nil {
		panic(err)
	}
	return t
}

// is reports whether got is the zero time (want == "") or equals the stamp want.
func is(got time.Time, want string) bool {
	if want == "" {
		return got.IsZero()
	}
	return got.Equal(at(want))
}

func TestAttendanceDate(t *testing.T) {
	for in, want := range map[string]string{
		"2026-10-20 01:00:00": "2026-10-19 00:00:00",
		"2026-10-20 03:00:00": "2026-10-20 00:00:00",
		"2026-11-01 02:59:00": "2026-10-31 00:00:00",
	} {
		if got := attendanceDate(at(in)); !got.Equal(at(want)) {
			t.Errorf("attendanceDate(%s) = %s, want %s", in, got.Format(stampFmt), want)
		}
	}
}

func TestCheckInOut(t *testing.T) {
	day := at("2026-10-05 00:00:00")
	office := windowOn(day, "08:30", "17:30", "")
	shift3 := windowOn(day, "15:00", "22:00", "3")

	tests := []struct {
		name            string
		taps            []string
		w               *Window
		wantIn, wantOut string
	}{
		{"in and out", []string{"2026-10-05 08:25:00", "2026-10-05 17:40:00"}, &office, "2026-10-05 08:25:00", "2026-10-05 17:40:00"},
		{"middle tap ignored", []string{"2026-10-05 08:20:00", "2026-10-05 12:00:00", "2026-10-05 17:35:00"}, &office, "2026-10-05 08:20:00", "2026-10-05 17:35:00"},
		{"double tap", []string{"2026-10-05 08:25:00", "2026-10-05 08:25:25"}, &office, "2026-10-05 08:25:00", ""},
		{"single tap after midpoint", []string{"2026-10-05 15:10:00"}, &office, "", "2026-10-05 15:10:00"},
		{"single tap at midpoint is out", []string{"2026-10-05 13:00:00"}, &office, "", "2026-10-05 13:00:00"},
		{"shift single tap before midpoint", []string{"2026-10-05 18:00:00"}, &shift3, "2026-10-05 18:00:00", ""},
		{"shift single tap after midpoint", []string{"2026-10-05 19:00:00"}, &shift3, "", "2026-10-05 19:00:00"},
		{"no window single tap", []string{"2026-10-05 19:00:00"}, nil, "2026-10-05 19:00:00", ""},
		{"out after midnight", []string{"2026-10-05 14:40:00", "2026-10-06 01:00:00"}, &shift3, "2026-10-05 14:40:00", "2026-10-06 01:00:00"},
		{"unsorted taps", []string{"2026-10-05 17:40:00", "2026-10-05 08:25:00"}, &office, "2026-10-05 08:25:00", "2026-10-05 17:40:00"},
		{"no taps", nil, &office, "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var taps []time.Time
			for _, s := range tc.taps {
				taps = append(taps, at(s))
			}
			orig := append([]time.Time(nil), taps...)
			in, out := checkInOut(taps, tc.w)
			for i := range taps {
				if !taps[i].Equal(orig[i]) {
					t.Fatal("checkInOut mutated its input")
				}
			}
			if !is(in, tc.wantIn) {
				t.Errorf("in = %v, want %q", in, tc.wantIn)
			}
			if !is(out, tc.wantOut) {
				t.Errorf("out = %v, want %q", out, tc.wantOut)
			}
		})
	}
}

func TestWindowOn(t *testing.T) {
	day := at("2026-10-05 00:00:00")
	w := windowOn(day, "15:00", "22:00", "3")
	if !w.Start.Equal(at("2026-10-05 15:00:00")) || !w.End.Equal(at("2026-10-05 22:00:00")) || w.Shift != "3" {
		t.Errorf("same-day window = %+v", w)
	}
	w = windowOn(day, "22:00", "06:00", "")
	if !w.End.Equal(at("2026-10-06 06:00:00")) {
		t.Errorf("overnight end = %v, want next day 06:00", w.End)
	}
}

func TestEvaluate(t *testing.T) {
	date := at("2026-10-05 00:00:00")
	office := windowOn(date, "08:30", "17:30", "")
	shift3 := windowOn(date, "15:00", "22:00", "3")
	stamps := func(ss ...string) []time.Time {
		var ts []time.Time
		for _, s := range ss {
			ts = append(ts, at(s))
		}
		return ts
	}
	const d = "2026-10-05 "

	tests := []struct {
		name            string
		day             Day
		now             string // empty means the morning after
		wantIn, wantOut string
		status          Status
		lateBy          time.Duration
		flags           []Flag
		actual, inShift time.Duration // actual != 0 means HasHours
	}{
		{"on time", Day{Window: &office, Taps: stamps(d+"08:40:00", d+"17:31:00")}, "",
			d + "08:40:00", d + "17:31:00", StatusOnTime, 0, nil, 8*time.Hour + 51*time.Minute, 8*time.Hour + 50*time.Minute},
		{"late", Day{Window: &office, Taps: stamps(d+"08:41:00", d+"17:30:00")}, "",
			d + "08:41:00", d + "17:30:00", StatusLate, 11 * time.Minute, nil, 8*time.Hour + 49*time.Minute, 8*time.Hour + 49*time.Minute},
		{"absent", Day{Window: &office}, d + "18:00:00", "", "", StatusAbsent, 0, nil, 0, 0},
		{"pending", Day{Window: &office}, d + "10:00:00", "", "", StatusPending, 0, nil, 0, 0},
		{"out only", Day{Window: &office, Taps: stamps(d + "16:00:00")}, "",
			"", d + "16:00:00", StatusPresent, 0, []Flag{FlagNoCheckIn}, 0, 0},
		{"in only", Day{Window: &office, Taps: stamps(d + "08:35:00")}, "",
			d + "08:35:00", "", StatusOnTime, 0, []Flag{FlagNoCheckOut}, 0, 0},
		{"in only during the day", Day{Date: date, Window: &office, Taps: stamps(d + "08:25:00")}, d + "10:00:00",
			d + "08:25:00", "", StatusOnTime, 0, nil, 0, 0},
		{"leave", Day{Leave: true, Taps: stamps(d+"09:00:00", d+"17:00:00")}, "",
			d + "09:00:00", d + "17:00:00", StatusLeave, 0, nil, 8 * time.Hour, 0},
		{"holiday", Day{Holiday: true}, "", "", "", StatusHoliday, 0, nil, 0, 0},
		{"unrostered", Day{Roster: true, Taps: stamps(d+"11:00:00", d+"20:00:00")}, "",
			d + "11:00:00", d + "20:00:00", StatusOff, 0, []Flag{FlagUnrostered}, 9 * time.Hour, 0},
		{"off without roster", Day{Taps: stamps(d+"11:00:00", d+"20:00:00")}, "",
			d + "11:00:00", d + "20:00:00", StatusOff, 0, nil, 9 * time.Hour, 0},
		{"shift past midnight", Day{Window: &shift3, Taps: stamps(d+"14:40:00", "2026-10-06 01:00:00")}, "",
			d + "14:40:00", "2026-10-06 01:00:00", StatusOnTime, 0, nil, 10*time.Hour + 20*time.Minute, 7 * time.Hour},
		{"fix in over tap out", Day{Window: &office, Taps: stamps(d + "17:35:00"), Fix: &Fix{In: at(d + "08:30:00")}}, "",
			d + "08:30:00", d + "17:35:00", StatusOnTime, 0, []Flag{FlagManagerEdit}, 9*time.Hour + 5*time.Minute, 9 * time.Hour},
		{"fix in after tap out", Day{Window: &office, Taps: stamps(d+"08:00:00", d+"17:00:00"), Fix: &Fix{In: at(d + "18:00:00")}}, "",
			d + "18:00:00", d + "17:00:00", StatusLate, 9*time.Hour + 30*time.Minute, []Flag{FlagManagerEdit}, 0, 0},
		{"fix without taps", Day{Window: &office, Fix: &Fix{In: at(d + "08:30:00"), Out: at(d + "17:30:00")}}, d + "20:00:00",
			d + "08:30:00", d + "17:30:00", StatusOnTime, 0, []Flag{FlagManagerEdit}, 9 * time.Hour, 9 * time.Hour},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			now := at("2026-10-06 12:00:00")
			if tc.now != "" {
				now = at(tc.now)
			}
			got := evaluate(tc.day, 10*time.Minute, now)
			if !is(got.In, tc.wantIn) || !is(got.Out, tc.wantOut) {
				t.Errorf("in/out = %v / %v, want %q / %q", got.In, got.Out, tc.wantIn, tc.wantOut)
			}
			if got.Status != tc.status || got.LateBy != tc.lateBy {
				t.Errorf("status = %q late by %v, want %q late by %v", got.Status, got.LateBy, tc.status, tc.lateBy)
			}
			if !reflect.DeepEqual(got.Flags, tc.flags) {
				t.Errorf("flags = %q, want %q", got.Flags, tc.flags)
			}
			if got.Actual != tc.actual || got.InShift != tc.inShift || got.HasHours != (tc.actual != 0) {
				t.Errorf("hours = %v in shift %v (has %v), want %v in shift %v", got.Actual, got.InShift, got.HasHours, tc.actual, tc.inShift)
			}
		})
	}
}
