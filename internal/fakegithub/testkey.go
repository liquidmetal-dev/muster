package fakegithub

import (
	"crypto/rand"
	"crypto/rsa"
	"sync"
	"testing"
)

var (
	appKeyOnce sync.Once
	appKey     *rsa.PrivateKey
	appKeyErr  error
)

// AppKey returns a 2048-bit RSA key for tests. One key is generated per
// process and shared, because key generation is slow under the race detector.
func AppKey(tb testing.TB) *rsa.PrivateKey {
	tb.Helper()
	appKeyOnce.Do(func() { appKey, appKeyErr = rsa.GenerateKey(rand.Reader, 2048) })
	if appKeyErr != nil {
		tb.Fatalf("generate app key: %v", appKeyErr)
	}
	return appKey
}
