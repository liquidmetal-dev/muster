package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

const signaturePrefix = "sha256="

// verifySignature reports whether header is the X-Hub-Signature-256 value
// for body under secret. Only the lower-case hex form GitHub sends is
// accepted; there is no SHA-1 fallback.
func verifySignature(secret, body []byte, header string) bool {
	digest, ok := strings.CutPrefix(header, signaturePrefix)
	if !ok || digest != strings.ToLower(digest) {
		return false
	}
	got, err := hex.DecodeString(digest)
	if err != nil {
		return false
	}
	m := hmac.New(sha256.New, secret)
	m.Write(body)
	return hmac.Equal(got, m.Sum(nil))
}
