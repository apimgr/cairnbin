// Package geoip provides GeoIP lookup and country-blocking middleware using
// ip-location-db MMDB files (MIT-licensed, no API key required).
// Databases are downloaded on first use and stored in a configurable directory.
// The package uses maxminddb-golang directly because ip-location-db uses
// non-MaxMind database_type strings that geoip2-golang rejects.
package geoip

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	maxminddb "github.com/oschwald/maxminddb-golang"
	"github.com/apimgr/cairnbin/src/common/retry"
)

// Database URLs from sapics/ip-location-db. ASN/Country are served via
// jsDelivr CDN; the City databases are served from GitHub Releases directly.
// PART 19 requires separate IPv4 and IPv6 city databases (dbip-city-ipv4.mmdb,
// dbip-city-ipv6.mmdb) — a single combined city.mmdb is not sufficient.
//
// PART 19 is explicit that there is no separate "WHOIS" database: the
// geo-whois-asn-country package name is misleading and exposes only
// country_code. Organization-name data comes solely from the ASN database's
// autonomous_system_organization field — there is no urlWHOIS constant.
//
// These are vars, not consts, so tests can point them at an httptest server.
// Update() deletes the destination before calling ensureFile, so every download
// URL is always hit on the Update path — without this override, any test that
// exercises Update talks to the real CDN and blocks for the 5-minute
// ensureFile context timeout, per database.
var (
	urlASN     = "https://cdn.jsdelivr.net/npm/@ip-location-db/asn-mmdb/asn.mmdb"
	urlCountry = "https://cdn.jsdelivr.net/npm/@ip-location-db/geo-whois-asn-country-mmdb/geo-whois-asn-country.mmdb"
	urlCityV4  = "https://github.com/sapics/ip-location-db/releases/download/latest/dbip-city-ipv4.mmdb"
	urlCityV6  = "https://github.com/sapics/ip-location-db/releases/download/latest/dbip-city-ipv6.mmdb"
)

// ASNRecord holds ASN lookup fields from the asn.mmdb database.
type ASNRecord struct {
	ASN uint32 `maxminddb:"autonomous_system_number"`
	Org string `maxminddb:"autonomous_system_organization"`
}

// CountryRecord holds country lookup fields from the country database.
type CountryRecord struct {
	CountryCode string `maxminddb:"country_code"`
}

// CityRecord holds city lookup fields from the city database.
type CityRecord struct {
	City        string  `maxminddb:"city"`
	CountryCode string  `maxminddb:"country_code"`
	Latitude    float64 `maxminddb:"latitude"`
	Longitude   float64 `maxminddb:"longitude"`
	Postcode    string  `maxminddb:"postcode"`
	State1      string  `maxminddb:"state1"`
	State2      string  `maxminddb:"state2"`
	Timezone    string  `maxminddb:"timezone"`
}

// Info is the combined GeoIP result for a single IP address.
type Info struct {
	IP          string
	CountryCode string
	City        string
	State1      string
	Timezone    string
	Latitude    float64
	Longitude   float64
	ASN         uint32
	ASNOrg      string
}

// Config is consumed by Open; mirrors config.GeoIPConfig.
type Config struct {
	Dir           string
	EnableASN     bool
	EnableCountry bool
	EnableCity    bool
	// DenyCountries blocks the listed codes; AllowCountries allows ONLY the
	// listed ones. When both are set, AllowCountries wins; when both are empty
	// no country blocking happens.
	DenyCountries  []string
	AllowCountries []string
	// IPs that always bypass country blocking
	Allowlist []string
}

// DB holds open MMDB handles and the blocking configuration.
type DB struct {
	mu      sync.RWMutex
	asnDB   *maxminddb.Reader
	countDB *maxminddb.Reader
	// cityDB is the IPv4 city database (dbip-city-ipv4.mmdb).
	cityDB *maxminddb.Reader
	// cityDBv6 is the IPv6 city database (dbip-city-ipv6.mmdb).
	cityDBv6 *maxminddb.Reader
	cfg      Config
	// countryOverride, when non-nil, supplies the country code for an IP in
	// place of the MMDB lookup. It lets tests exercise the country-policy paths
	// without a committed binary database fixture; it is always nil in production.
	countryOverride func(net.IP) string
}

