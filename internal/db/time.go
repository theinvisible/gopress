package db

import (
	"database/sql/driver"
	"fmt"
	"time"
)

// TimeLayout is the WordPress-compatible storage format of all dates.
const TimeLayout = "2006-01-02 15:04:05"

// Time is a date column. It scans 'YYYY-MM-DD HH:MM:SS' text into a time in
// UTC. For *_local columns the result is the site-local wall clock carried in
// a UTC time value; format it directly, do not convert it.
type Time struct{ time.Time }

// Scan implements sql.Scanner.
func (t *Time) Scan(src any) error {
	var s string
	switch v := src.(type) {
	case string:
		s = v
	case []byte:
		s = string(v)
	case time.Time:
		t.Time = v.UTC()
		return nil
	case nil:
		t.Time = time.Time{}
		return nil
	default:
		return fmt.Errorf("db.Time: cannot scan %T", src)
	}
	if s == "" || s == "0000-00-00 00:00:00" {
		t.Time = time.Time{}
		return nil
	}
	parsed, err := time.Parse(TimeLayout, s)
	if err != nil {
		return err
	}
	t.Time = parsed
	return nil
}

// Value implements driver.Valuer.
func (t Time) Value() (driver.Value, error) {
	if t.IsZero() {
		return "0000-00-00 00:00:00", nil
	}
	return t.UTC().Format(TimeLayout), nil
}
