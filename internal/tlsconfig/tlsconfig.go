// Package tlsconfig turns TLS settings into crypto/tls and gRPC
// configurations so every connection follows the same rules (requirement
// SEC-2): plaintext only with Insecure set, and TLS 1.2 or newer otherwise.
//
// Client describes one outbound connection (battery, flintlock). Server
// describes one listener (the webhook listener, the muster API). Both load
// the private key from a file or from an environment variable (requirement
// SEC-3); certificates and CA bundles are always files.
package tlsconfig

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

// Client is the TLS configuration of one outbound connection.
type Client struct {
	// CAFile is a PEM bundle that signs the server certificate. Empty uses
	// the system pool.
	CAFile string `json:"ca_file,omitempty"`
	// CertFile is the client certificate for mutual TLS. It needs exactly
	// one of KeyFile or KeyEnv.
	CertFile string `json:"cert_file,omitempty"`
	// KeyFile is the PEM file that holds the private key of CertFile.
	KeyFile string `json:"key_file,omitempty"`
	// KeyEnv names an environment variable that holds the PEM private key
	// of CertFile.
	KeyEnv string `json:"key_env,omitempty"`
	// Insecure selects plaintext. It must be the only field set.
	Insecure bool `json:"insecure,omitempty"`
}

// Validate reports settings that contradict each other.
func (c Client) Validate() error {
	if c.Insecure && (c.CAFile != "" || c.CertFile != "" || c.KeyFile != "" || c.KeyEnv != "") {
		return errors.New("tls: insecure is set together with ca_file, cert_file, key_file or key_env")
	}
	return validateKeyPair(c.CertFile, c.KeyFile, c.KeyEnv, false)
}

// TLSConfig builds the crypto/tls configuration. It returns nil, nil when
// Insecure is set.
func (c Client) TLSConfig() (*tls.Config, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if c.Insecure {
		return nil, nil
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if c.CAFile != "" {
		pool, err := loadCertPool(c.CAFile, "ca_file")
		if err != nil {
			return nil, err
		}
		cfg.RootCAs = pool
	}
	if c.CertFile != "" {
		pair, err := loadKeyPair(c.CertFile, c.KeyFile, c.KeyEnv)
		if err != nil {
			return nil, err
		}
		cfg.Certificates = []tls.Certificate{pair}
	}
	return cfg, nil
}

// Credentials returns the gRPC transport credentials for the settings.
func (c Client) Credentials() (credentials.TransportCredentials, error) {
	cfg, err := c.TLSConfig()
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		return insecure.NewCredentials(), nil
	}
	return credentials.NewTLS(cfg), nil
}

// Server is the TLS configuration of one listener.
type Server struct {
	// CertFile is the server certificate. Required unless Insecure is set.
	CertFile string `json:"cert_file,omitempty"`
	// KeyFile is the PEM file that holds the private key of CertFile.
	KeyFile string `json:"key_file,omitempty"`
	// KeyEnv names an environment variable that holds the PEM private key
	// of CertFile.
	KeyEnv string `json:"key_env,omitempty"`
	// ClientCAFile is a PEM bundle. When set, clients must present a
	// certificate that it signs (mutual TLS).
	ClientCAFile string `json:"client_ca_file,omitempty"`
	// Insecure selects plaintext. It must be the only field set.
	Insecure bool `json:"insecure,omitempty"`
}

// Validate reports settings that contradict each other or are missing.
func (s Server) Validate() error {
	if s.Insecure {
		if s.CertFile != "" || s.KeyFile != "" || s.KeyEnv != "" || s.ClientCAFile != "" {
			return errors.New("tls: insecure is set together with cert_file, key_file, key_env or client_ca_file")
		}
		return nil
	}
	return validateKeyPair(s.CertFile, s.KeyFile, s.KeyEnv, true)
}

// TLSConfig builds the crypto/tls configuration. It returns nil, nil when
// Insecure is set.
func (s Server) TLSConfig() (*tls.Config, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	if s.Insecure {
		return nil, nil
	}
	pair, err := loadKeyPair(s.CertFile, s.KeyFile, s.KeyEnv)
	if err != nil {
		return nil, err
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{pair}}
	if s.ClientCAFile != "" {
		pool, err := loadCertPool(s.ClientCAFile, "client_ca_file")
		if err != nil {
			return nil, err
		}
		cfg.ClientCAs = pool
		cfg.ClientAuth = tls.RequireAndVerifyClientCert
	}
	return cfg, nil
}

// validateKeyPair checks the certificate and key sources. certRequired
// makes an empty certFile an error (servers); otherwise the pair is
// optional but must be complete when any part is set (clients).
func validateKeyPair(certFile, keyFile, keyEnv string, certRequired bool) error {
	if keyFile != "" && keyEnv != "" {
		return errors.New("tls: key_file and key_env are both set; set exactly one")
	}
	hasKey := keyFile != "" || keyEnv != ""
	switch {
	case certFile == "" && certRequired:
		return errors.New("tls: cert_file is required unless insecure is set")
	case certFile == "" && hasKey:
		return errors.New("tls: key_file or key_env is set without cert_file")
	case certFile != "" && !hasKey:
		return errors.New("tls: cert_file is set without key_file or key_env")
	}
	return nil
}

// loadKeyPair reads the certificate file and the key from its file or
// environment variable.
func loadKeyPair(certFile, keyFile, keyEnv string) (tls.Certificate, error) {
	certPEM, err := os.ReadFile(filepath.Clean(certFile))
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("tls: read cert_file: %w", err)
	}
	var keyPEM []byte
	if keyFile != "" {
		keyPEM, err = os.ReadFile(filepath.Clean(keyFile))
		if err != nil {
			return tls.Certificate{}, fmt.Errorf("tls: read key_file: %w", err)
		}
	} else {
		value, ok := os.LookupEnv(keyEnv)
		if !ok || value == "" {
			return tls.Certificate{}, fmt.Errorf("tls: key_env %s is not set", keyEnv)
		}
		keyPEM = []byte(value)
	}
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("tls: load certificate and key: %w", err)
	}
	return pair, nil
}

// loadCertPool reads a PEM bundle into a pool. field names the setting in
// errors.
func loadCertPool(path, field string) (*x509.CertPool, error) {
	pemBytes, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("tls: read %s: %w", field, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemBytes) {
		return nil, fmt.Errorf("tls: %s %s contains no certificates", field, path)
	}
	return pool, nil
}
