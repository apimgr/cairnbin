package task

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	defaultNVDSource = "https://services.nvd.nist.gov/rest/json/cves/2.0"
	nvdPageSize      = 2000
	nvdWindowLimit   = 120 * 24 * time.Hour
	nvdResponseLimit = 256 << 20
)

// NVDConfig describes the NVD source and API key used by the cve_update task.
type NVDConfig struct {
	Source string
	APIKey string
}

type nvdPage struct {
	TotalResults    int `json:"totalResults"`
	ResultsPerPage  int `json:"resultsPerPage"`
	StartIndex      int `json:"startIndex"`
	Vulnerabilities []struct {
		CVE json.RawMessage `json:"cve"`
	} `json:"vulnerabilities"`
}

type nvdStoredCVE struct {
	ID string `json:"id"`
}

// NVDUpdate downloads the NVD API's paged CVE feed and atomically stores its
// CVE objects as a JSON array. Later runs request changes since .last_updated
// and replace records by CVE id while preserving the previous good copy on any
// failed page, validation error, or write failure.
func NVDUpdate(dataDir string, cfg NVDConfig, client *http.Client) func(context.Context) error {
	return func(ctx context.Context) error {
		return updateNVD(ctx, dataDir, cfg, client)
	}
}

func updateNVD(ctx context.Context, dataDir string, cfg NVDConfig, client *http.Client) error {
	dir := filepath.Join(dataDir, "security", "cve")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("cve_update: create NVD directory: %w", err)
	}
	source := strings.TrimSpace(cfg.Source)
	if source == "" {
		source = defaultNVDSource
	}
	parsed, err := url.Parse(source)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return fmt.Errorf("cve_update: invalid NVD API source")
	}
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Minute}
	}
	path := filepath.Join(dir, "nvd.json")
	lastUpdatedPath := filepath.Join(dir, ".last_updated")
	lastUpdated, _ := readNVDUpdateTime(lastUpdatedPath)
	records, recordsErr := readNVDRecords(path)
	if recordsErr != nil {
		lastUpdated = time.Time{}
		records = make(map[string]json.RawMessage)
	}
	fullUpdate := lastUpdated.IsZero() || len(records) == 0
	now := time.Now().UTC().Truncate(time.Second)
	updated := make(map[string]json.RawMessage)
	if fullUpdate {
		if err := fetchNVDRange(ctx, client, source, cfg.APIKey, time.Time{}, time.Time{}, updated); err != nil {
			return err
		}
	} else if now.After(lastUpdated) {
		for start := lastUpdated; start.Before(now); {
			end := start.Add(nvdWindowLimit)
			if end.After(now) {
				end = now
			}
			if err := fetchNVDRange(ctx, client, source, cfg.APIKey, start, end, updated); err != nil {
				return err
			}
			start = end
		}
	}
	if fullUpdate {
		if len(updated) == 0 {
			return fmt.Errorf("cve_update: NVD returned no valid CVE records")
		}
		records = updated
	} else {
		for id, record := range updated {
			records[id] = record
		}
	}
	if len(records) == 0 {
		return fmt.Errorf("cve_update: no usable NVD records")
	}
	encoded, err := marshalNVDRecords(records)
	if err != nil {
		return fmt.Errorf("cve_update: encode NVD records: %w", err)
	}
	if err := atomicWriteNVD(path, encoded); err != nil {
		return fmt.Errorf("cve_update: store NVD records: %w", err)
	}
	if err := atomicWriteNVD(lastUpdatedPath, []byte(now.Format(time.RFC3339)+"\n")); err != nil {
		return fmt.Errorf("cve_update: store NVD update time: %w", err)
	}
	log.Printf("cve_update: NVD database refreshed with %d records", len(records))
	return nil
}

