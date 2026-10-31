package github

import (
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/liquidmetal-dev/muster/internal/clock"
	"github.com/liquidmetal-dev/muster/internal/fakegithub"
)

type recordAuth struct{ auth []string }

func (r *recordAuth) RoundTrip(req *http.Request) (*http.Response, error) {
	r.auth = append(r.auth, req.Header.Get("Authorization"))
	return &http.Response{StatusCode: 200, Body: http.NoBody, Header: http.Header{}, Request: req}, nil
}

func TestAuthTransportOrigin(t *testing.T) {
	base, _ := url.Parse("https://ghe.example.com/api/v3/")
	targets := []struct {
		url  string
		auth bool
	}{
		{"https://ghe.example.com/api/v3/user", true},
		{"https://GHE.example.com:443/api/v3/user", true},
		{"https://evil.example/api/v3/user", false},
		{"https://ghe.example.com:8443/api/v3/user", false},
		{"http://ghe.example.com/api/v3/user", false},
	}
	key := fakegithub.AppKey(t)
	transports := map[string]func(next http.RoundTripper) http.RoundTripper{
		"token": func(next http.RoundTripper) http.RoundTripper {
			return &authTransport{src: patSource{token: "ghp_x"}, base: base, next: next}
		},
		"jwt": func(next http.RoundTripper) http.RoundTripper {
			return &jwtTransport{auth: AppAuth{AppID: 1, PrivateKey: key}, clock: clock.NewManual(time.Now()), base: base, next: next}
		},
	}
	for name, build := range transports {
		t.Run(name, func(t *testing.T) {
			for _, tt := range targets {
				rec := &recordAuth{}
				req, _ := http.NewRequest(http.MethodGet, tt.url, nil)
				resp, err := build(rec).RoundTrip(req)
				if err != nil {
					t.Fatalf("%s: %v", tt.url, err)
				}
				_ = resp.Body.Close()
				if got := rec.auth[0] != ""; got != tt.auth {
					t.Errorf("%s: authorization sent = %v, want %v", tt.url, got, tt.auth)
				}
			}
		})
	}
}
