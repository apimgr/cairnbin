//go:build windows

package service

import (
	"log"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// IsWindowsService reports whether the process was started by the Windows
// Service Control Manager (SCM). Returns false when running interactively.
func IsWindowsService() bool {
	ok, err := svc.IsWindowsService()
	if err != nil {
		return false
	}
	return ok
}

// isPrivileged is an alias for isElevated for backward compatibility.
func isPrivileged() bool { return isElevated() }

// isWindowsServiceInstalled reports whether the service is registered with the
// Service Control Manager. Used by needsEscalationForService to decide whether
// managing it requires an elevated (administrator) token.
func isWindowsServiceInstalled() bool {
	m, err := mgr.Connect()
	if err != nil {
		return false
	}
	defer m.Disconnect()

	s, err := m.OpenService(appName)
	if err != nil {
		return false
	}
	s.Close()
	return true
}

// windowsService wraps a user-supplied start function as a svc.Handler.
// The function is expected to block for the lifetime of the service.
type windowsService struct {
	start      func()
	startWithStop func(<-chan struct{})
	stop       chan struct{}
	stopSignal chan struct{}
}

// Execute implements svc.Handler. It transitions the service through
// StartPending → Running, runs start() in a goroutine, then waits for a
// Stop or Shutdown command before returning.
func (ws *windowsService) Execute(_ []string, r <-chan svc.ChangeRequest, s chan<- svc.Status) (bool, uint32) {
	s <- svc.Status{State: svc.StartPending}

	go func() {
		defer close(ws.stop)
		if ws.startWithStop != nil {
			ws.startWithStop(ws.stopSignal)
			return
		}
		ws.start()
	}()

	s <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}

	for c := range r {
		switch c.Cmd {
		case svc.Stop, svc.Shutdown:
			s <- svc.Status{State: svc.StopPending}
			if ws.stopSignal != nil {
				close(ws.stopSignal)
			}
			select {
			case <-ws.stop:
			case <-time.After(20 * time.Second):
			}
			return false, 0
		default:
			log.Printf("service: unexpected control request #%d", c)
		}
	}

	return false, 0
}

// RunAsWindowsService starts the Windows SCM service loop with appName as
// the service name. start is called in a goroutine and must block for the
// duration of the service; when SCM sends Stop/Shutdown the handler returns
// and the process exits cleanly.
func RunAsWindowsService(appName string, start func()) error {
	ws := &windowsService{
		start: start,
		stop:  make(chan struct{}),
	}
	return svc.Run(appName, ws)
}

// RunAsWindowsServiceWithStop passes SCM stop/shutdown notifications to the
// service body so it can drain requests and complete its graceful shutdown.
func RunAsWindowsServiceWithStop(appName string, start func(<-chan struct{})) error {
	ws := &windowsService{startWithStop: start, stop: make(chan struct{}), stopSignal: make(chan struct{})}
	return svc.Run(appName, ws)
}