func fetchNVDRange(ctx context.Context, client *http.Client, source, apiKey string, from, to time.Time, records map[string]json.RawMessage) error {
	totalResults := -1
	for startIndex := 0; ; {
		if err := ctx.Err(); err != nil {
			return err
		}
		requestURL, err := url.Parse(source)
		if err != nil {
			return fmt.Errorf("cve_update: parse NVD API URL: %w", err)
		}
		query := requestURL.Query()
		query.Set("resultsPerPage", fmt.Sprint(nvdPageSize))
		query.Set("startIndex", fmt.Sprint(startIndex))
		if !from.IsZero() {
			query.Set("lastModStartDate", nvdTime(from))
			query.Set("lastModEndDate", nvdTime(to))
		}
		requestURL.RawQuery = query.Encode()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
		if err != nil {
			return fmt.Errorf("cve_update: create NVD request: %w", err)
		}
		req.Header.Set("Accept", "application/json")
		if strings.TrimSpace(apiKey) != "" {
			req.Header.Set("apiKey", strings.TrimSpace(apiKey))
		}
		if err := waitNVDRateLimit(ctx, requestURL.Hostname(), apiKey); err != nil {
			return err
		}
		response, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("cve_update: request NVD page at startIndex %d: %w", startIndex, err)
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, nvdResponseLimit+1))
		closeErr := response.Body.Close()
		if readErr != nil {
			return fmt.Errorf("cve_update: read NVD response: %w", readErr)
		}
		if closeErr != nil {
			return fmt.Errorf("cve_update: close NVD response: %w", closeErr)
		}
		if len(body) > nvdResponseLimit {
			return fmt.Errorf("cve_update: NVD response exceeded the size limit")
		}
		if response.StatusCode == http.StatusForbidden || response.StatusCode == http.StatusTooManyRequests {
			return fmt.Errorf("cve_update: NVD returned HTTP %d; the next scheduled run will retry", response.StatusCode)
		}
		if response.StatusCode != http.StatusOK {
			return fmt.Errorf("cve_update: NVD returned HTTP %d", response.StatusCode)
		}
		var page nvdPage
		if err := json.Unmarshal(body, &page); err != nil {
			return fmt.Errorf("cve_update: parse NVD page: %w", err)
		}
		if page.StartIndex != startIndex || page.TotalResults < 0 {
			return fmt.Errorf("cve_update: NVD returned inconsistent pagination metadata")
		}
		if totalResults == -1 {
			totalResults = page.TotalResults
		} else if totalResults != page.TotalResults {
			return fmt.Errorf("cve_update: NVD totalResults changed during pagination")
		}
		for _, item := range page.Vulnerabilities {
			var cve nvdStoredCVE
			if len(item.CVE) == 0 || json.Unmarshal(item.CVE, &cve) != nil || strings.TrimSpace(cve.ID) == "" {
				return fmt.Errorf("cve_update: NVD returned a malformed CVE record")
			}
			records[cve.ID] = append(json.RawMessage(nil), item.CVE...)
		}
		if startIndex+len(page.Vulnerabilities) >= totalResults {
			return nil
		}
		if len(page.Vulnerabilities) == 0 {
			return fmt.Errorf("cve_update: NVD returned an empty page before totalResults was reached")
		}
		startIndex += len(page.Vulnerabilities)
	}
}

func waitNVDRateLimit(ctx context.Context, host, apiKey string) error {
	if !strings.EqualFold(host, "services.nvd.nist.gov") {
		return nil
	}
	interval := 6 * time.Second
	if strings.TrimSpace(apiKey) != "" {
		interval = 600 * time.Millisecond
	}
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func nvdTime(value time.Time) string {
	return value.UTC().Format("2006-01-02T15:04:05.000Z")
}

func readNVDUpdateTime(path string) (time.Time, error) {
	value, err := os.ReadFile(path)
	if err != nil {
		return time.Time{}, err
	}
	parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(string(value)))
	if err != nil {
		return time.Time{}, err
	}
	return parsed.UTC(), nil
}

func readNVDRecords(path string) (map[string]json.RawMessage, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var values []json.RawMessage
	if err := json.Unmarshal(content, &values); err != nil || len(values) == 0 {
		return nil, errors.New("NVD data is not a non-empty CVE array")
	}
	records := make(map[string]json.RawMessage, len(values))
	for _, value := range values {
		var cve nvdStoredCVE
		if json.Unmarshal(value, &cve) != nil || strings.TrimSpace(cve.ID) == "" {
			return nil, errors.New("NVD data contains a malformed CVE record")
		}
		records[cve.ID] = append(json.RawMessage(nil), value...)
	}
	return records, nil
}

func marshalNVDRecords(records map[string]json.RawMessage) ([]byte, error) {
	ids := make([]string, 0, len(records))
	for id := range records {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	values := make([]json.RawMessage, 0, len(ids))
	for _, id := range ids {
		values = append(values, records[id])
	}
	return json.MarshalIndent(values, "", "  ")
}

func atomicWriteNVD(path string, content []byte) error {
	temp, err := os.CreateTemp(filepath.Dir(path), ".nvd-update-*")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if err := temp.Chmod(0o640); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(content); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempName, path)
}
