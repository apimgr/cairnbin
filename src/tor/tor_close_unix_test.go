//go:build !windows

package tor

import (
	"net"
	"net/textproto"
	"os/exec"
	"testing"
	"time"

	"github.com/cretz/bine/control"
	binettor "github.com/cretz/bine/tor"
)

func TestBineTorCloseShutsDownProcessAndControl(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	clientConn, serverConn := net.Pipe()
	defer serverConn.Close()
	ctrl := control.NewConn(textproto.NewConn(clientConn))
	ctrl.Authenticated = true
	go func() {
		server := textproto.NewConn(serverConn)
		for {
			line, err := server.ReadLine()
			if err != nil {
				return
			}
			if line == "SIGNAL SHUTDOWN" {
				_ = cmd.Process.Kill()
			}
			if err := server.PrintfLine("250 OK"); err != nil || line == "QUIT" {
				return
			}
		}
	}()

	instance := &binettor.Tor{
		Control:            ctrl,
		Process:            &pidCapturingProcess{Cmd: cmd},
		ProcessCancelFunc:  func() { _ = cmd.Process.Kill() },
		StopProcessOnClose: true,
	}
	started := time.Now()
	if err := (bineTor{tor: instance}).Close(); err != nil {
		t.Fatalf("close Tor: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("graceful close took %s; want bounded control shutdown", elapsed)
	}
}
