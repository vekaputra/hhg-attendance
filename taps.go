package main

import "time"

// Tap is one door event for an employee. SerialNo is the device's event counter, so (DeviceID, SerialNo, Time) identifies a tap across resends.
type Tap struct {
	DeviceID, SerialNo, EmployeeID string
	Time                           time.Time
}

// InsertTap stores a tap; one the device already sent (a resend) is silently ignored.
func (s *Store) InsertTap(t Tap, receivedAt time.Time) error {
	_, err := s.db.Exec(`INSERT INTO taps (device_id, serial_no, device_time, employee_id, received_at) VALUES (?, ?, ?, ?, ?)`,
		t.DeviceID, t.SerialNo, t.Time.In(s.loc).Format(stampFmt), t.EmployeeID, receivedAt.In(s.loc).Format(stampFmt))
	if isDuplicate(err) {
		return nil
	}
	return err
}

// Taps returns the employee's device times with from <= t < to, ascending.
func (s *Store) Taps(employeeID string, from, to time.Time) ([]time.Time, error) {
	rows, err := s.db.Query(`SELECT device_time FROM taps WHERE employee_id = ? AND device_time >= ? AND device_time < ? ORDER BY device_time`,
		employeeID, from.In(s.loc).Format(stampFmt), to.In(s.loc).Format(stampFmt))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var times []time.Time
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		t, err := time.ParseInLocation(stampFmt, v, s.loc)
		if err != nil {
			return nil, err
		}
		times = append(times, t)
	}
	return times, rows.Err()
}

// TouchDevice records that the device was heard from at the given time.
func (s *Store) TouchDevice(deviceID string, at time.Time) error {
	contact := at.In(s.loc).Format(stampFmt)
	_, err := s.db.Exec(`INSERT INTO devices (device_id, last_contact) VALUES (?, ?)`, deviceID, contact)
	if isDuplicate(err) {
		_, err = s.db.Exec(`UPDATE devices SET last_contact = ? WHERE device_id = ?`, contact, deviceID)
	}
	return err
}

// DeviceContacts returns each device's last contact time by device ID.
func (s *Store) DeviceContacts() (map[string]time.Time, error) {
	rows, err := s.db.Query(`SELECT device_id, last_contact FROM devices`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	contacts := map[string]time.Time{}
	for rows.Next() {
		var id, v string
		if err := rows.Scan(&id, &v); err != nil {
			return nil, err
		}
		t, err := time.ParseInLocation(stampFmt, v, s.loc)
		if err != nil {
			return nil, err
		}
		contacts[id] = t
	}
	return contacts, rows.Err()
}
