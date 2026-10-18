package main

import (
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
