package main

import (
	"slices"
	"strconv"
	"strings"
	"time"
)

const maxPaidHours = 8

// payPeriod is the pay period named for month's month: the 26th of the month before through the 25th.
func payPeriod(month time.Time) (first, last time.Time) {
	y, m, _ := month.Date()
	return time.Date(y, m-1, 26, 0, 0, 0, 0, month.Location()), time.Date(y, m, 25, 0, 0, 0, 0, month.Location())
}

// payMonthOf is the month whose pay period contains day.
func payMonthOf(day time.Time) time.Time {
	if day.Day() > 25 {
		day = monthOf(day).AddDate(0, 1, 0)
	}
	return monthOf(day)
}

// defaultPayMonth is the period opened by default: the calendar month of today's attendance day.
func defaultPayMonth(today time.Time) time.Time { return monthOf(today) }

type ptState int

const (
	ptNone    ptState = iota // – no in and no out
	ptHours                  // both in and out
	ptMissing                // ? one side missing and the day is over, or check-out not after check-in
	ptOpen                   // … one side missing and the day is still running
)

type session struct{ Start, End time.Time }

type ptDay struct {
	Date     time.Time
	State    ptState
	In, Out  time.Time     // zero when missing
	Sessions []session     // in → out split around merged breaks; empty unless ptHours
	Worked   time.Duration // after breaks, rounded to the nearest minute
	Break    time.Duration // merged break time inside [In, Out]
	Hours    int           // rounded up, capped
	Capped   bool          // the uncapped rounded-up hours exceeded 8
	Edited   bool          // a manager correction was used
	Pay      int64
}

// partTimeDay turns one attendance result and that day's breaks into paid hours.
func partTimeDay(r Result, breaks []Break, wage int64) ptDay {
	d := ptDay{Date: r.Date, In: r.In, Out: r.Out, Edited: slices.Contains(r.Flags, FlagManagerEdit)}
	switch {
	case r.In.IsZero() && r.Out.IsZero():
		d.State = ptNone
	case r.In.IsZero() || r.Out.IsZero():
		d.State = ptOpen
		if slices.Contains(r.Flags, FlagNoCheckIn) || slices.Contains(r.Flags, FlagNoCheckOut) {
			d.State = ptMissing
		}
	case !r.Out.After(r.In): // both known but unusable, so one of them needs fixing
		d.State = ptMissing
	default:
		d.State = ptHours
	}
	if !r.HasHours {
		return d
	}

	// Clip breaks to [In, Out], then merge overlaps; the gaps between them are the sessions.
	var gaps []session
	for _, b := range breaks {
		g := session{b.Start, b.End}
		if g.Start.Before(r.In) {
			g.Start = r.In
		}
		if g.End.After(r.Out) {
			g.End = r.Out
		}
		gaps = append(gaps, g)
	}
	slices.SortFunc(gaps, func(a, b session) int { return a.Start.Compare(b.Start) })
	cur := r.In
	for _, g := range gaps {
		if !g.End.After(g.Start) || !g.End.After(cur) {
			continue
		}
		if g.Start.After(cur) {
			d.Sessions = append(d.Sessions, session{cur, g.Start})
			d.Break += g.End.Sub(g.Start)
		} else {
			d.Break += g.End.Sub(cur)
		}
		cur = g.End
	}
	if cur.Before(r.Out) {
		d.Sessions = append(d.Sessions, session{cur, r.Out})
	}

	d.Worked = max((r.Out.Sub(r.In) - d.Break).Round(time.Minute), 0)
	hours := int((d.Worked + time.Hour - 1) / time.Hour)
	d.Hours, d.Capped = min(hours, maxPaidHours), hours > maxPaidHours
	d.Pay = dayPay(wage, d.Hours)
	return d
}

// dayPay is wage*hours/8, rounded to the nearest rupiah.
func dayPay(wage int64, hours int) int64 { return (wage*int64(hours) + 4) / 8 }

// rupiah formats n as "Rp 3.445.000".
func rupiah(n int64) string {
	s := strconv.FormatInt(n, 10)
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte('.')
		}
		b.WriteRune(c)
	}
	return "Rp " + b.String()
}
