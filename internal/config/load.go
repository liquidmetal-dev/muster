package config

import (
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
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
		resolveSecret(&errs, &gh.Token, "github.token_file", gh.TokenFile, "github.token_env", gh.TokenEnv)
	}
	if gh.WebhookSecretFile != "" || gh.WebhookSecretEnv != "" {
		resolveSecret(&errs, &gh.WebhookSecret, "github.webhook_secret_file", gh.WebhookSecretFile, "github.webhook_secret_env", gh.WebhookSecretEnv)
	}
	if gh.App != nil {
		path := resolveSecret(&errs, &gh.App.PrivateKey, "github.app.private_key_file", gh.App.PrivateKeyFile, "github.app.private_key_env", gh.App.PrivateKeyEnv)
		if gh.App.PrivateKey.IsSet() && parseRSAPrivateKey(gh.App.PrivateKey.Value()) != nil {
			errs.add(path, "not a PEM-encoded RSA private key")
		}
	}

	if cfg.Listen != nil && cfg.Listen.TLS != nil {
		probeTLS(&errs, "listen.tls", cfg.Listen.TLS)
	}
	if cfg.API != nil {
		probeTLS(&errs, "api.tls", cfg.API.TLS)
	}
	probeTLS(&errs, "battery.tls", cfg.Battery.TLS)
	probeTLS(&errs, "flintlock.tls", cfg.Flintlock.TLS)
	for name, host := range cfg.Flintlock.Hosts {
		probeTLS(&errs, "flintlock.hosts["+strconv.Quote(name)+"].tls", host.TLS)
	}
	return errs.err()
}

// resolveSecret reads the secret named by file or env into dst and
// records a failure at the path of the source that was set. It returns
// that path.
func resolveSecret(errs *errorList, dst *Secret, filePath, file, envPath, env string) string {
	path := filePath
	if file == "" {
		path = envPath
	}
	s, err := readSecret(file, env)
	if err != nil {
		errs.addErr(path, err)
		return path
	}
	*dst = s
	return path
}

// tlsBuilder is what tlsconfig.Client and tlsconfig.Server share.
type tlsBuilder interface {
	TLSConfig() (*tls.Config, error)
}

// probeTLS builds the configuration once so bad files fail at startup.
func probeTLS(errs *errorList, path string, b tlsBuilder) {
	if _, err := b.TLSConfig(); err != nil {
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
