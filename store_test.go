package main

import (
	"reflect"
	"testing"
	"time"
)

func TestOpenStoreSeedsOnce(t *testing.T) {
	path := t.TempDir() + "/a.db"
	check := func(s *Store) {
		t.Helper()
		orgs, err := s.Orgs()
		if err != nil {
			t.Fatal(err)
		}
		if len(orgs) != 2 {
			t.Fatalf("got %d orgs, want 2: %+v", len(orgs), orgs)
		}
		office, cafe := orgs[0], orgs[1]
		wantOffice := Org{ID: office.ID, Name: "Office", Kind: "fixed", Start: "08:30", End: "17:30", Weekdays: "12345"}
		if office != wantOffice {
			t.Errorf("office = %+v, want %+v", office, wantOffice)
		}
		wantCafe := Org{ID: cafe.ID, Name: "Cafe", Kind: "roster"}
		if cafe != wantCafe {
			t.Errorf("cafe = %+v, want %+v", cafe, wantCafe)
		}
		shifts, err := s.Shifts(cafe.ID)
		if err != nil {
			t.Fatal(err)
		}
		var got [][3]string
		for _, sh := range shifts {
			got = append(got, [3]string{sh.Name, sh.Start, sh.End})
		}
		want := [][3]string{{"1", "06:00", "15:00"}, {"2", "11:00", "20:00"}, {"3", "15:00", "22:00"}}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("cafe shifts = %v, want %v", got, want)
		}
		if shifts, err := s.Shifts(office.ID); err != nil || len(shifts) != 0 {
			t.Errorf("office shifts = %v, %v; want none", shifts, err)
		}
	}

	s, err := OpenStore(path, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	check(s)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = OpenStore(path, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	check(s)
}
