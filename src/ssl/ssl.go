package ssl

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	crand "crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"

	"github.com/go-acme/lego/v4/certificate"
	"github.com/go-acme/lego/v4/challenge"
	"github.com/go-acme/lego/v4/lego"
	legodns "github.com/go-acme/lego/v4/providers/dns"
	"github.com/go-acme/lego/v4/registration"

	"github.com/apimgr/cairnbin/src/config"
)

// LetsEncryptStagingURL is the ACME directory URL for the Let's Encrypt staging CA.
const LetsEncryptStagingURL = "https://acme-staging-v02.api.letsencrypt.org/directory"

// Config holds SSL/TLS configuration.
type Config struct {
	Enabled bool
	// CertDir is the {config_dir}/ssl/ base directory.
	CertDir string
	// FQDN is the primary domain.
	FQDN        string
	LetsEncrypt LetsEncryptConfig
}

// LetsEncryptConfig holds Let's Encrypt ACME settings.
type LetsEncryptConfig struct {
	Enabled bool
	Email   string
	// Challenge is the ACME challenge type: http-01, tls-alpn-01, or dns-01.
	Challenge       string
	DNSProviderType string
	// DNSCredentials holds decrypted provider credentials.
	DNSCredentials map[string]string
	// Staging selects the LE staging CA.
	Staging bool
}

// Manager handles SSL/TLS certificates.
type Manager struct {
	config      Config
	certManager *autocert.Manager
	mu          sync.RWMutex
}

// NewManager creates a new SSL manager.
func NewManager(cfg Config) *Manager {
	return &Manager{config: cfg}
}