// Open downloads any missing MMDB files and opens the configured databases.
// If no databases are enabled, a no-op DB is returned (all lookups return nil).
func Open(cfg Config) (*DB, error) {
	d := &DB{cfg: cfg}

	if err := os.MkdirAll(cfg.Dir, 0o750); err != nil {
		return nil, fmt.Errorf("geoip: create dir: %w", err)
	}
	if skipInitialDownload() {
		return d, nil
	}

	if cfg.EnableASN {
		path := filepath.Join(cfg.Dir, "asn.mmdb")
		if err := ensureFile(path, urlASN); err != nil {
			log.Printf("geoip: warning: ASN database unavailable: %v", err)
		} else {
			r, err := maxminddb.Open(path)
			if err != nil {
				log.Printf("geoip: warning: open asn.mmdb: %v", err)
			} else {
				d.asnDB = r
			}
		}
	}

	if cfg.EnableCountry {
		path := filepath.Join(cfg.Dir, "country.mmdb")
		if err := ensureFile(path, urlCountry); err != nil {
			log.Printf("geoip: warning: country database unavailable: %v", err)
		} else {
			r, err := maxminddb.Open(path)
			if err != nil {
				log.Printf("geoip: warning: open country.mmdb: %v", err)
			} else {
				d.countDB = r
			}
		}
	}

	if cfg.EnableCity {
		path := filepath.Join(cfg.Dir, "dbip-city-ipv4.mmdb")
		if err := ensureFile(path, urlCityV4); err != nil {
			log.Printf("geoip: warning: city (IPv4) database unavailable: %v", err)
		} else {
			r, err := maxminddb.Open(path)
			if err != nil {
				log.Printf("geoip: warning: open dbip-city-ipv4.mmdb: %v", err)
			} else {
				d.cityDB = r
			}
		}

		pathV6 := filepath.Join(cfg.Dir, "dbip-city-ipv6.mmdb")
		if err := ensureFile(pathV6, urlCityV6); err != nil {
			log.Printf("geoip: warning: city (IPv6) database unavailable: %v", err)
		} else {
			r, err := maxminddb.Open(pathV6)
			if err != nil {
				log.Printf("geoip: warning: open dbip-city-ipv6.mmdb: %v", err)
			} else {
				d.cityDBv6 = r
			}
		}
	}

	return d, nil
}

// Update downloads fresh copies of all configured databases and reopens the
// handles atomically. Intended to be called by the weekly scheduler task.
func (d *DB) Update() error {
	d.mu.Lock()
	defer d.mu.Unlock()

	var errs []string

	if d.cfg.EnableASN {
		path := filepath.Join(d.cfg.Dir, "asn.mmdb")
		// Remove so ensureFile always downloads.
		_ = os.Remove(path)
		if err := ensureFile(path, urlASN); err != nil {
			errs = append(errs, fmt.Sprintf("asn: %v", err))
		} else if r, err := maxminddb.Open(path); err != nil {
			errs = append(errs, fmt.Sprintf("open asn: %v", err))
		} else {
			if d.asnDB != nil {
				_ = d.asnDB.Close()
			}
			d.asnDB = r
		}
	}

	if d.cfg.EnableCountry {
		path := filepath.Join(d.cfg.Dir, "country.mmdb")
		_ = os.Remove(path)
		if err := ensureFile(path, urlCountry); err != nil {
			errs = append(errs, fmt.Sprintf("country: %v", err))
		} else if r, err := maxminddb.Open(path); err != nil {
			errs = append(errs, fmt.Sprintf("open country: %v", err))
		} else {
			if d.countDB != nil {
				_ = d.countDB.Close()
			}
			d.countDB = r
		}
	}

	if d.cfg.EnableCity {
		path := filepath.Join(d.cfg.Dir, "dbip-city-ipv4.mmdb")
		_ = os.Remove(path)
		if err := ensureFile(path, urlCityV4); err != nil {
			errs = append(errs, fmt.Sprintf("city ipv4: %v", err))
		} else if r, err := maxminddb.Open(path); err != nil {
			errs = append(errs, fmt.Sprintf("open city ipv4: %v", err))
		} else {
			if d.cityDB != nil {
				_ = d.cityDB.Close()
			}
			d.cityDB = r
		}

		pathV6 := filepath.Join(d.cfg.Dir, "dbip-city-ipv6.mmdb")
		_ = os.Remove(pathV6)
		if err := ensureFile(pathV6, urlCityV6); err != nil {
			errs = append(errs, fmt.Sprintf("city ipv6: %v", err))
		} else if r, err := maxminddb.Open(pathV6); err != nil {
			errs = append(errs, fmt.Sprintf("open city ipv6: %v", err))
		} else {
			if d.cityDBv6 != nil {
				_ = d.cityDBv6.Close()
			}
			d.cityDBv6 = r
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("geoip update errors: %s", strings.Join(errs, "; "))
	}
	return nil
}

// Close releases all open database handles.
func (d *DB) Close() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.asnDB != nil {
		_ = d.asnDB.Close()
		d.asnDB = nil
	}
	if d.countDB != nil {
		_ = d.countDB.Close()
		d.countDB = nil
	}
	if d.cityDB != nil {
		_ = d.cityDB.Close()
		d.cityDB = nil
	}
	if d.cityDBv6 != nil {
		_ = d.cityDBv6.Close()
		d.cityDBv6 = nil
	}
}

