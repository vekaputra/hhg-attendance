package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"hhg-attendance/internal/wire"
)

// ingestServer sets the relay credentials (restored after the test) and returns the app on st.
func ingestServer(t *testing.T, st *Store) http.Handler {
	t.Helper()
	oldUser, oldPassword := relayUser, relayPassword
	relayUser, relayPassword = "relay", "s3cret"
	t.Cleanup(func() { relayUser, relayPassword = oldUser, oldPassword })
	return newServer(st, time.UTC, time.Now)
}

func post(h http.Handler, body []byte, auth func(*http.Request)) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/api/events", bytes.NewReader(body))
	if auth != nil {
		auth(r)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func postMsg(t *testing.T, h http.Handler, m wire.Message) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return post(h, body, func(r *http.Request) { r.SetBasicAuth("relay", "s3cret") })
}

func acsMsg() wire.Message {
	m := wire.Message{Command: "ACS", DeviceID: "gshhgfp", SerialNo: "83", Time: "2026-10-18 10:42:03", Major: "0x5", Minor: "0x26", EmployeeNo: "HHG0001"}
	m.Hash = wire.EventHash(m)
	return m
}

func storedEvents(t *testing.T, st *Store) []Event {
	t.Helper()
	got, err := st.Events(time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC), "")
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestIngestAuth(t *testing.T) {
	st := newTestStore(t)
	h := ingestServer(t, st)
	body, _ := json.Marshal(acsMsg())
	for name, auth := range map[string]func(*http.Request){
		"no credentials": nil,
		"wrong password": func(r *http.Request) { r.SetBasicAuth("relay", "nope") },
		"wrong user":     func(r *http.Request) { r.SetBasicAuth("other", "s3cret") },
	} {
		if w := post(h, body, auth); w.Code != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want 401", name, w.Code)
		}
	}

	// Unconfigured credentials must never match an empty login.
	relayUser, relayPassword = "", ""
	if w := post(h, body, func(r *http.Request) { r.SetBasicAuth("", "") }); w.Code != http.StatusUnauthorized {
		t.Errorf("unset credentials: status = %d, want 401", w.Code)
	}
	if got := storedEvents(t, st); len(got) != 0 {
		t.Errorf("events = %v, want none", got)
	}
}

func TestIngestStoresTap(t *testing.T) {
	st := newTestStore(t)
	w := postMsg(t, ingestServer(t, st), acsMsg())
	if w.Code != http.StatusOK || w.Body.Len() != 0 {
		t.Fatalf("status = %d, body = %q; want 200, empty", w.Code, w.Body)
	}
	want := []time.Time{time.Date(2026, 10, 18, 10, 42, 3, 0, time.UTC)}
	if got := tapsOn(t, st, "HHG0001"); !reflect.DeepEqual(got, want) {
		t.Errorf("taps = %v, want %v", got, want)
	}
	if got := storedEvents(t, st); len(got) != 1 || got[0].Major != "0x5" {
		t.Errorf("events = %+v, want one with major 0x5", got)
	}
}

func TestIngestResendIsOneRow(t *testing.T) {
	st := newTestStore(t)
	h := ingestServer(t, st)
	for range 2 {
		if w := postMsg(t, h, acsMsg()); w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", w.Code)
		}
	}
	if got := storedEvents(t, st); len(got) != 1 {
		t.Errorf("events = %+v, want one", got)
	}
	if got := tapsOn(t, st, "HHG0001"); len(got) != 1 {
		t.Errorf("taps = %v, want one", got)
	}
}

func TestIngestEventWithoutEmployee(t *testing.T) {
	st := newTestStore(t)
	m := acsMsg()
	m.EmployeeNo = ""
	m.Hash = wire.EventHash(m)
	if w := postMsg(t, ingestServer(t, st), m); w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if got := storedEvents(t, st); len(got) != 1 {
		t.Errorf("events = %+v, want one", got)
	}
	if got := tapsOn(t, st, ""); len(got) != 0 {
		t.Errorf("taps = %v, want none", got)
	}
}

// Resending an unreadable time wouldn't fix it, so it is answered 200 and dropped.
func TestIngestBadTimeAccepted(t *testing.T) {
	st := newTestStore(t)
	m := acsMsg()
	m.Time = "garbage"
	m.Hash = wire.EventHash(m)
	if w := postMsg(t, ingestServer(t, st), m); w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if got := storedEvents(t, st); len(got) != 0 {
		t.Errorf("events = %+v, want none", got)
	}
	if got := tapsOn(t, st, "HHG0001"); len(got) != 0 {
		t.Errorf("taps = %v, want none", got)
	}
}

func TestIngestBadHash(t *testing.T) {
	st := newTestStore(t)
	m := acsMsg()
	m.Hash = "deadbeef"
	if w := postMsg(t, ingestServer(t, st), m); w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if got := storedEvents(t, st); len(got) != 0 {
		t.Errorf("events = %+v, want none", got)
	}
}

func TestIngestBadBody(t *testing.T) {
	h := ingestServer(t, newTestStore(t))
	for name, body := range map[string]string{
		"not json":       "nope",
		"no command":     `{"device_id":"gshhgfp"}`,
		"no device id":   `{"command":"ACS"}`,
		"truncated json": `{"command":"ACS","device_id":"x"`,
	} {
		w := post(h, []byte(body), func(r *http.Request) { r.SetBasicAuth("relay", "s3cret") })
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", name, w.Code)
		}
	}
}

func TestIngestNonACS(t *testing.T) {
	st := newTestStore(t)
	w := postMsg(t, ingestServer(t, st), wire.Message{Command: "ALARM", DeviceID: "gshhgfp", Raw: "<PPVSPMessage/>"})
	if w.Code != http.StatusOK || w.Body.Len() != 0 {
		t.Fatalf("status = %d, body = %q; want 200, empty", w.Code, w.Body)
	}
	if got := storedEvents(t, st); len(got) != 0 {
		t.Errorf("events = %+v, want none", got)
	}
}

// A store error must not be 200, or the relay would delete an event that was never stored.
func TestIngestStoreDown(t *testing.T) {
	st := newTestStore(t)
	h := ingestServer(t, st)
	st.Close()
	if w := postMsg(t, h, acsMsg()); w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.Code)
	}
}