// GetTLSConfig returns a *tls.Config for the given domains.
// Certificate lookup follows the PART 15 priority order:
//  1. /etc/letsencrypt/live/domain/ (system certbot, literal "domain" dir)
//  2. /etc/letsencrypt/live/{fqdn}/ (system certbot, FQDN-named dir)
//  3. {cert_dir}/letsencrypt/{fqdn}/ (app-managed LE certs)
//  4. {cert_dir}/local/{fqdn}/ (user-provided / self-signed)
//
// If no cert is found and Let's Encrypt is enabled, a new cert is requested
// via autocert and saved to {cert_dir}/letsencrypt/{fqdn}/.
func (m *Manager) GetTLSConfig(domains []string) (*tls.Config, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.config.Enabled {
		return nil, nil
	}

	fqdn := m.config.FQDN
	if fqdn == "" && len(domains) > 0 {
		fqdn = domains[0]
	}

	// Priority 1: /etc/letsencrypt/live/domain/ (literal "domain" directory).
	if cert, key := findCertPair("/etc/letsencrypt/live/domain"); cert != "" {
		if err := validateCertFile(cert, fqdn); err == nil {
			log.Printf("ssl: using system cert /etc/letsencrypt/live/domain/")
			return tlsConfigFromFiles(cert, key)
		}
	}

	// Priority 2: /etc/letsencrypt/live/{fqdn}/.
	if fqdn != "" {
		dir := filepath.Join("/etc/letsencrypt/live", fqdn)
		if cert, key := findCertPair(dir); cert != "" {
			if err := validateCertFile(cert, fqdn); err == nil {
				log.Printf("ssl: using system cert %s/", dir)
				return tlsConfigFromFiles(cert, key)
			}
		}
	}

	// Priority 3: {cert_dir}/letsencrypt/{fqdn}/ (app-managed).
	if m.config.CertDir != "" && fqdn != "" {
		dir := filepath.Join(m.config.CertDir, "letsencrypt", fqdn)
		if cert, key := findCertPair(dir); cert != "" {
			if err := validateCertFile(cert, fqdn); err == nil {
				log.Printf("ssl: using app-managed cert %s/", dir)
				return tlsConfigFromFiles(cert, key)
			}
			log.Printf("ssl: app-managed cert at %s/ failed validation; will request new cert", dir)
		}
	}

	// Priority 4: {cert_dir}/local/{fqdn}/ (user-provided).
	if m.config.CertDir != "" && fqdn != "" {
		dir := filepath.Join(m.config.CertDir, "local", fqdn)
		if cert, key := findCertPair(dir); cert != "" {
			if err := validateCertFile(cert, fqdn); err == nil {
				log.Printf("ssl: using local cert %s/", dir)
				return tlsConfigFromFiles(cert, key)
			}
		}
	}

	// No existing cert found — request via Let's Encrypt if enabled. Every
	// domain MUST pass production-grade SSL host validation (PART 12,
	// "SSL/Let's Encrypt FQDN Requirements"): dev-only TLDs, project TLDs,
	// bare IPs, and .onion hosts can never receive a publicly trusted cert.
	// Invalid domains are dropped with a warning rather than failing the
	// whole request; if none remain, LE is skipped and SSL falls through to
	// the overlay/self-signed or "no certificate" paths below.
	if m.config.LetsEncrypt.Enabled {
		validDomains := make([]string, 0, len(domains))
		for _, d := range domains {
			if config.IsValidSSLHost(d) {
				validDomains = append(validDomains, d)
			} else {
				log.Printf("ssl: domain %q is not eligible for Let's Encrypt (dev-only TLD, IP, or overlay host); skipping", d)
			}
		}
		if len(validDomains) > 0 {
			// dns-01 uses lego (no port 80/443 listener requirement, supports
			// wildcards). http-01/tls-alpn-01 keep using autocert, unchanged.
			if m.config.LetsEncrypt.Challenge == "dns-01" && m.config.LetsEncrypt.DNSProviderType != "" {
				return m.getLetsEncryptTLSConfigDNS01(validDomains)
			}
			return m.getLetsEncryptTLSConfig(validDomains)
		}
		log.Printf("ssl: no domains passed SSL host validation for %q; skipping Let's Encrypt request", fqdn)
	}

	// Overlay networks (.onion/.i2p/.exit) cannot use Let's Encrypt, so HTTPS on
	// an overlay host falls back to a cached self-signed cert in
	// {cert_dir}/local/{fqdn}/ (PART 15). Clearnet hosts NEVER self-sign.
	if isOverlayHost(fqdn) && m.config.CertDir != "" {
		dir := filepath.Join(m.config.CertDir, "local", fqdn)
		certPath, keyPath, err := ensureSelfSignedCert(dir, fqdn)
		if err != nil {
			return nil, fmt.Errorf("ssl: overlay self-signed cert generation failed for %q: %w", fqdn, err)
		}
		log.Printf("ssl: using self-signed cert for overlay host %s at %s/", fqdn, dir)
		return tlsConfigFromFiles(certPath, keyPath)
	}

	return nil, fmt.Errorf("ssl: no certificates available for %q and Let's Encrypt not enabled", fqdn)
}

// getLetsEncryptTLSConfig configures autocert for Let's Encrypt.
// Certificates are cached in {cert_dir}/letsencrypt/{fqdn}/.
func (m *Manager) getLetsEncryptTLSConfig(domains []string) (*tls.Config, error) {
	cacheDir := filepath.Join(m.config.CertDir, "letsencrypt", m.config.FQDN)
	if err := os.MkdirAll(cacheDir, 0700); err != nil {
		return nil, fmt.Errorf("ssl: failed to create cert cache dir %s: %w", cacheDir, err)
	}

	acmeMgr := &autocert.Manager{
		Prompt:     autocert.AcceptTOS,
		HostPolicy: autocert.HostWhitelist(domains...),
		Cache:      autocert.DirCache(cacheDir),
		Email:      m.config.LetsEncrypt.Email,
	}

	// Use staging CA when requested (e.g., for testing).
	if m.config.LetsEncrypt.Staging {
		acmeMgr.Client = &acme.Client{
			DirectoryURL: LetsEncryptStagingURL,
		}
	}

	m.certManager = acmeMgr
	return acmeMgr.TLSConfig(), nil
}

