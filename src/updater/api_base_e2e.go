//go:build e2e

package updater

import "os"

func configuredAPIBase() string {
	if apiURL := os.Getenv("CAIRNBIN_E2E_UPDATE_API_URL"); apiURL != "" {
		return apiURL
	}
	return apiBase
}
