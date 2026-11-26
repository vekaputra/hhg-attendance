package main

import (
	"reflect"
	"testing"
	"time"
)

func TestBreaksRoundTrip(t *testing.T) {
	s := newTestStore(t)
	org := mustGet(s.Orgs())[0].ID
	if err := s.AddEmployee(Employee{ID: "P1", Name: "Pia", OrgID: org, ActiveFrom: "2026-10-01", PartTime: true}); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 6, 9, 0, 0, 0, time.UTC)
	day := func(d int) time.Time { return time.Date(2026, 11, d, 0, 0, 0, 0, time.UTC) }
	first := Break{EmployeeID: "P1", Day: day(2), Start: day(2).Add(10 * time.Hour), End: day(2).Add(13 * time.Hour), By: "admin", At: at}
	later := Break{EmployeeID: "P1", Day: day(5), Start: day(5).Add(10 * time.Hour), End: day(5).Add(11 * time.Hour), By: "admin", At: at}
	var err error
	if first.ID, err = s.AddBreak(first); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AddBreak(later); err != nil {
		t.Fatal(err)
	}

	got, err := s.Breaks(day(1), day(3))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []Break{first}) {
		t.Fatalf("Breaks = %+v, want %+v", got, first)
	}

	if err := s.DeleteBreak(first.ID); err != nil {
		t.Fatal(err)
	}
	if got, err = s.Breaks(day(1), day(3)); err != nil || len(got) != 0 {
		t.Fatalf("after delete: %+v, %v", got, err)
	}
}

func TestPartTimeWage(t *testing.T) {
	s := newTestStore(t)
	for _, step := range []struct{ set, want int64 }{{0, 130000}, {150000, 150000}, {140000, 140000}} {
		if step.set != 0 {
			if err := s.SetPartTimeWage(step.set); err != nil {
				t.Fatal(err)
			}
		}
		if got, err := s.PartTimeWage(); err != nil || got != step.want {
			t.Fatalf("after set %d: wage = %d, %v; want %d", step.set, got, err, step.want)
		}
	}
}
