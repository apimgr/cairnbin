//go:build e2e

package geoip

import "os"

func skipInitialDownload() bool {
	return os.Getenv("PASTEBIN_E2E_GEOIP_SKIP_INITIAL_DOWNLOAD") == "true"
}

func init() {
	base := os.Getenv("PASTEBIN_E2E_GEOIP_BASE_URL")
	if base == "" {
		return
	}
	urlASN = base + "/asn"
	urlCountry = base + "/country"
	urlCityV4 = base + "/city-ipv4"
	urlCityV6 = base + "/city-ipv6"
}