// dnsProviderEnvMapping maps our generic DNSCredentials field names to the
// actual environment variable names lego's DNS-01 provider factories read
// (verified against github.com/go-acme/lego/v4's provider source, not
// guessed — several differ from the "obvious" name, e.g. rfc2136 uses the
// DNSUPDATE_ prefix, and cloudflare's short aliases are CF_-prefixed).
// Covers PART 15's "Common Providers" table. Any other lego-supported
// provider (https://go-acme.github.io/lego/dns/) is still constructible —
// see buildDNSProvider's fallback branch.
var dnsProviderEnvMapping = map[string]map[string]string{
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
		// client_ip has no lego env var — lego auto-detects the caller's
		// public IP when NAMECHEAP_API_USER/KEY are set and no ClientIP is
		// supplied, so this field is intentionally not mapped.
	},
	"rfc2136": {
		"nameserver":     "DNSUPDATE_NAMESERVER",
		"tsig_key":       "DNSUPDATE_TSIG_KEY",
		"tsig_secret":    "DNSUPDATE_TSIG_SECRET",
		"tsig_algorithm": "DNSUPDATE_TSIG_ALGORITHM",
	},
}

// buildDNSProvider constructs a lego DNS-01 challenge.Provider for
// providerName using credentials. For the 6 providers in
// dnsProviderEnvMapping, our generic credential field names (e.g.
// "api_token") are translated to that provider's real lego environment
// variable name before construction, since lego's provider factories read
// credentials exclusively from the process environment.
//
// For any other lego-supported provider, credentials are set verbatim:
// operators must key DNSCredentials with that provider's exact lego
// environment variable name (see https://go-acme.github.io/lego/dns/), since
// a reliable generic field-name-to-env-var mapping cannot be derived without
// enumerating every one of lego's ~100 providers.
func buildDNSProvider(providerName string, credentials map[string]string) (challenge.Provider, error) {
	providerName = strings.ToLower(strings.TrimSpace(providerName))
	if providerName == "" {
		return nil, fmt.Errorf("ssl: dns-01 requires server.tls.dns_provider to be set")
	}
	if len(credentials) == 0 {
		return nil, fmt.Errorf("ssl: dns-01 provider %q has no credentials configured", providerName)
	}

	if mapping, ok := dnsProviderEnvMapping[providerName]; ok {
		for field, envVar := range mapping {
			v, present := credentials[field]
			if !present || v == "" {
				continue
			}
			if err := os.Setenv(envVar, v); err != nil {
				return nil, fmt.Errorf("ssl: dns-01 provider %q: set %s: %w", providerName, envVar, err)
			}
		}
	} else {
		for envVar, v := range credentials {
			if v == "" {
				continue
			}
			if err := os.Setenv(envVar, v); err != nil {
				return nil, fmt.Errorf("ssl: dns-01 provider %q: set %s: %w", providerName, envVar, err)
			}
		}
	}

	provider, err := legodns.NewDNSChallengeProviderByName(providerName)
	if err != nil {
		return nil, fmt.Errorf("ssl: dns-01 provider %q: %w", providerName, err)
	}
	return provider, nil
}

// ValidateDNSProvider attempts to construct the DNS-01 challenge provider
// for providerName using credentials, without making any ACME calls. Used
// at server startup and before certificate requests (PART 15: "Server
// validates credentials on startup and before certificate requests").
// Errors are non-fatal for callers — startup should warn-and-continue,
// matching config.Validate()'s warn-and-default pattern, never crash.
func ValidateDNSProvider(providerName string, credentials map[string]string) error {
	_, err := buildDNSProvider(providerName, credentials)
	return err
}

// legoAccountUser implements registration.User for the lego ACME client.
type legoAccountUser struct {
	email string
	reg   *registration.Resource
	key   crypto.PrivateKey
}

func (u *legoAccountUser) GetEmail() string                        { return u.email }
func (u *legoAccountUser) GetRegistration() *registration.Resource { return u.reg }
func (u *legoAccountUser) GetPrivateKey() crypto.PrivateKey        { return u.key }

// loadOrCreateAccountKey loads the ACME account's ECDSA private key from
// path, generating and persisting a new P-256 key if none exists.
// atomicWriteFile writes data to path atomically by writing to a temp file in
// the same directory, fsyncing, and renaming into place. Certificate and key
// files must never be left half-written after a crash.
func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".ssl.*.tmp")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("writing temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("syncing temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing temp file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("renaming temp file: %w", err)
	}
	return nil
}

