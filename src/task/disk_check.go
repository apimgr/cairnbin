package task

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"
)

// DiskLowConfig describes the filesystems monitored by the disk_check task.
type DiskLowConfig struct {
	// Paths are the data, log, and backup directories to measure.
	Paths []string
	// ThresholdPercent is the used-space percentage that triggers an alert.
	ThresholdPercent int
	// Repeat is the minimum interval between alerts for one volume.
	Repeat time.Duration
	// OperatorEmail receives alerts when email delivery is available.
	OperatorEmail string
	// Mailer sends the disk_low notification; nil disables email delivery.
	Mailer Mailer
	// Log writes leveled, structured operator messages when configured.
	Log func(level, message string, fields ...string)
	// stats allows package tests to provide deterministic filesystem readings.
	stats func(string) (uint64, uint64, error)
}

type diskAlertState struct {
	lastAlert time.Time
	active    bool
}

// DiskCheck returns the scheduler body for low disk alerts. Volumes are
// identified from the filesystem device id where available, with normalized
// volume roots as a portable fallback.
func DiskCheck(cfg DiskLowConfig) func(context.Context) error {
	if cfg.ThresholdPercent <= 0 || cfg.ThresholdPercent > 100 {
		cfg.ThresholdPercent = 85
	}
	if cfg.Repeat <= 0 {
		cfg.Repeat = 24 * time.Hour
	}
	stats := cfg.stats
	if stats == nil {
		stats = diskStats
	}
	var mu sync.Mutex
	states := make(map[string]diskAlertState)
	return func(ctx context.Context) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		seen := make(map[string]struct{}, len(cfg.Paths))
		for _, rawPath := range cfg.Paths {
			path := strings.TrimSpace(rawPath)
			if path == "" {
				continue
			}
			resolved, err := filepath.EvalSymlinks(path)
			if err != nil {
				return fmt.Errorf("disk_check resolve %s: %w", path, err)
			}
			key := diskVolumeKey(resolved)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			free, total, err := stats(resolved)
			if err != nil {
				return fmt.Errorf("disk_check %s: %w", path, err)
			}
			if total == 0 {
				continue
			}
			used := int(float64(total-min(free, total)) * 100 / float64(total))
			now := time.Now()
			mu.Lock()
			state := states[key]
			if used <= max(cfg.ThresholdPercent-5, 0) {
				if state.active {
					diskLog(cfg.Log, "info", "Disk space recovered", "path", path, "used_percent", fmt.Sprint(used), "free_bytes", fmt.Sprint(free))
				}
				delete(states, key)
				mu.Unlock()
				continue
			}
			if used < cfg.ThresholdPercent {
				mu.Unlock()
				continue
			}
			if !state.active || now.Sub(state.lastAlert) >= cfg.Repeat {
				state.active = true
				state.lastAlert = now
				states[key] = state
				mu.Unlock()
				diskLog(cfg.Log, "warn", "Disk space low", "path", path, "used_percent", fmt.Sprint(used), "threshold_percent", fmt.Sprint(cfg.ThresholdPercent), "free_bytes", fmt.Sprint(free))
				if cfg.Mailer != nil && cfg.Mailer.Enabled() && cfg.OperatorEmail != "" {
					if err := cfg.Mailer.Send(cfg.OperatorEmail, "disk_low", map[string]string{
						"path": path, "used_percent": fmt.Sprint(used),
						"threshold_percent": fmt.Sprint(cfg.ThresholdPercent), "free_bytes": fmt.Sprint(free),
					}); err != nil {
						log.Printf("disk_check: send low disk email: %v", err)
					}
				}
				continue
			}
			mu.Unlock()
		}
		return nil
	}
}

func diskLog(logger func(string, string, ...string), level, message string, fields ...string) {
	if logger != nil {
		logger(level, message, fields...)
		return
	}
	log.Printf("%s: %s %s", strings.ToUpper(level), message, strings.Join(fields, " "))
}

func diskVolumeKey(path string) string {
	if info, err := os.Stat(path); err == nil && info.Sys() != nil {
		value := reflect.ValueOf(info.Sys())
		if value.Kind() == reflect.Pointer {
			value = value.Elem()
		}
		if value.IsValid() && value.Kind() == reflect.Struct {
			field := value.FieldByName("Dev")
			if field.IsValid() && field.CanUint() {
				return fmt.Sprintf("device:%d", field.Uint())
			}
		}
	}
	volume := filepath.VolumeName(path)
	if volume == "" {
		volume = string(filepath.Separator)
	}
	return "volume:" + strings.ToLower(volume)
}
