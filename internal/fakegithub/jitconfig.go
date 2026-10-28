package fakegithub

import (
	"encoding/base64"
	"encoding/json"
)

// JITConfig is the content of the encoded JIT configuration the fake issues.
// The e2e runner stub decodes it, sleeps for SleepSeconds and exits with
// ExitCode, so the stub and the fake agree on how the job ends.
type JITConfig struct {
	RunnerID     int64   `json:"runner_id"`
	RunnerName   string  `json:"runner_name"`
	SleepSeconds float64 `json:"sleep_seconds"`
	ExitCode     int     `json:"exit_code"`
}

// Encode returns the base64 of the JSON form of c.
func (c JITConfig) Encode() string {
	b, _ := json.Marshal(c)
	return base64.StdEncoding.EncodeToString(b)
}

// DecodeJITConfig parses a value produced by Encode.
func DecodeJITConfig(s string) (JITConfig, error) {
	var c JITConfig
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, err
	}
	return c, nil
}
