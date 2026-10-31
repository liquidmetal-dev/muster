package github

import (
	"fmt"
	"net/http"

	"github.com/liquidmetal-dev/muster/internal/clock"
)

// jwtTransport authenticates as the App with a JWT minted for each request.
type jwtTransport struct {
	auth  AppAuth
	clock clock.Clock
	next  http.RoundTripper
}

func (t *jwtTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	jwt, err := signAppJWT(t.auth.PrivateKey, t.auth.AppID, t.clock.Now())
	if err != nil {
		return nil, err
	}
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", fmt.Sprintf("Bearer %s", jwt))
	return t.next.RoundTrip(r)
}
