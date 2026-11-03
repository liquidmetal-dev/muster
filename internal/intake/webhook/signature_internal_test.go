package webhook

import (
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec // builds a legacy signature that must be rejected
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func mac(secret, body []byte) string {
	m := hmac.New(sha256.New, secret)
	m.Write(body)
	return hex.EncodeToString(m.Sum(nil))
}

func TestVerifySignature(t *testing.T) {
	secret := []byte("s3cret")
	body := []byte(`{"a":1}`)
	good := "sha256=" + mac(secret, body)
	legacy := hmac.New(sha1.New, secret)
	legacy.Write(body)

	tests := []struct {
		name   string
		header string
		body   []byte
		want   bool
	}{
		{"good", good, body, true},
		{"uppercase hex", "sha256=" + strings.ToUpper(mac(secret, body)), body, false},
		{"sha1", "sha1=" + hex.EncodeToString(legacy.Sum(nil)), body, false},
		{"no prefix", mac(secret, body), body, false},
		{"empty digest", "sha256=", body, false},
		{"empty header", "", body, false},
		{"not hex", "sha256=zz", body, false},
		{"odd length", good[:len(good)-1], body, false},
		{"wrong secret", "sha256=" + mac([]byte("other"), body), body, false},
		{"other body", good, []byte(`{"a":2}`), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := verifySignature(secret, tt.body, tt.header); got != tt.want {
				t.Fatalf("verifySignature(%q) = %v, want %v", tt.header, got, tt.want)
			}
		})
	}
}
