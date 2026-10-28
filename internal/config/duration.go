package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Duration is a time.Duration written as a string such as "10m".
type Duration time.Duration

// UnmarshalJSON parses a duration string. A JSON number is an error.
func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return errors.New(`must be a duration string such as "10m"`)
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q", s)
	}
	*d = Duration(v)
	return nil
}

// MarshalJSON writes the duration as a string.
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

// Duration returns the time.Duration.
func (d Duration) Duration() time.Duration {
	return time.Duration(d)
}
