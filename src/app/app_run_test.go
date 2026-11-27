package app

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/apimgr/cairnbin/src/config"
)

func TestRunStartsServesAndShutsDown(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the integration test exercises the Linux service-user privilege drop")
	}
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve test port: %v", err)
	}
	port := reserved.Addr().(*net.TCPAddr).Port
	if err := reserved.Close(); err != nil {
		t.Fatalf("release test port: %v", err)
	}

	tempBase := projectScopedTempBase()
	if err := os.MkdirAll(tempBase, 0o755); err != nil {
		t.Fatalf("create project-scoped temp base: %v", err)
	}
	root, err := os.MkdirTemp(tempBase, "cairnbin-run-")
	if err != nil {
		t.Fatalf("create app runtime temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	if err := os.Chmod(root, 0o711); err != nil {
		t.Fatalf("allow the service identity to traverse the test root: %v", err)
	}
	configDir := filepath.Join(root, "config")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("create test config directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "server.yml"), []byte("server:\n  address: 127.0.0.1\n  port: \""+strconv.Itoa(port)+"\"\n"), 0o600); err != nil {
		t.Fatalf("write test server config: %v", err)
	}

	args := []string{
		"--address", "127.0.0.1", "--port", strconv.Itoa(port),
		"--config", configDir,
		"--data", filepath.Join(root, "data"),
		"--cache", filepath.Join(root, "cache"),
		"--log", filepath.Join(root, "log"),
		"--backup", filepath.Join(root, "backup"),
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestRunAppHelperProcess$")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("open app helper control pipe: %v", err)
	}
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	childEnv := make([]string, 0, len(os.Environ())+4)
	for _, item := range os.Environ() {
		if !strings.HasPrefix(item, "TMPDIR=") {
			childEnv = append(childEnv, item)
		}
	}
	cmd.Env = append(childEnv, "TMPDIR=/tmp",
		"CAIRNBIN_APP_RUN_HELPER=1",
		"CAIRNBIN_APP_RUN_ARGS="+strings.Join(args, "\n"),
		"CAIRNBIN_APP_RUN_VERSION=test",
	)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start app helper process: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	url := fmt.Sprintf("http://127.0.0.1:%d/server/healthz", port)
	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(20 * time.Second)
	var response *http.Response
	var requestErr error
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			t.Fatalf("app.Run returned before serving: %v\n%s", err, output.String())
		default:
		}
		response, requestErr = client.Get(url)
		if requestErr == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if response == nil {
		_ = stdin.Close()
		select {
		case err := <-done:
			t.Fatalf("server stopped before health request: %v\n%s", err, output.String())
		default:
			t.Fatalf("server did not answer health request before timeout: %v\n%s", requestErr, output.String())
		}
	}
	_ = response.Body.Close()

	_, _ = stdin.Write([]byte{1})
	_ = stdin.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("app.Run after cancellation: %v\n%s", err, output.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("app.Run did not return after context cancellation")
	}
}

func projectScopedTempBase() string {
	for dir := os.TempDir(); dir != filepath.Dir(dir); dir = filepath.Dir(dir) {
		if strings.EqualFold(filepath.Base(dir), "apimgr") {
			return dir
		}
	}
	return filepath.Join(os.TempDir(), "apimgr")
}

func TestRunAppHelperProcess(t *testing.T) {
	if os.Getenv("CAIRNBIN_APP_RUN_HELPER") != "1" {
		return
	}
	args := strings.Split(os.Getenv("CAIRNBIN_APP_RUN_ARGS"), "\n")
	cfg, err := config.Load(args)
	if err != nil {
		t.Fatalf("load app helper config: %v", err)
	}
	cfg.Version = os.Getenv("CAIRNBIN_APP_RUN_VERSION")
	cfg.CommitID = "test"
	cfg.BuildEpoch = "0"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		var signal [1]byte
		_, _ = os.Stdin.Read(signal[:])
		cancel()
	}()
	if err := Run(ctx, *cfg); err != nil {
		t.Fatalf("app.Run: %v", err)
	}
}