func loadOrCreateAccountKey(path string) (*ecdsa.PrivateKey, error) {
	if data, err := os.ReadFile(path); err == nil {
		if block, _ := pem.Decode(data); block != nil {
			if key, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
				return key, nil
			}
		}
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), crand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate account key: %w", err)
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("marshal account key: %w", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
	if err := os.WriteFile(path, pemBytes, 0o600); err != nil {
		return nil, fmt.Errorf("write account key: %w", err)
	}
	return key, nil
}

// loadOrCreateLegoAccount loads (or initializes) the persisted ACME account
// key and registration resource from cacheDir, so restarts reuse the
// existing account instead of re-registering against Let's Encrypt on
// every startup.
func loadOrCreateLegoAccount(cacheDir, email string) (*legoAccountUser, error) {
	key, err := loadOrCreateAccountKey(filepath.Join(cacheDir, "account.key"))
	if err != nil {
		return nil, err
	}

	user := &legoAccountUser{email: email, key: key}

	if data, err := os.ReadFile(filepath.Join(cacheDir, "account.json")); err == nil {
		var reg registration.Resource
		if err := json.Unmarshal(data, &reg); err == nil {
			user.reg = &reg
		}
	}
	return user, nil
}

// saveLegoRegistration persists the ACME account registration resource to
// cacheDir so future restarts skip re-registration.
func saveLegoRegistration(cacheDir string, reg *registration.Resource) error {
	data, err := json.Marshal(reg)
	if err != nil {
		return fmt.Errorf("marshal registration: %w", err)
	}
	return os.WriteFile(filepath.Join(cacheDir, "account.json"), data, 0o600)
}

// getLetsEncryptTLSConfigDNS01 requests a certificate via the dns-01
// challenge using lego, since golang.org/x/crypto/acme/autocert has no
// DNS-01 hook. This is a separate code path from getLetsEncryptTLSConfig
// (http-01/tls-alpn-01 via autocert) — the two never run for the same
// challenge type, so there is no risk of racing/double-issuing. Certificates
// are cached in {cert_dir}/letsencrypt/{fqdn}/, matching Priority 3 lookup
// in GetTLSConfig so restarts reuse the issued cert without re-requesting.
func (m *Manager) getLetsEncryptTLSConfigDNS01(domains []string) (*tls.Config, error) {
	cacheDir := filepath.Join(m.config.CertDir, "letsencrypt", m.config.FQDN)
	if err := os.MkdirAll(cacheDir, 0700); err != nil {
		return nil, fmt.Errorf("ssl: failed to create cert cache dir %s: %w", cacheDir, err)
	}

	provider, err := buildDNSProvider(m.config.LetsEncrypt.DNSProviderType, m.config.LetsEncrypt.DNSCredentials)
	if err != nil {
		return nil, fmt.Errorf("ssl: dns-01: %w", err)
	}

	user, err := loadOrCreateLegoAccount(cacheDir, m.config.LetsEncrypt.Email)
	if err != nil {
		return nil, fmt.Errorf("ssl: dns-01: account: %w", err)
	}

	legoCfg := lego.NewConfig(user)
	legoCfg.CADirURL = lego.LEDirectoryProduction
	if m.config.LetsEncrypt.Staging {
		legoCfg.CADirURL = LetsEncryptStagingURL
	}

	client, err := lego.NewClient(legoCfg)
	if err != nil {
		return nil, fmt.Errorf("ssl: dns-01: acme client: %w", err)
	}
	if err := client.Challenge.SetDNS01Provider(provider); err != nil {
		return nil, fmt.Errorf("ssl: dns-01: set provider %q: %w", m.config.LetsEncrypt.DNSProviderType, err)
	}

	if user.reg == nil {
		reg, err := client.Registration.Register(registration.RegisterOptions{TermsOfServiceAgreed: true})
		if err != nil {
			return nil, fmt.Errorf("ssl: dns-01: register account: %w", err)
		}
		user.reg = reg
		if err := saveLegoRegistration(cacheDir, reg); err != nil {
			log.Printf("ssl: dns-01: failed to persist ACME account registration: %v", err)
		}
	}

	cert, err := client.Certificate.Obtain(certificate.ObtainRequest{
		Domains: domains,
		Bundle:  true,
	})
	if err != nil {
		return nil, fmt.Errorf("ssl: dns-01: obtain certificate: %w", err)
	}

	certPath := filepath.Join(cacheDir, "fullchain.pem")
	keyPath := filepath.Join(cacheDir, "privkey.pem")
	// Write the private key before the certificate: a crash between the two
	// writes must never leave a certificate with no matching key, which would
	// fail tls.LoadX509KeyPair on the next startup. Each write is atomic.
	if err := atomicWriteFile(keyPath, cert.PrivateKey, 0o600); err != nil {
		return nil, fmt.Errorf("ssl: dns-01: write private key: %w", err)
	}
	if err := atomicWriteFile(certPath, cert.Certificate, 0o644); err != nil {
		return nil, fmt.Errorf("ssl: dns-01: write certificate: %w", err)
	}

	log.Printf("ssl: dns-01: obtained certificate for %v via provider %q", domains, m.config.LetsEncrypt.DNSProviderType)
	return tlsConfigFromFiles(certPath, keyPath)
}

