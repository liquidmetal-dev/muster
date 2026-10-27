// Package tlsconfig turns the shared client TLS settings (CA file, client
// certificate and key, explicit insecure) into gRPC transport credentials.
// It is used for battery, flintlock and any other outbound gRPC client so
// all of them follow the same rules (requirement SEC-2): plaintext only
// with Insecure set, and TLS 1.2 or newer otherwise.
package tlsconfig

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"

	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

// Client is the TLS configuration of one outbound connection.
type Client struct {
	// CAFile is a PEM bundle that signs the server certificate. Empty uses
	// the system pool.
	CAFile string
	// CertFile and KeyFile are the client certificate for mutual TLS. Both
	// or neither must be set.
	CertFile string
	KeyFile  string
	// Insecure selects plaintext. It must be the only field set.
	Insecure bool
}

// Validate reports settings that contradict each other.
func (c Client) Validate() error {
	if c.Insecure && (c.CAFile != "" || c.CertFile != "" || c.KeyFile != "") {
		return errors.New("tls: insecure is set together with ca_file, cert_file or key_file")
	}
	if c.CertFile != "" && c.KeyFile == "" {
		return errors.New("tls: cert_file is set without key_file")
	}
	if c.KeyFile != "" && c.CertFile == "" {
		return errors.New("tls: key_file is set without cert_file")
	}
	return nil
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
		pemBytes, err := os.ReadFile(c.CAFile)
		if err != nil {
			return nil, fmt.Errorf("tls: read ca_file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pemBytes) {
			return nil, fmt.Errorf("tls: ca_file %s contains no certificates", c.CAFile)
		}
		cfg.RootCAs = pool
	}
	if c.CertFile != "" {
		pair, err := tls.LoadX509KeyPair(c.CertFile, c.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("tls: load cert_file and key_file: %w", err)
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
