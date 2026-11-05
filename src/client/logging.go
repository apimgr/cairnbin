package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/apimgr/cairnbin/src/client/paths"
)

func initializeLogging(cfg cliConfig) {
	path := strings.TrimSpace(cfg.Logging.File)
	if path == "" {
		path = paths.LogFile()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	if os.Chmod(filepath.Dir(path), 0o700) != nil {
		return
	}
	maxSize, err := parseByteSize(cfg.Logging.MaxSize)
	if err != nil || maxSize <= 0 {
		maxSize = 10 * 1024 * 1024
	}
	maxFiles := cfg.Logging.MaxFiles
	if maxFiles < 1 {
		maxFiles = 5
	}
	rotateLog(path, maxSize, maxFiles)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	if file.Chmod(0o600) != nil {
		_ = file.Close()
		return
	}
	log.SetOutput(io.MultiWriter(os.Stderr, file))
	log.SetFlags(log.LstdFlags)
	if level := strings.ToLower(strings.TrimSpace(cfg.Logging.Level)); level != "" && level != "warn" {
		log.SetPrefix("[" + level + "] ")
	}
}

func rotateLog(path string, maxSize int64, maxFiles int) {
	info, err := os.Stat(path)
	if err != nil || info.Size() < maxSize {
		return
	}
	_ = os.Remove(path + "." + strconv.Itoa(maxFiles))
	for i := maxFiles - 1; i >= 1; i-- {
		_ = os.Rename(path+"."+strconv.Itoa(i), path+"."+strconv.Itoa(i+1))
	}
	_ = os.Rename(path, path+".1")
}

func parseByteSize(value string) (int64, error) {
	value = strings.TrimSpace(strings.ToUpper(value))
	if value == "" {
		return 0, fmt.Errorf("empty size")
	}
	unit := int64(1)
	for _, candidate := range []struct {
		suffix     string
		multiplier int64
	}{{"GIB", 1024 * 1024 * 1024}, {"GB", 1024 * 1024 * 1024}, {"MIB", 1024 * 1024}, {"MB", 1024 * 1024}, {"KIB", 1024}, {"KB", 1024}, {"B", 1}} {
		if strings.HasSuffix(value, candidate.suffix) {
			unit = candidate.multiplier
			value = strings.TrimSpace(strings.TrimSuffix(value, candidate.suffix))
			break
		}
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || n < 0 || (unit > 1 && n > int64(^uint64(0)>>1)/unit) {
		return 0, fmt.Errorf("invalid size")
	}
	return n * unit, nil
}

func parseDurationOr(value string, fallback time.Duration) time.Duration {
	if parsed, err := time.ParseDuration(value); err == nil && parsed > 0 {
		return parsed
	}
	return fallback
}

func writePagedOutput(content []byte, mode string) error {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		mode = "auto"
	}
	stdoutInfo, err := os.Stdout.Stat()
	if err != nil {
		return err
	}
	if !shouldUsePager(mode, stdoutInfo.Mode()&os.ModeCharDevice != 0, content) {
		_, err := os.Stdout.Write(content)
		return err
	}
	command := strings.Fields(os.Getenv("PAGER"))
	if len(command) == 0 {
		if _, err := exec.LookPath("less"); err == nil {
			command = []string{"less", "-FRX"}
		} else if _, err := exec.LookPath("more"); err == nil {
			command = []string{"more"}
		}
	}
	if len(command) == 0 {
		_, err := os.Stdout.Write(content)
		return err
	}
	cmd := exec.Command(command[0], command[1:]...)
	cmd.Stdin = strings.NewReader(string(content))
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		_, writeErr := os.Stdout.Write(content)
		if writeErr != nil {
			return writeErr
		}
		return nil
	}
	return nil
}

func shouldUsePager(mode string, stdoutIsTerminal bool, content []byte) bool {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "always":
		return true
	case "auto":
		return stdoutIsTerminal && (len(content) > 4096 || strings.Count(string(content), "\n") > 24)
	default:
		return false
	}
}
