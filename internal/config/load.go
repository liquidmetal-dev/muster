package config

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/liquidmetal-dev/muster/internal/tlsconfig"
)

// Load reads the file at path with Parse, then resolves every secret and
// builds every TLS configuration once, so a missing or malformed key,
// certificate or CA bundle fails at startup rather than at first use.
// Parse failures are returned on their own; resolve failures are joined.
func Load(path string) (*Config, error) {
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return nil, &FieldError{Err: err}
	}
	defer func() { _ = f.Close() }()
	cfg, err := Parse(f)
	if err != nil {
		return nil, err
	}
	if err := resolve(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

func resolve(cfg *Config) error {
	var errs errorList
	gh := &cfg.GitHub
	if gh.TokenFile != "" || gh.TokenEnv != "" {
		s, err := readSecret("github.token_file", gh.TokenFile, "github.token_env", gh.TokenEnv)
		if err != nil {
			errs.errs = append(errs.errs, err)
		}
		gh.Token = s
	}
	if gh.WebhookSecretFile != "" || gh.WebhookSecretEnv != "" {
		s, err := readSecret("github.webhook_secret_file", gh.WebhookSecretFile, "github.webhook_secret_env", gh.WebhookSecretEnv)
		if err != nil {
			errs.errs = append(errs.errs, err)
		}
		gh.WebhookSecret = s
	}
	if gh.App != nil {
		s, err := readSecret("github.app.private_key_file", gh.App.PrivateKeyFile, "github.app.private_key_env", gh.App.PrivateKeyEnv)
		switch {
		case err != nil:
			errs.errs = append(errs.errs, err)
		case parseRSAPrivateKey(s.Value()) != nil:
			path := "github.app.private_key_file"
			if gh.App.PrivateKeyFile == "" {
				path = "github.app.private_key_env"
			}
			errs.add(path, "not a PEM-encoded RSA private key")
		}
		gh.App.PrivateKey = s
	}

	if cfg.Listen != nil && cfg.Listen.TLS != nil {
		probeServer("listen.tls", *cfg.Listen.TLS, &errs)
	}
	if cfg.API != nil {
		probeServer("api.tls", cfg.API.TLS, &errs)
	}
	probeClient("battery.tls", cfg.Battery.TLS, &errs)
	probeClient("flintlock.tls", cfg.Flintlock.TLS, &errs)
	for name, host := range cfg.Flintlock.Hosts {
		probeClient("flintlock.hosts["+strconv.Quote(name)+"].tls", host.TLS, &errs)
	}
	return errs.err()
}

func probeServer(path string, s tlsconfig.Server, errs *errorList) {
	if _, err := s.TLSConfig(); err != nil {
		errs.addErr(path, err)
	}
}

func probeClient(path string, c tlsconfig.Client, errs *errorList) {
	if _, err := c.TLSConfig(); err != nil {
		errs.addErr(path, err)
	}
}

// parseRSAPrivateKey accepts a PKCS#1 or PKCS#8 PEM block holding an RSA
// key, the two forms GitHub issues for App keys.
func parseRSAPrivateKey(pemBytes []byte) error {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return errors.New("no PEM block")
	}
	if _, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return nil
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return fmt.Errorf("parse key: %w", err)
	}
	if _, ok := key.(*rsa.PrivateKey); !ok {
		return errors.New("not an RSA key")
	}
	return nil
}
