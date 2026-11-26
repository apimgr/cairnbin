package ssl

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// overlaySuffixes are the app-managed overlay-network TLDs (PART 15). Let's
// Encrypt cannot issue certificates for these hosts, so HTTPS on an overlay
// address is served with a cached self-signed certificate. Clearnet hosts are
// NEVER self-signed.
var overlaySuffixes = []string{".onion", ".i2p", ".exit"}

// isOverlayHost reports whether fqdn belongs to an app-managed overlay network
// (Tor .onion, I2P .i2p, or .exit notation).
func isOverlayHost(fqdn string) bool {
	lower := strings.ToLower(strings.TrimSpace(fqdn))
	for _, s := range overlaySuffixes {
		if strings.HasSuffix(lower, s) {
			return true
		}
	}
	return false
}

// ensureSelfSignedCert returns the paths to a self-signed cert/key pair for
// fqdn in dir, generating and caching a new pair when one is absent, unreadable,
// or expired. The pair is written as cert.pem + key.pem with 0600 permissions
// (PART 15 overlay fallback). Overlay certs are user-managed with no auto-renew.
func ensureSelfSignedCert(dir, fqdn string) (certPath, keyPath string, err error) {
	certPath = filepath.Join(dir, "cert.pem")
	keyPath = filepath.Join(dir, "key.pem")

	if fileReadable(certPath) && fileReadable(keyPath) {
		if verr := validateCertFile(certPath, fqdn); verr == nil {
			return certPath, keyPath, nil
		}
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", "", fmt.Errorf("create cert dir %s: %w", dir, err)
	}

	certPEM, keyPEM, err := generateSelfSigned(fqdn)
	if err != nil {
		return "", "", err
	}
	// Write the key before the cert so a crash mid-write never leaves a cert
	// without its matching key; each write is atomic (temp + rename).
	if err := atomicWriteFile(keyPath, keyPEM, 0o600); err != nil {
		return "", "", fmt.Errorf("write key %s: %w", keyPath, err)
	}
	if err := atomicWriteFile(certPath, certPEM, 0o600); err != nil {
		return "", "", fmt.Errorf("write cert %s: %w", certPath, err)
	}
	return certPath, keyPath, nil
}

// generateSelfSigned creates a self-signed ECDSA P-256 certificate covering
// fqdn, valid for 10 years (overlay certs have no auto-renew).
func generateSelfSigned(fqdn string) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate key: %w", err)
	}

	serialLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		return nil, nil, fmt.Errorf("generate serial: %w", err)
	}

	now := time.Now()
	tmpl := x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: fqdn},
		NotBefore:             now.Add(-1 * time.Hour),
		NotAfter:              now.AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{fqdn},
	}

	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, fmt.Errorf("create certificate: %w", err)
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal key: %w", err)
	}

	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM, nil
}
