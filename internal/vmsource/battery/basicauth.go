package battery

import (
	"context"
	"encoding/base64"
)

// basicAuth adds poolmgrd's basic auth header to every call.
type basicAuth struct{ header string }

func newBasicAuth(token string) basicAuth {
	return basicAuth{header: "basic " + base64.StdEncoding.EncodeToString([]byte(token))}
}

// GetRequestMetadata implements credentials.PerRPCCredentials.
func (b basicAuth) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	return map[string]string{"authorization": b.header}, nil
}

// RequireTransportSecurity implements credentials.PerRPCCredentials. It is
// false because the operator chose plaintext explicitly with insecure.
func (basicAuth) RequireTransportSecurity() bool { return false }