// GetHTTPHandler wraps fallback with the ACME HTTP-01 challenge handler.
// Only active when autocert is managing certificates.
func (m *Manager) GetHTTPHandler(fallback http.Handler) http.Handler {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.certManager != nil {
		return m.certManager.HTTPHandler(fallback)
	}
	return fallback
}

// ParseChallenge normalises a challenge type string to its canonical form.
func ParseChallenge(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "http-01", "http01", "http":
		return "http-01"
	case "tls-alpn-01", "tlsalpn01", "tls-alpn", "tls":
		return "tls-alpn-01"
	case "dns-01", "dns01", "dns":
		return "dns-01"
	default:
		return "http-01"
	}
}

// findCertPair looks for fullchain.pem+privkey.pem or cert.pem+key.pem in dir.
func findCertPair(dir string) (certPath, keyPath string) {
	pairs := [][2]string{
		{filepath.Join(dir, "fullchain.pem"), filepath.Join(dir, "privkey.pem")},
		{filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")},
	}
	for _, p := range pairs {
		if fileReadable(p[0]) && fileReadable(p[1]) {
			return p[0], p[1]
		}
	}
	return "", ""
}

// validateCertFile loads the PEM certificate at certPath, parses the first
// certificate block, and verifies:
//   - not expired
//   - covers fqdn (CN or SAN match), when fqdn is not empty or "localhost"
func validateCertFile(certPath, fqdn string) error {
	data, err := os.ReadFile(certPath)
	if err != nil {
		return fmt.Errorf("cannot read cert: %w", err)
	}

	var cert *x509.Certificate
	for rest := data; len(rest) > 0; {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		cert, err = x509.ParseCertificate(block.Bytes)
		if err != nil {
			return fmt.Errorf("cannot parse certificate: %w", err)
		}
		break
	}
	if cert == nil {
		return fmt.Errorf("no CERTIFICATE PEM block found in %s", certPath)
	}

	if time.Now().After(cert.NotAfter) {
		return fmt.Errorf("certificate expired at %s", cert.NotAfter.Format(time.RFC3339))
	}
	if fqdn == "" || fqdn == "localhost" {
		return nil
	}
	return cert.VerifyHostname(fqdn)
}

// tlsConfigFromFiles loads a TLS certificate and key from disk.
func tlsConfigFromFiles(cert, key string) (*tls.Config, error) {
	tlsCert, err := tls.LoadX509KeyPair(cert, key)
	if err != nil {
		return nil, fmt.Errorf("ssl: failed to load key pair (%s, %s): %w", cert, key, err)
	}
	return &tls.Config{
		Certificates: []tls.Certificate{tlsCert},
		MinVersion:   tls.VersionTLS12,
	}, nil
}

// fileReadable reports whether path exists and can be opened for reading.
func fileReadable(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}
