// Package tlstest generates throwaway certificates for tests: one CA, one
// server certificate and one client certificate, written as PEM files.
package tlstest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Certs holds the paths of the generated PEM files.
type Certs struct {
	CAFile         string
	ServerCertFile string
	ServerKeyFile  string
	ClientCertFile string
	ClientKeyFile  string
}

// Generate writes a CA, a server certificate valid for host and 127.0.0.1,
// and a client certificate under t.TempDir(). All are valid for one hour.
func Generate(t testing.TB, host string) Certs {
	t.Helper()
	dir := t.TempDir()
	now := time.Now()

	caKey := newKey(t)
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "tlstest CA"},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("tlstest: create CA: %v", err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatalf("tlstest: parse CA: %v", err)
	}

	serverTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: host},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{host},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
	}
	clientTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(3),
		Subject:      pkix.Name{CommonName: "tlstest client"},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}

	c := Certs{
		CAFile:         filepath.Join(dir, "ca.pem"),
		ServerCertFile: filepath.Join(dir, "server.pem"),
		ServerKeyFile:  filepath.Join(dir, "server-key.pem"),
		ClientCertFile: filepath.Join(dir, "client.pem"),
		ClientKeyFile:  filepath.Join(dir, "client-key.pem"),
	}
	writePEM(t, c.CAFile, "CERTIFICATE", caDER)
	issue(t, serverTmpl, caCert, caKey, c.ServerCertFile, c.ServerKeyFile)
	issue(t, clientTmpl, caCert, caKey, c.ClientCertFile, c.ClientKeyFile)
	return c
}

func issue(t testing.TB, tmpl, ca *x509.Certificate, caKey *ecdsa.PrivateKey, certFile, keyFile string) {
	t.Helper()
	key := newKey(t)
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatalf("tlstest: create certificate %s: %v", tmpl.Subject.CommonName, err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("tlstest: marshal key: %v", err)
	}
	writePEM(t, certFile, "CERTIFICATE", der)
	writePEM(t, keyFile, "EC PRIVATE KEY", keyDER)
}

func newKey(t testing.TB) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("tlstest: generate key: %v", err)
	}
	return key
}

func writePEM(t testing.TB, path, typ string, der []byte) {
	t.Helper()
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der}), 0o600); err != nil {
		t.Fatalf("tlstest: write %s: %v", path, err)
	}
}