// Lookup returns GeoIP information for the given IP address.
// Returns nil if GeoIP is not available or the IP is invalid.
func (d *DB) Lookup(ip net.IP) *Info {
	if d == nil || ip == nil {
		return nil
	}
	d.mu.RLock()
	defer d.mu.RUnlock()

	info := &Info{IP: ip.String()}

	if d.countDB != nil {
		var rec CountryRecord
		if err := d.countDB.Lookup(ip, &rec); err == nil {
			info.CountryCode = rec.CountryCode
		}
	}

	if d.asnDB != nil {
		var rec ASNRecord
		if err := d.asnDB.Lookup(ip, &rec); err == nil {
			info.ASN = rec.ASN
			info.ASNOrg = rec.Org
		}
	}

	// City lookups are split by IP family: dbip-city-ipv4.mmdb for IPv4,
	// dbip-city-ipv6.mmdb for IPv6 (AI.md PART 19).
	cityDB := d.cityDB
	if ip.To4() == nil {
		cityDB = d.cityDBv6
	}
	if cityDB != nil {
		var rec CityRecord
		if err := cityDB.Lookup(ip, &rec); err == nil {
			info.City = rec.City
			if info.CountryCode == "" {
				info.CountryCode = rec.CountryCode
			}
			info.State1 = rec.State1
			info.Timezone = rec.Timezone
			info.Latitude = rec.Latitude
			info.Longitude = rec.Longitude
		}
	}

	if d.countryOverride != nil {
		info.CountryCode = d.countryOverride(ip)
	}

	return info
}

// countryMode resolves the effective country-blocking mode.
//
// PART 19 defines the mode purely by the contents of the two lists: an
// allowlist wins when both are populated, and with neither list set there is
// no country blocking at all.
func (d *DB) countryMode() string {
	if len(d.cfg.AllowCountries) > 0 {
		return "allow"
	}
	if len(d.cfg.DenyCountries) > 0 {
		return "deny"
	}
	return "none"
}

// OnBlocked, when set, is called for every request blocked by the country
// policy so the server can record a security.country_blocked audit event.
var OnBlocked func(r *http.Request, country string)

