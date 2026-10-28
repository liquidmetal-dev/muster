package config

import (
	"bytes"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// Default values applied by Parse when a field is absent or zero.
const (
	DefaultListenAddr         = ":8443"
	DefaultAPIAddr            = ":9443"
	DefaultMetricsAddr        = ":9090"
	DefaultAPIBaseURL         = "https://api.github.com"
	DefaultPollInterval       = 60 * time.Second
	DefaultNamespace          = "muster"
	DefaultClaimDeadline      = 10 * time.Minute
	DefaultStartTimeout       = 2 * time.Minute
	DefaultIdleTimeout        = 5 * time.Minute
	DefaultMaxSessionDuration = 6 * time.Hour
)

// Parse decodes a configuration, applies defaults and runs every static
// validation rule. It touches no file. The error joins one FieldError per
// failure.
func Parse(r io.Reader) (*Config, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, &FieldError{Err: fmt.Errorf("read configuration: %w", err)}
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, &FieldError{Err: errors.New("configuration is empty")}
	}
	var cfg Config
	if err := jsonv2.Unmarshal(data, &cfg, jsonv2.RejectUnknownMembers(true)); err != nil {
		return nil, decodeError(err)
	}
	applyDefaults(&cfg)
	if err := validate(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// decodeError turns a json/v2 error into a FieldError with the JSON path.
func decodeError(err error) error {
	var se *jsonv2.SemanticError
	if errors.As(err, &se) {
		path := pointerPath(se.JSONPointer)
		switch {
		case errors.Is(se.Err, jsonv2.ErrUnknownName):
			return &FieldError{Path: path, Err: errors.New("unknown field")}
		case se.Err != nil:
			return &FieldError{Path: path, Err: se.Err}
		case se.GoType != nil:
			return &FieldError{Path: path, Err: fmt.Errorf("cannot decode JSON %s into %s", se.JSONKind, se.GoType)}
		}
		return &FieldError{Path: path, Err: err}
	}
	var sy *jsontext.SyntacticError
	if errors.As(err, &sy) && sy.JSONPointer != "" {
		return &FieldError{Path: pointerPath(sy.JSONPointer), Err: sy.Err}
	}
	return &FieldError{Err: err}
}

// pointerPath converts a JSON pointer such as /pools/0/size into the
// dotted form pools[0].size.
func pointerPath(p jsontext.Pointer) string {
	var b strings.Builder
	for token := range p.Tokens() {
		if _, err := strconv.Atoi(token); err == nil {
			b.WriteString("[" + token + "]")
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('.')
		}
		b.WriteString(token)
	}
	return b.String()
}

func applyDefaults(cfg *Config) {
	if cfg.Listen != nil && cfg.Listen.Addr == "" {
		cfg.Listen.Addr = DefaultListenAddr
	}
	if cfg.API != nil && cfg.API.Addr == "" {
		cfg.API.Addr = DefaultAPIAddr
	}
	if cfg.MetricsAddr == "" {
		cfg.MetricsAddr = DefaultMetricsAddr
	}
	if cfg.GitHub.APIBaseURL == "" {
		cfg.GitHub.APIBaseURL = DefaultAPIBaseURL
	}
	if cfg.GitHub.Poll.Interval == 0 {
		cfg.GitHub.Poll.Interval = Duration(DefaultPollInterval)
	}
	if cfg.Battery.Namespace == "" {
		cfg.Battery.Namespace = DefaultNamespace
	}
	defaultDuration(&cfg.Defaults.ClaimDeadline, DefaultClaimDeadline)
	defaultDuration(&cfg.Defaults.StartTimeout, DefaultStartTimeout)
	defaultDuration(&cfg.Defaults.IdleTimeout, DefaultIdleTimeout)
	defaultDuration(&cfg.Defaults.MaxSessionDuration, DefaultMaxSessionDuration)
	for i := range cfg.Pools {
		p := &cfg.Pools[i]
		if p.MaxConcurrent == 0 && p.Size > 0 {
			p.MaxConcurrent = 2 * p.Size
		}
	}
}

func defaultDuration(d *Duration, v time.Duration) {
	if *d == 0 {
		*d = Duration(v)
	}
}
