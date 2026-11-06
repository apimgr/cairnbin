package ssl

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// ─── findCertPair ─────────────────────────────────────────────────────────────

func TestFindCertPair_NotFound(t *testing.T) {
	dir := t.TempDir()
	cert, key := findCertPair(dir)
	if cert != "" || key != "" {
		t.Errorf("findCertPair empty dir: got (%q, %q); want (\"\", \"\")", cert, key)
	}
}

func TestFindCertPair_FullchainAndPrivkey(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "fullchain.pem")
	keyPath := filepath.Join(dir, "privkey.pem")
	if err := os.WriteFile(certPath, []byte("cert"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, []byte("key"), 0o600); err != nil {
		t.Fatal(err)
	}

	gotCert, gotKey := findCertPair(dir)
	if gotCert != certPath {
		t.Errorf("cert = %q; want %q", gotCert, certPath)
	}
	if gotKey != keyPath {
		t.Errorf("key = %q; want %q", gotKey, keyPath)
	}
}

func TestFindCertPair_CertAndKey(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certPath, []byte("cert"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, []byte("key"), 0o600); err != nil {
		t.Fatal(err)
	}

	gotCert, gotKey := findCertPair(dir)
	if gotCert != certPath {
		t.Errorf("cert = %q; want %q", gotCert, certPath)
	}
	if gotKey != keyPath {
		t.Errorf("key = %q; want %q", gotKey, keyPath)
	}
}

// ─── validateCertFile ─────────────────────────────────────────────────────────

func TestValidateCertFile_Nonexistent(t *testing.T) {
	err := validateCertFile("/nonexistent/cert.pem", "example.com")
	if err == nil {
		t.Error("expected error for nonexistent cert file")
	}
}

