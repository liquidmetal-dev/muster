package session

import (
	"encoding/base32"
	"fmt"
	"io"

	"github.com/liquidmetal-dev/muster/internal/config"
)

var idEncoding = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

func newID(r io.Reader) (string, error) {
	var b [8]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return "", fmt.Errorf("session id: %w", err)
	}
	return idEncoding.EncodeToString(b[:])[:config.SessionIDLen], nil
}

func runnerName(namespace, pool, id string) string {
	return config.RunnerNamePrefix(namespace, pool) + id
}
