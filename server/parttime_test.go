package main

import (
	"slices"
	"testing"
	"time"
)

func day(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

func TestPayPeriod(t *testing.T) {
	for _, c := range []struct{ month, first, last time.Time }{
		{day(2026, 11, 14), day(2026, 10, 26), day(2026, 11, 25)},
		{day(2027, 1, 1), day(2026, 12, 26), day(2027, 1, 25)},
	} {
		if f, l := payPeriod(c.month); !f.Equal(c.first) || !l.Equal(c.last) {
			t.Errorf("payPeriod(%v) = %v..%v, want %v..%v", c.month, f, l, c.first, c.last)
		}
	}
}

func TestPayMonthOf(t *testing.T) {
	for _, c := range []struct{ in, want time.Time }{
		{day(2026, 11, 25), day(2026, 11, 1)},
		{day(2026, 11, 26), day(2026, 12, 1)},
		{day(2026, 12, 31), day(2027, 1, 1)},
	} {
		if got := payMonthOf(c.in); !got.Equal(c.want) {
			t.Errorf("payMonthOf(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestDefaultPayMonth(t *testing.T) {
	for _, c := range []struct{ in, want time.Time }{
		{day(2026, 11, 27), day(2026, 11, 1)},
		{day(2026, 12, 1), day(2026, 12, 1)},
	} {
		if got := defaultPayMonth(c.in); !got.Equal(c.want) {
			t.Errorf("defaultPayMonth(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestPartTimeDay(t *testing.T) {
	at := func(d, h, m, s int) time.Time { return time.Date(2026, 11, d, h, m, s, 0, time.UTC) }
	brk := func(sh, sm, eh, em int) Break { return Break{Start: at(10, sh, sm, 0), End: at(10, eh, em, 0)} }
	res := func(in, out time.Time, flags ...Flag) Result {
		return Result{Date: day(2026, 11, 10), In: in, Out: out, Flags: flags, HasHours: !in.IsZero() && !out.IsZero() && out.After(in)}
	}
	h := time.Hour
	cases := []struct {
		name     string
		r        Result
		breaks   []Break
		state    ptState
		worked   time.Duration
		brk      time.Duration
		hours    int
		capped   bool
		edited   bool
		pay      int64
		sessions []session
	}{
		{name: "over the cap", r: res(at(10, 12, 40, 59), at(10, 22, 23, 22)), state: ptHours,
			worked: 9*h + 42*time.Minute, hours: 8, capped: true, pay: 130000,
			sessions: []session{{at(10, 12, 40, 59), at(10, 22, 23, 22)}}},
		{name: "one break", r: res(at(10, 6, 0, 0), at(10, 15, 0, 0)), breaks: []Break{brk(10, 0, 13, 0)},
			state: ptHours, worked: 6 * h, brk: 3 * h, hours: 6, pay: 97500,
			sessions: []session{{at(10, 6, 0, 0), at(10, 10, 0, 0)}, {at(10, 13, 0, 0), at(10, 15, 0, 0)}}},
		{name: "overlapping breaks merge", r: res(at(10, 6, 0, 0), at(10, 15, 0, 0)),
			breaks: []Break{brk(10, 0, 13, 0), brk(10, 0, 13, 0), brk(12, 0, 14, 0)},
			state:  ptHours, worked: 5 * h, brk: 4 * h, hours: 5, pay: 81250,
			sessions: []session{{at(10, 6, 0, 0), at(10, 10, 0, 0)}, {at(10, 14, 0, 0), at(10, 15, 0, 0)}}},
		{name: "unsorted overlapping breaks merge", r: res(at(10, 6, 0, 0), at(10, 15, 0, 0)),
			breaks: []Break{brk(12, 0, 14, 0), brk(10, 0, 13, 0), brk(10, 0, 13, 0)},
			state:  ptHours, worked: 5 * h, brk: 4 * h, hours: 5, pay: 81250,
			sessions: []session{{at(10, 6, 0, 0), at(10, 10, 0, 0)}, {at(10, 14, 0, 0), at(10, 15, 0, 0)}}},
		{name: "nested break", r: res(at(10, 6, 0, 0), at(10, 15, 0, 0)),
			breaks: []Break{brk(10, 0, 14, 0), brk(11, 0, 12, 0)},
			state:  ptHours, worked: 5 * h, brk: 4 * h, hours: 5, pay: 81250,
			sessions: []session{{at(10, 6, 0, 0), at(10, 10, 0, 0)}, {at(10, 14, 0, 0), at(10, 15, 0, 0)}}},
		{name: "break clipped to span", r: res(at(10, 6, 0, 0), at(10, 15, 0, 0)), breaks: []Break{brk(5, 0, 7, 0)},
			state: ptHours, worked: 8 * h, brk: h, hours: 8, pay: 130000,
			sessions: []session{{at(10, 7, 0, 0), at(10, 15, 0, 0)}}},
		{name: "seconds round to minute", r: res(at(10, 6, 0, 0), at(10, 14, 0, 20)),
			state: ptHours, worked: 8 * h, hours: 8, pay: 130000,
			sessions: []session{{at(10, 6, 0, 0), at(10, 14, 0, 20)}}},
		{name: "rounding up", r: res(at(10, 6, 0, 0), at(10, 12, 0, 40)),
			state: ptHours, worked: 6*h + time.Minute, hours: 7, pay: 113750,
			sessions: []session{{at(10, 6, 0, 0), at(10, 12, 0, 40)}}},
		{name: "overnight break", r: res(at(10, 18, 0, 0), at(11, 1, 30, 0)),
			breaks: []Break{{Start: at(10, 23, 0, 0), End: at(11, 0, 30, 0)}},
			state:  ptHours, worked: 6 * h, brk: 90 * time.Minute, hours: 6, pay: 97500,
			sessions: []session{{at(10, 18, 0, 0), at(10, 23, 0, 0)}, {at(11, 0, 30, 0), at(11, 1, 30, 0)}}},
		{name: "break covers span", r: res(at(10, 6, 0, 0), at(10, 8, 0, 0)), breaks: []Break{brk(5, 0, 9, 0)},
			state: ptHours, brk: 2 * h},
		{name: "missing", r: res(at(10, 6, 0, 0), time.Time{}, FlagNoCheckOut), state: ptMissing},
		{name: "open", r: res(at(10, 6, 0, 0), time.Time{}), state: ptOpen},
		{name: "check-out before check-in", r: res(at(10, 15, 0, 0), at(10, 9, 0, 0)), state: ptMissing},
		{name: "check-out equals check-in", r: res(at(10, 9, 0, 0), at(10, 9, 0, 0)), state: ptMissing},
		{name: "no taps", r: res(time.Time{}, time.Time{}), state: ptNone},
		{name: "edited", r: res(at(10, 6, 0, 0), at(10, 8, 0, 0), FlagManagerEdit), state: ptHours,
			worked: 2 * h, hours: 2, edited: true, pay: 32500,
			sessions: []session{{at(10, 6, 0, 0), at(10, 8, 0, 0)}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g := partTimeDay(c.r, c.breaks, 130000)
			if g.State != c.state || g.Worked != c.worked || g.Break != c.brk || g.Hours != c.hours ||
				g.Capped != c.capped || g.Edited != c.edited || g.Pay != c.pay || !slices.Equal(g.Sessions, c.sessions) {
				t.Errorf("got %+v\nwant state %v worked %v break %v hours %d capped %v edited %v pay %d sessions %v",
					g, c.state, c.worked, c.brk, c.hours, c.capped, c.edited, c.pay, c.sessions)
			}
		})
	}
}

func TestDayPay(t *testing.T) {
	for _, c := range []struct {
		wage  int64
		hours int
		want  int64
	}{{130000, 6, 97500}, {130000, 7, 113750}, {130001, 3, 48750}, {130000, 0, 0}} {
		if got := dayPay(c.wage, c.hours); got != c.want {
			t.Errorf("dayPay(%d, %d) = %d, want %d", c.wage, c.hours, got, c.want)
		}
	}
}

func TestRupiah(t *testing.T) {
	for n, want := range map[int64]string{3445000: "Rp 3.445.000", 97500: "Rp 97.500", 0: "Rp 0"} {
		if got := rupiah(n); got != want {
			t.Errorf("rupiah(%d) = %q, want %q", n, got, want)
		}
	}
}
