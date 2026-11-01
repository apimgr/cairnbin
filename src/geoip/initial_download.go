//go:build !e2e

package geoip

func skipInitialDownload() bool {
	return false
}