func TestValidateCertFile_NoPEMBlock(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "cert.pem")
	if err := os.WriteFile(p, []byte("not pem"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := validateCertFile(p, "")
	if err == nil {
		t.Error("expected error for file with no PEM block")
	}
}

func TestValidateCertFile_ValidLocalhost(t *testing.T) {
	certPEM, _ := generateSelfSignedCert(t, "localhost", time.Now().Add(24*time.Hour))
	dir := t.TempDir()
	p := filepath.Join(dir, "cert.pem")
	if err := os.WriteFile(p, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	// localhost FQDN skips hostname validation — should succeed.
	if err := validateCertFile(p, "localhost"); err != nil {
		t.Errorf("validateCertFile (localhost): unexpected error: %v", err)
	}
}

func TestValidateCertFile_Expired(t *testing.T) {
	certPEM, _ := generateSelfSignedCert(t, "example.com", time.Now().Add(-time.Hour))
	dir := t.TempDir()
	p := filepath.Join(dir, "cert.pem")
	if err := os.WriteFile(p, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	err := validateCertFile(p, "example.com")
	if err == nil {
		t.Error("expected error for expired certificate")
	}
}

// ─── fileReadable ─────────────────────────────────────────────────────────────

func TestFileReadable_Exists(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !fileReadable(p) {
		t.Error("fileReadable: expected true for existing file")
	}
}

func TestFileReadable_Missing(t *testing.T) {
	if fileReadable("/nonexistent/path.pem") {
		t.Error("fileReadable: expected false for missing file")
	}
}

// ─── overlay self-signed ──────────────────────────────────────────────────────

func TestIsOverlayHost(t *testing.T) {
	cases := map[string]bool{
		"abc123.onion":  true,
		"Foo.I2P":       true,
		"node.exit":     true,
		"example.com":   false,
		"localhost":     false,
		"onion.example": false,
		"":              false,
	}
	for host, want := range cases {
		if got := isOverlayHost(host); got != want {
			t.Errorf("isOverlayHost(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestGenerateSelfSigned_CoversFQDN(t *testing.T) {
	fqdn := "abc123def456.onion"
	certPEM, keyPEM, err := generateSelfSigned(fqdn)
	if err != nil {
		t.Fatalf("generateSelfSigned: %v", err)
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "cert.pem")
	if err := os.WriteFile(p, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateCertFile(p, fqdn); err != nil {
		t.Errorf("generated cert failed validation for %q: %v", fqdn, err)
	}
	if len(keyPEM) == 0 {
		t.Error("empty key PEM")
	}
}

func TestEnsureSelfSignedCert_GeneratesAndReuses(t *testing.T) {
	fqdn := "reuse123.onion"
	dir := filepath.Join(t.TempDir(), "local", fqdn)

	certPath, keyPath, err := ensureSelfSignedCert(dir, fqdn)
	if err != nil {
		t.Fatalf("ensureSelfSignedCert (first): %v", err)
	}
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("key perm = %o, want 600", perm)
	}
	first, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatal(err)
	}

	// Second call must reuse the cached, still-valid cert (bytes unchanged).
	if _, _, err := ensureSelfSignedCert(dir, fqdn); err != nil {
		t.Fatalf("ensureSelfSignedCert (second): %v", err)
	}
	second, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Error("cached cert was regenerated on second call")
	}
}

// ─── dnsProviderEnvMapping ──────────────────────────────────────────────────

func TestDNSProviderEnvMapping_SixExplicitProviders(t *testing.T) {
	// PART 15 "Common Providers" table: verify the mapping table covers all 6
	// explicit providers with their real lego environment variable names
	// (verified against go-acme/lego/v4 source, not guessed).
	want := map[string]map[string]string{
		"cloudflare": {
			"api_token": "CF_DNS_API_TOKEN",
			"api_key":   "CF_API_KEY",
			"email":     "CF_API_EMAIL",
		},
		"route53": {
			"access_key_id":     "AWS_ACCESS_KEY_ID",
			"secret_access_key": "AWS_SECRET_ACCESS_KEY",
			"region":            "AWS_REGION",
		},
		"digitalocean": {
			"auth_token": "DO_AUTH_TOKEN",
		},
		"godaddy": {
			"api_key":    "GODADDY_API_KEY",
			"api_secret": "GODADDY_API_SECRET",
		},
		"namecheap": {
			"api_user": "NAMECHEAP_API_USER",
			"api_key":  "NAMECHEAP_API_KEY",
		},
		"rfc2136": {
			"nameserver":     "DNSUPDATE_NAMESERVER",
			"tsig_key":       "DNSUPDATE_TSIG_KEY",
			"tsig_secret":    "DNSUPDATE_TSIG_SECRET",
			"tsig_algorithm": "DNSUPDATE_TSIG_ALGORITHM",
		},
	}
	for provider, fields := range want {
		got, ok := dnsProviderEnvMapping[provider]
		if !ok {
			t.Errorf("dnsProviderEnvMapping missing provider %q", provider)
			continue
		}
		for field, envVar := range fields {
			if got[field] != envVar {
				t.Errorf("dnsProviderEnvMapping[%q][%q] = %q; want %q", provider, field, got[field], envVar)
			}
		}
		if len(got) != len(fields) {
			t.Errorf("dnsProviderEnvMapping[%q] has %d fields; want %d", provider, len(got), len(fields))
		}
	}
	// namecheap intentionally has no client_ip mapping — lego auto-detects it.
	if _, present := dnsProviderEnvMapping["namecheap"]["client_ip"]; present {
		t.Error("dnsProviderEnvMapping[\"namecheap\"] should not map client_ip")
	}
}

func TestDNSProviderEnvMapping_ExactlySixProviders(t *testing.T) {
	if len(dnsProviderEnvMapping) != 6 {
		t.Errorf("dnsProviderEnvMapping has %d providers; want 6", len(dnsProviderEnvMapping))
	}
}

// ─── buildDNSProvider ─────────────────────────────────────────────────────────

func TestBuildDNSProvider_EmptyProviderName(t *testing.T) {
	_, err := buildDNSProvider("", map[string]string{"auth_token": "x"})
	if err == nil {
		t.Error("expected error for empty provider name")
	}
}

func TestBuildDNSProvider_NoCredentials(t *testing.T) {
	_, err := buildDNSProvider("digitalocean", nil)
	if err == nil {
		t.Error("expected error for missing credentials")
	}
	_, err = buildDNSProvider("digitalocean", map[string]string{})
	if err == nil {
		t.Error("expected error for empty credentials map")
	}
}

func TestBuildDNSProvider_MappedProvider_Digitalocean(t *testing.T) {
	// digitalocean's NewDNSProvider makes no network calls during
	// construction, so it's safe to exercise end-to-end here.
	t.Setenv("DO_AUTH_TOKEN", "")
	provider, err := buildDNSProvider("digitalocean", map[string]string{"auth_token": "test-token-value"})
	if err != nil {
		t.Fatalf("buildDNSProvider(digitalocean): unexpected error: %v", err)
	}
	if provider == nil {
		t.Error("expected non-nil challenge.Provider")
	}
	if got := os.Getenv("DO_AUTH_TOKEN"); got != "test-token-value" {
		t.Errorf("DO_AUTH_TOKEN = %q; want %q (credential translation)", got, "test-token-value")
	}
}

func TestBuildDNSProvider_UnknownProviderName(t *testing.T) {
	// Not in dnsProviderEnvMapping and not a real lego provider name — the
	// generic passthrough branch sets the env var verbatim, then lego's
	// factory itself rejects the unrecognized provider name.
	_, err := buildDNSProvider("totally-not-a-real-provider", map[string]string{"SOME_ENV_VAR": "x"})
	if err == nil {
		t.Error("expected error for unrecognized provider name")
	}
}

func TestBuildDNSProvider_UnmappedProvider_VerbatimEnvPassthrough(t *testing.T) {
	// Unmapped-but-real lego provider ("exec" — no network calls on
	// construction, reads EXEC_PATH) exercises the verbatim env-var-name
	// passthrough branch instead of the 6-provider mapping table.
	t.Setenv("EXEC_PATH", "")
	provider, err := buildDNSProvider("exec", map[string]string{"EXEC_PATH": "/bin/true"})
	if err != nil {
		t.Fatalf("buildDNSProvider(exec): unexpected error: %v", err)
	}
	if provider == nil {
		t.Error("expected non-nil challenge.Provider")
	}
	if got := os.Getenv("EXEC_PATH"); got != "/bin/true" {
		t.Errorf("EXEC_PATH = %q; want verbatim passthrough %q", got, "/bin/true")
	}
}

// generateSelfSignedCert generates a minimal self-signed PEM certificate valid
// until notAfter (or expired if notAfter is in the past).
func generateSelfSignedCert(t *testing.T, cn string, notAfter time.Time) (certPEM []byte, keyPEM []byte) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	notBefore := notAfter.Add(-24 * time.Hour)
	if notAfter.Before(time.Now()) {
		notBefore = notAfter.Add(-24 * time.Hour)
	}

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		DNSNames:     []string{cn},
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatal(err)
	}

	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})

	privDer, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: privDer})
	return certPEM, keyPEM
}

func TestCertNeedsRenewal(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name     string
		notAfter time.Time
		want     bool
	}{
		{"expires in 3 days", time.Now().Add(3 * 24 * time.Hour), true},
		{"expires in 30 days", time.Now().Add(30 * 24 * time.Hour), false},
	}
	for _, tc := range cases {
		certPEM, _ := generateSelfSignedCert(t, "localhost", tc.notAfter)
		p := filepath.Join(dir, "cert.pem")
		if err := os.WriteFile(p, certPEM, 0o600); err != nil {
			t.Fatal(err)
		}
		if got := certNeedsRenewal(p); got != tc.want {
			t.Errorf("%s: certNeedsRenewal = %v, want %v", tc.name, got, tc.want)
		}
	}
	if !certNeedsRenewal(filepath.Join(dir, "missing.pem")) {
		t.Error("missing file must report renewal needed")
	}
}