// IsBlocked returns true if the IP should be blocked based on country policy.
//
// Blocking logic (from spec PART 19):
//   - RFC 1918 / loopback / link-local IPs are never blocked
//   - IPs in the allowlist always pass
//   - Mode "allow": block unless country is in AllowCountries, but fail-open
//     (never block) when the country cannot be determined
//   - Mode "deny": block if country is in DenyCountries
//   - Mode "none": never block
func (d *DB) IsBlocked(ip net.IP) bool {
	if d == nil || ip == nil {
		return false
	}

	// Never block private/loopback/link-local addresses.
	if isPrivate(ip) {
		return false
	}

	// Allowlist bypass.
	ipStr := ip.String()
	for _, a := range d.cfg.Allowlist {
		if a == ipStr {
			return false
		}
		if _, net, err := net.ParseCIDR(a); err == nil && net.Contains(ip) {
			return false
		}
	}

	mode := d.countryMode()
	if mode == "none" {
		return false
	}

	info := d.Lookup(ip)
	cc := ""
	if info != nil {
		cc = strings.ToUpper(info.CountryCode)
	}

	if mode == "allow" {
		// Fail-open: if the country cannot be determined (DB missing, stale, or
		// lookup failed) never block — GeoIP is a risk signal, not an auth gate
		// (PART 19, AI.md:27279).
		if cc == "" {
			return false
		}
		for _, c := range d.cfg.AllowCountries {
			if strings.ToUpper(c) == cc {
				return false
			}
		}
		return true
	}

	for _, c := range d.cfg.DenyCountries {
		if strings.ToUpper(c) == cc {
			return true
		}
	}

	return false
}

// Middleware returns a chi-compatible middleware that blocks requests from
// country-blocked IPs. Must run after realIPMiddleware (which rewrites
// r.RemoteAddr to the true client IP per PART 12 trusted-proxy rules).
func (d *DB) Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := realIP(r)
			if ip != nil && d.IsBlocked(ip) {
				// Log the block without retaining the exact IP (redact last octet).
				info := d.Lookup(ip)
				cc := ""
				if info != nil {
					cc = info.CountryCode
				}
				log.Printf("geoip_block: ip=[redacted], country=%s", cc)
				if OnBlocked != nil {
					OnBlocked(r, cc)
				}
				http.Error(w, "access denied: your region is not permitted", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// LookupRequest returns GeoIP info for the remote address of an HTTP request.
// The server's realIPMiddleware (PART 12) must have already run so that
// r.RemoteAddr contains the true client IP, not the proxy's IP.
func (d *DB) LookupRequest(r *http.Request) *Info {
	ip := realIP(r)
	if ip == nil {
		return nil
	}
	return d.Lookup(ip)
}

// realIP extracts the client IP from r.RemoteAddr only. Forwarded-IP headers
// (X-Real-IP, X-Forwarded-For, etc.) are intentionally NOT read here; the
// server's realIPMiddleware already applied the PART 12 trusted-proxy gate and
// rewrote r.RemoteAddr before any of these middleware functions run.
func realIP(r *http.Request) net.IP {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return net.ParseIP(host)
}

// isPrivate returns true for RFC 1918, loopback, link-local, and ULA addresses.
func isPrivate(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return true
	}
	private := []string{
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"fc00::/7",
		// CGNAT
		"100.64.0.0/10",
	}
	for _, cidr := range private {
		_, network, err := net.ParseCIDR(cidr)
		if err != nil {
			continue
		}
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

// ensureFile downloads src to dst if dst does not exist or is empty.
func ensureFile(dst, src string) error {
	if info, err := os.Stat(dst); err == nil && info.Size() > 0 {
		// already present
		return nil
	}

	log.Printf("geoip: downloading %s → %s", src, dst)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	return retry.Do(ctx, func() error { return downloadOnce(ctx, dst, src) })
}

// downloadOnce is a single download attempt for ensureFile.
func downloadOnce(ctx context.Context, dst, src string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", "cairnbin-geoip/1.0")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return &retry.StatusError{Code: resp.StatusCode, URL: src}
	}

	tmp := dst + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
	if err != nil {
		return fmt.Errorf("create tmp file: %w", err)
	}

	// Cap download at 512 MiB to guard against runaway responses.
	lr := io.LimitReader(resp.Body, 512<<20)
	if _, err := io.Copy(f, lr); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("write: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close: %w", err)
	}

	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename: %w", err)
	}

	log.Printf("geoip: downloaded %s (%.1f MiB)", filepath.Base(dst), float64(mustStat(dst))/float64(1<<20))
	return nil
}

func mustStat(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}
