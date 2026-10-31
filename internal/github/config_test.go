package github_test

import (
	"crypto/x509"
	"encoding/pem"
	"errors"
	"testing"

	"github.com/liquidmetal-dev/muster/internal/config"
	"github.com/liquidmetal-dev/muster/internal/fakegithub"
	"github.com/liquidmetal-dev/muster/internal/github"
)

func TestFromConfigApp(t *testing.T) {
	key := fakegithub.AppKey(t)
	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	forms := map[string][]byte{
		"pkcs1": pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}),
		"pkcs8": pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8}),
	}
	for name, p := range forms {
		t.Run(name, func(t *testing.T) {
			cfg := &config.Config{GitHub: config.GitHub{
				APIBaseURL: "https://ghe.example.com/api/v3",
				App:        &config.App{AppID: 1, InstallationID: 2, PrivateKey: config.NewSecret(p)},
			}}
			got, err := github.FromConfig(cfg)
			if err != nil {
				t.Fatalf("FromConfig: %v", err)
			}
			if got.App == nil || got.App.AppID != 1 || got.App.InstallationID != 2 || !got.App.PrivateKey.Equal(key) {
				t.Fatalf("App = %+v", got.App)
			}
			if got.Token != "" || got.BaseURL != "https://ghe.example.com/api/v3" {
				t.Fatalf("Token=%q BaseURL=%q", got.Token, got.BaseURL)
			}
		})
	}
}

func TestFromConfigToken(t *testing.T) {
	cfg := &config.Config{GitHub: config.GitHub{Token: config.NewSecret([]byte("ghp_x"))}}
	got, err := github.FromConfig(cfg)
	if err != nil {
		t.Fatalf("FromConfig: %v", err)
	}
	if got.Token != "ghp_x" || got.App != nil {
		t.Fatalf("Token=%q App=%v", got.Token, got.App)
	}
}

func TestFromConfigNeitherOrBoth(t *testing.T) {
	both := &config.Config{GitHub: config.GitHub{
		Token: config.NewSecret([]byte("ghp_x")),
		App:   &config.App{AppID: 1, InstallationID: 2, PrivateKey: config.NewSecret([]byte("x"))},
	}}
	for name, cfg := range map[string]*config.Config{"neither": {}, "both": both} {
		if _, err := github.FromConfig(cfg); !errors.Is(err, github.ErrInvalidRequest) {
			t.Errorf("%s: err = %v, want ErrInvalidRequest", name, err)
		}
	}
}

func TestFromConfigBadKey(t *testing.T) {
	cfg := &config.Config{GitHub: config.GitHub{
		App: &config.App{AppID: 1, InstallationID: 2, PrivateKey: config.NewSecret([]byte("not pem"))},
	}}
	if _, err := github.FromConfig(cfg); !errors.Is(err, github.ErrInvalidRequest) {
		t.Fatalf("err = %v, want ErrInvalidRequest", err)
	}
}
