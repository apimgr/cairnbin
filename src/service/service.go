package service

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/apimgr/pastebin/src/path"
	"github.com/apimgr/pastebin/src/pid"
)

const (
	appName     = "pastebin"
	orgName     = "apimgr"
	serviceUser = "pastebin"
	// launchdLabel is the reverse-DNS identifier for the macOS launchd plist.
	// PART 3 derives {plist_name} as io.github.{project_org}.{internal_name}.
	launchdLabel = "io.github." + orgName + "." + appName
)

// reservedIDs is the set of system UID/GID values that must not be used for the
// service account. These cover well-known daemon UIDs on common Linux distros.
var reservedIDs = map[int]bool{}

func init() {
	// Reserve ranges 980-999 and 101-110 and 170-179 per spec.
	for i := 980; i <= 999; i++ {
		reservedIDs[i] = true
	}
	for i := 101; i <= 110; i++ {
		reservedIDs[i] = true
	}
	for i := 170; i <= 179; i++ {
		reservedIDs[i] = true
	}
	// Reserve 65534 (nobody/nogroup — universal nobody user) per spec.
	reservedIDs[65534] = true
}

// findAvailableSystemID scans from 899 down to 200 and returns the first UID/GID
// that is not reserved, not in /etc/passwd, and not in /etc/group (PART 23).
func findAvailableSystemID() (int, error) {
	for id := 899; id >= 200; id-- {
		if reservedIDs[id] {
			continue
		}
		// Check if UID is already in use.
		if _, err := user.LookupId(strconv.Itoa(id)); err == nil {
			continue
		}
		// Check if GID is already in use.
		if _, err := user.LookupGroupId(strconv.Itoa(id)); err == nil {
			continue
		}
		return id, nil
	}
	return 0, fmt.Errorf("no available system UID/GID in range 200-899")
}

// ok returns "✅ " when color/emoji output is enabled, or "[ok] " when NO_COLOR is set.
func ok() string {
	if os.Getenv("NO_COLOR") != "" {
		return "[ok] "
	}
	return "✅ "
}

// ServiceType represents the type of service manager
type ServiceType int

const (
	ServiceUnknown ServiceType = iota
	ServiceSystemd
	ServiceOpenRC
	ServiceSysV
	ServiceRunit
	ServiceLaunchd
	ServiceWindows
	ServiceBSDRC
)

// DetectServiceManager detects the system's service manager
func DetectServiceManager() ServiceType {
	switch runtime.GOOS {
	case "linux":
		// Check for systemd
		if _, err := os.Stat("/run/systemd/system"); err == nil {
			return ServiceSystemd
		}
		if _, err := os.Stat("/etc/systemd"); err == nil {
			return ServiceSystemd
		}
		// Check for OpenRC (Alpine, Gentoo, Devuan)
		if _, err := os.Stat("/sbin/openrc-run"); err == nil {
			return ServiceOpenRC
		}
		// Check for runit
		if _, err := os.Stat("/run/runit"); err == nil {
			return ServiceRunit
		}
		// Check for SysVinit — /etc/init.d exists with update-rc.d or chkconfig
		if _, err := os.Stat("/etc/init.d"); err == nil {
			if _, err2 := exec.LookPath("update-rc.d"); err2 == nil {
				return ServiceSysV
			}
			if _, err2 := exec.LookPath("chkconfig"); err2 == nil {
				return ServiceSysV
			}
		}
		return ServiceUnknown

	case "darwin":
		return ServiceLaunchd

	case "windows":
		return ServiceWindows

	case "freebsd", "openbsd", "netbsd":
		return ServiceBSDRC

	default:
		return ServiceUnknown
	}
}

// fileExists reports whether path exists (of any type).
func fileExists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// systemServicePaths returns the on-disk locations a system-wide service
// definition can occupy for the service managers this binary supports.
func systemServicePaths() []string {
	return []string{
		fmt.Sprintf("/etc/systemd/system/%s.service", appName),
		fmt.Sprintf("/Library/LaunchDaemons/%s.plist", launchdLabel),
		fmt.Sprintf("/usr/local/etc/rc.d/%s", appName),
		fmt.Sprintf("/etc/init.d/%s", appName),
		fmt.Sprintf("/etc/sv/%s", appName),
	}
}

// needsEscalationForService reports whether the current process must gain
// elevated privileges before it can manage the installed service (PART 5
// "How Binary Determines Escalation Need"). A system-wide service requires
// root/administrator; a user service, or no service at all, requires nothing.
func needsEscalationForService() bool {
	if runtime.GOOS == "windows" {
		return !isElevated() && isWindowsServiceInstalled()
	}
	for _, p := range systemServicePaths() {
		if fileExists(p) {
			return !isElevated()
		}
	}
	return false
}

// elevateServiceOp escalates privileges for a service lifecycle operation when
// a system-wide service is installed and the caller is unprivileged. A true
// first return value means the operation was fully handled (re-executed under
// the escalation tool) or refused, and the caller must return err immediately.
//
// PART 23: never prompt when escalation is impossible — report why instead.
func elevateServiceOp() (bool, error) {
	if !needsEscalationForService() {
		return false, nil
	}
	if canEscalate() {
		return true, execElevated()
	}
	return true, fmt.Errorf("managing the system-wide %s service requires administrator privileges; you do not have sudo/admin access — contact your system administrator", appName)
}

// userServiceAction performs a lifecycle action against the per-user service
// when no system service is being managed and a user unit exists. A true first
// return value means the action was handled by the user service.
func userServiceAction(action string) (bool, error) {
	if isElevated() {
		return false, nil
	}
	unitPath, supported := userServicePath()
	if !supported || !fileExists(unitPath) {
		return false, nil
	}

	if runtime.GOOS == "darwin" {
		switch action {
		case "start":
			return true, exec.Command("launchctl", "load", "-w", unitPath).Run()
		case "stop", "disable":
			return true, exec.Command("launchctl", "unload", "-w", unitPath).Run()
		default:
			exec.Command("launchctl", "unload", "-w", unitPath).Run()
			return true, exec.Command("launchctl", "load", "-w", unitPath).Run()
		}
	}

	if action == "disable" {
		return true, exec.Command("systemctl", "--user", "disable", "--now", appName).Run()
	}
	return true, exec.Command("systemctl", "--user", action, appName).Run()
}

// lifecycleGuard applies the PART 5 escalation matrix to a service lifecycle
// action (start/stop/restart/reload/disable). A true first return value means
// the action was already fully handled — by re-executing elevated, by the
// per-user service, or by refusing because escalation is impossible — and the
// caller must return err without touching the system service.
func lifecycleGuard(action string) (bool, error) {
	if handled, err := elevateServiceOp(); handled {
		return true, err
	}
	return userServiceAction(action)
}

// Install installs the service for the detected service manager, then enables
// and starts it (PART 23: --install installs, enables, and starts).
func Install() error {
	if !isPrivileged() {
		if canEscalate() {
			return execElevated()
		}
		// PART 23 Service Installation Logic: when a system service cannot be
		// installed, fall back to a user service (systemd --user, launchd agent).
		return installUserService()
	}

	serviceType := DetectServiceManager()

	var err error
	switch serviceType {
	case ServiceSystemd:
		err = installSystemd()
	case ServiceOpenRC:
		err = installOpenRC()
	case ServiceSysV:
		err = installSysV()
	case ServiceRunit:
		err = installRunit()
	case ServiceLaunchd:
		err = installLaunchd()
	case ServiceWindows:
		err = installWindows()
	case ServiceBSDRC:
		err = installBSDRC()
	default:
		return fmt.Errorf("unsupported service manager")
	}
	if err != nil {
		return err
	}

	// PART 23: --install also starts the service after installing and enabling.
	if startErr := Start(); startErr != nil {
		fmt.Printf("Service installed but failed to start automatically: %v\n", startErr)
		return nil
	}
	fmt.Printf("%sService started.\n", ok())
	return nil
}

// userServicePath returns the path of the per-user service definition for the
// current platform, plus whether a user service is supported at all.
func userServicePath() (string, bool) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", false
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "LaunchAgents", launchdLabel+".plist"), true
	case "linux":
		if _, err := exec.LookPath("systemctl"); err != nil {
			return "", false
		}
		return filepath.Join(home, ".config", "systemd", "user", appName+".service"), true
	default:
		return "", false
	}
}

// installUserService installs, enables, and starts a per-user service when the
// caller cannot obtain root (PART 23 Service Installation Logic, branch "NO").
// The binary is run in place — nothing is copied into a system directory.
func installUserService() error {
	unitPath, supported := userServicePath()
	if !supported {
		return fmt.Errorf("installing a service requires root on this platform; re-run with sudo: sudo %s --service --install", appName)
	}

	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to resolve binary path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(unitPath), 0755); err != nil {
		return fmt.Errorf("failed to create user service directory: %w", err)
	}

	if runtime.GOOS == "darwin" {
		plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>%s</string>
    <key>ProgramArguments</key>
    <array>
        <string>%s</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
</dict>
</plist>
`, launchdLabel, exePath)
		if err := os.WriteFile(unitPath, []byte(plist), 0644); err != nil {
			return fmt.Errorf("failed to write user agent: %w", err)
		}
		if err := exec.Command("launchctl", "load", "-w", unitPath).Run(); err != nil {
			return fmt.Errorf("failed to load user agent: %w", err)
		}
		fmt.Printf("%sUser agent installed at: %s\n", ok(), unitPath)
		return nil
	}

	unit := fmt.Sprintf(`[Unit]
Description=%s service
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=%s
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
`, appName, exePath)
	if err := os.WriteFile(unitPath, []byte(unit), 0644); err != nil {
		return fmt.Errorf("failed to write user unit: %w", err)
	}
	if err := exec.Command("systemctl", "--user", "daemon-reload").Run(); err != nil {
		return fmt.Errorf("failed to reload user systemd: %w", err)
	}
	if err := exec.Command("systemctl", "--user", "enable", "--now", appName).Run(); err != nil {
		return fmt.Errorf("failed to enable user service: %w", err)
	}
	fmt.Printf("%sUser service installed at: %s\n", ok(), unitPath)
	return nil
}

// uninstallUserService removes a service previously installed by
// installUserService. It never touches config, data, or system directories.
func uninstallUserService() error {
	unitPath, supported := userServicePath()
	if !supported {
		return fmt.Errorf("uninstalling a system service requires root; re-run with sudo: sudo %s --service --uninstall", appName)
	}
	if _, err := os.Stat(unitPath); err != nil {
		return fmt.Errorf("uninstalling a system service requires root; re-run with sudo: sudo %s --service --uninstall", appName)
	}

	if runtime.GOOS == "darwin" {
		exec.Command("launchctl", "unload", "-w", unitPath).Run()
	} else {
		exec.Command("systemctl", "--user", "disable", "--now", appName).Run()
	}
	if err := os.Remove(unitPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove user service: %w", err)
	}
	if runtime.GOOS != "darwin" {
		exec.Command("systemctl", "--user", "daemon-reload").Run()
	}
	fmt.Printf("%sUser service uninstalled\n", ok())
	return nil
}

// Uninstall stops, disables, and removes the service, then deletes all data,
// configs, and the service user (PART 23 Service Uninstall Logic). A
// confirmation prompt guards the destructive step.
func Uninstall() error {
	if !isPrivileged() {
		if canEscalate() {
			return execElevated()
		}
		// A user-scope service can still be removed without escalation.
		return uninstallUserService()
	}

	if !confirmDestructive() {
		fmt.Println("Uninstall cancelled.")
		return nil
	}

	serviceType := DetectServiceManager()

	var err error
	switch serviceType {
	case ServiceSystemd:
		err = uninstallSystemd()
	case ServiceOpenRC:
		err = uninstallOpenRC()
	case ServiceSysV:
		err = uninstallSysV()
	case ServiceRunit:
		err = uninstallRunit()
	case ServiceLaunchd:
		err = uninstallLaunchd()
	case ServiceWindows:
		err = uninstallWindows()
	case ServiceBSDRC:
		err = uninstallBSDRC()
	default:
		return fmt.Errorf("unsupported service manager")
	}
	if err != nil {
		return err
	}

	// PART 23: remove all data, configs, cache, logs, and the service user.
	purgeData()

	fmt.Printf("Service uninstalled. Delete binary manually: rm %s\n", GetBinaryPath())
	return nil
}

// confirmDestructive prompts the operator before deleting all data and the
// service user. Returns true only on an explicit "y"/"yes" answer.
func confirmDestructive() bool {
	fmt.Print("This will delete ALL data, configs, and the system user. Continue? [y/N]: ")
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		return false
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes"
}

// purgeDirs returns the platform-correct directories removed during uninstall
// per PART 23: config, data, cache, log, and backup directories. Paths come
// from the paths package so BSD (/var/db, /usr/local/etc) and macOS layouts
// are honored instead of hardcoded Linux locations.
func purgeDirs() []string {
	return []string{
		path.GetConfigDir(appName),
		path.GetDataDir(appName),
		path.GetCacheDir(appName),
		path.GetLogsDir(appName),
		path.GetBackupDir(appName),
	}
}

// purgeData removes the application config, data, cache, log, and backup
// directories, the PID file, and the dedicated service user/group (PART 23
// Service Uninstall Logic step 4-5). Removal failures are reported but do
// not abort the remaining cleanup.
func purgeData() {
	if runtime.GOOS == "windows" {
		base := fmt.Sprintf(`C:\ProgramData\%s\%s`, orgName, appName)
		if rmErr := os.RemoveAll(base); rmErr != nil && !os.IsNotExist(rmErr) {
			fmt.Printf("Warning: could not remove %s: %v\n", base, rmErr)
		}
		return
	}

	for _, dir := range purgeDirs() {
		if rmErr := os.RemoveAll(dir); rmErr != nil && !os.IsNotExist(rmErr) {
			fmt.Printf("Warning: could not remove %s: %v\n", dir, rmErr)
		}
	}

	// PART 23: remove the PID file left behind by a previous run.
	pidFile := path.GetPIDFile(appName)
	if rmErr := os.Remove(pidFile); rmErr != nil && !os.IsNotExist(rmErr) {
		fmt.Printf("Warning: could not remove %s: %v\n", pidFile, rmErr)
	}

	// Remove the dedicated service user and group created during install.
	if runtime.GOOS == "darwin" {
		exec.Command("dscl", ".", "-delete", "/Users/"+serviceUser).Run()
		exec.Command("dscl", ".", "-delete", "/Groups/"+serviceUser).Run()
		return
	}
	exec.Command("userdel", serviceUser).Run()
	exec.Command("groupdel", serviceUser).Run()
}

// GetBinaryPath returns the path where the binary should be installed
func GetBinaryPath() string {
	switch runtime.GOOS {
	case "windows":
		return fmt.Sprintf(`C:\Program Files\%s\%s\%s.exe`, orgName, appName, appName)
	default:
		return fmt.Sprintf("/usr/local/bin/%s", appName)
	}
}

func ensureSystemdWritablePaths(paths []string) error {
	for _, dir := range paths {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}
	return nil
}

// installSystemd creates the systemd service file (PART 24 compliant).
// The binary starts as root, binds ports, then drops privileges to the service
// user itself — no User= directive needed in the unit file.
func installSystemd() error {
	binaryPath := GetBinaryPath()
	writablePaths := []string{
		fmt.Sprintf("/etc/%s/%s", orgName, appName),
		fmt.Sprintf("/var/lib/%s/%s", orgName, appName),
		fmt.Sprintf("/var/cache/%s/%s", orgName, appName),
		fmt.Sprintf("/var/log/%s/%s", orgName, appName),
	}
	if err := ensureSystemdWritablePaths(writablePaths); err != nil {
		return fmt.Errorf("failed to prepare systemd writable paths: %w", err)
	}

	serviceContent := fmt.Sprintf(`[Unit]
Description=%s service
Documentation=https://apimgr.github.io/pastebin
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=%s
Restart=on-failure
RestartSec=5
StandardOutput=journal
StandardError=journal

# Security hardening (binary drops privileges after port binding)
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
RuntimeDirectory=%s
ReadWritePaths=/etc
ReadWritePaths=/etc/%s/%s
ReadWritePaths=/var/lib/%s/%s
ReadWritePaths=/var/cache/%s/%s
ReadWritePaths=/var/log/%s/%s

[Install]
WantedBy=multi-user.target
`, appName, binaryPath, orgName, orgName, appName, orgName, appName, orgName, appName, orgName, appName)

	servicePath := fmt.Sprintf("/etc/systemd/system/%s.service", appName)

	// systemd requires ReadWritePaths to exist before it creates the restricted
	// mount namespace. /etc is writable only during root startup so the binary
	// can create its service account; the process drops privileges immediately
	// afterward (PART 23, Server Startup Sequence step 8).

	// Write service file
	if err := os.WriteFile(servicePath, []byte(serviceContent), 0644); err != nil {
		return fmt.Errorf("failed to write service file: %w", err)
	}

	// Copy binary if not already in place
	if exePath, err := os.Executable(); err == nil && exePath != binaryPath {
		if err := copyBinary(exePath, binaryPath); err != nil {
			return fmt.Errorf("failed to copy binary: %w", err)
		}
	}

	// Reload systemd
	if err := exec.Command("systemctl", "daemon-reload").Run(); err != nil {
		return fmt.Errorf("failed to reload systemd: %w", err)
	}

	// Enable service
	if err := exec.Command("systemctl", "enable", appName).Run(); err != nil {
		return fmt.Errorf("failed to enable service: %w", err)
	}

	fmt.Printf("%sService installed at: %s\n", ok(), servicePath)
	fmt.Printf("%sBinary installed at: %s\n", ok(), binaryPath)
	fmt.Println()
	fmt.Println("To start the service:")
	fmt.Printf("  sudo systemctl start %s\n", appName)
	fmt.Println()
	fmt.Println("To check status:")
	fmt.Printf("  sudo systemctl status %s\n", appName)

	return nil
}

// uninstallSystemd removes systemd service
func uninstallSystemd() error {
	servicePath := fmt.Sprintf("/etc/systemd/system/%s.service", appName)

	// Stop service if running
	exec.Command("systemctl", "stop", appName).Run()

	// Disable service
	exec.Command("systemctl", "disable", appName).Run()

	// Remove service file
	if err := os.Remove(servicePath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove service file: %w", err)
	}

	// Reload systemd
	exec.Command("systemctl", "daemon-reload").Run()

	fmt.Printf("%sService uninstalled: %s\n", ok(), servicePath)
	return nil
}

// installOpenRC creates an OpenRC service file for Alpine, Gentoo, and Devuan.
func installOpenRC() error {
	binaryPath := GetBinaryPath()
	rcPath := fmt.Sprintf("/etc/init.d/%s", appName)

	rcContent := fmt.Sprintf(`#!/sbin/openrc-run

name="%s"
description="%s service"
command="%s"
command_args=""
command_user="%s:%s"
pidfile="/var/run/%s/%s.pid"
command_background=true
output_log="/var/log/%s/%s/server.log"
error_log="/var/log/%s/%s/error.log"

depend() {
	need net
	after firewall
	use dns logger
}

start_pre() {
	checkpath -d -m 0755 -o %s:%s /var/run/%s
	checkpath -d -m 0755 -o %s:%s /var/log/%s/%s
}
`, appName, appName, binaryPath, serviceUser, serviceUser, orgName, appName, orgName, appName, orgName, appName,
		serviceUser, serviceUser, orgName, serviceUser, serviceUser, orgName, appName)

	if err := os.WriteFile(rcPath, []byte(rcContent), 0755); err != nil {
		return fmt.Errorf("failed to write OpenRC init script: %w", err)
	}

	// Copy binary
	if exePath, err := os.Executable(); err == nil && exePath != binaryPath {
		if err := copyBinary(exePath, binaryPath); err != nil {
			return fmt.Errorf("failed to copy binary: %w", err)
		}
	}

	// Enable on default runlevel
	if err := exec.Command("rc-update", "add", appName, "default").Run(); err != nil {
		return fmt.Errorf("failed to enable OpenRC service: %w", err)
	}

	fmt.Printf("%sOpenRC service installed at: %s\n", ok(), rcPath)
	return nil
}

// uninstallOpenRC removes an OpenRC service file.
func uninstallOpenRC() error {
	rcPath := fmt.Sprintf("/etc/init.d/%s", appName)

	exec.Command("rc-service", appName, "stop").Run()
	exec.Command("rc-update", "del", appName).Run()

	if err := os.Remove(rcPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove OpenRC init script: %w", err)
	}

	fmt.Printf("%sOpenRC service uninstalled\n", ok())
	return nil
}

// installSysV creates a SysVinit init.d script (Debian/Ubuntu update-rc.d or
// RHEL/CentOS chkconfig).
func installSysV() error {
	binaryPath := GetBinaryPath()
	initPath := fmt.Sprintf("/etc/init.d/%s", appName)

	initContent := fmt.Sprintf(`#!/bin/sh
### BEGIN INIT INFO
# Provides:          %s
# Required-Start:    $network $remote_fs $syslog
# Required-Stop:     $network $remote_fs $syslog
# Default-Start:     2 3 4 5
# Default-Stop:      0 1 6
# Short-Description: %s service
# Description:       %s service daemon
### END INIT INFO

NAME=%s
DAEMON=%s
DAEMON_USER=%s
PIDFILE=/var/run/%s/%s.pid
LOGFILE=/var/log/%s/%s/server.log

case "$1" in
    start)
        echo "Starting $NAME..."
        mkdir -p $(dirname $PIDFILE) $(dirname $LOGFILE)
        chown -R $DAEMON_USER:$DAEMON_USER $(dirname $PIDFILE) $(dirname $LOGFILE)
        start-stop-daemon --start --quiet --background --make-pidfile \
            --pidfile $PIDFILE --chuid $DAEMON_USER --exec $DAEMON \
            --no-close >> $LOGFILE 2>&1
        ;;
    stop)
        echo "Stopping $NAME..."
        start-stop-daemon --stop --quiet --pidfile $PIDFILE --retry 30
        rm -f $PIDFILE
        ;;
    restart)
        $0 stop
        sleep 1
        $0 start
        ;;
    status)
        if [ -f $PIDFILE ] && kill -0 $(cat $PIDFILE) 2>/dev/null; then
            echo "$NAME is running (pid $(cat $PIDFILE))"
            exit 0
        else
            echo "$NAME is stopped"
            exit 3
        fi
        ;;
    *)
        echo "Usage: $0 {start|stop|restart|status}"
        exit 1
        ;;
esac
exit 0
`, appName, appName, appName, appName, binaryPath, serviceUser, orgName, appName, orgName, appName)

	if err := os.WriteFile(initPath, []byte(initContent), 0755); err != nil {
		return fmt.Errorf("failed to write SysV init script: %w", err)
	}

	// Copy binary
	if exePath, err := os.Executable(); err == nil && exePath != binaryPath {
		if err := copyBinary(exePath, binaryPath); err != nil {
			return fmt.Errorf("failed to copy binary: %w", err)
		}
	}

	// Enable on boot
	if _, err := exec.LookPath("update-rc.d"); err == nil {
		exec.Command("update-rc.d", appName, "defaults").Run()
	} else if _, err := exec.LookPath("chkconfig"); err == nil {
		exec.Command("chkconfig", "--add", appName).Run()
		exec.Command("chkconfig", appName, "on").Run()
	}

	fmt.Printf("%sSysV init script installed at: %s\n", ok(), initPath)
	return nil
}

// uninstallSysV removes a SysVinit init.d script.
func uninstallSysV() error {
	initPath := fmt.Sprintf("/etc/init.d/%s", appName)

	exec.Command(initPath, "stop").Run()

	if _, err := exec.LookPath("update-rc.d"); err == nil {
		exec.Command("update-rc.d", "-f", appName, "remove").Run()
	} else if _, err := exec.LookPath("chkconfig"); err == nil {
		exec.Command("chkconfig", "--del", appName).Run()
	}

	if err := os.Remove(initPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove SysV init script: %w", err)
	}

	fmt.Printf("%sSysV init script uninstalled\n", ok())
	return nil
}

// runitLinkPath returns the path of the symlink that enables the runit service.
// Distributions supervise either /var/service or /service; the first existing
// runsvdir wins so install, disable, uninstall, and status all agree.
func runitLinkPath() string {
	for _, dir := range []string{"/var/service", "/service", "/etc/runit/runsvdir/current"} {
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			return filepath.Join(dir, appName)
		}
	}
	return filepath.Join("/var/service", appName)
}

// installRunit creates runit service
func installRunit() error {
	svDir := fmt.Sprintf("/etc/sv/%s", appName)
	binaryPath := GetBinaryPath()

	// Create service directory
	if err := os.MkdirAll(svDir, 0755); err != nil {
		return fmt.Errorf("failed to create service directory: %w", err)
	}

	runScript := fmt.Sprintf(`#!/bin/sh
exec %s 2>&1
`, binaryPath)

	runPath := filepath.Join(svDir, "run")
	if err := os.WriteFile(runPath, []byte(runScript), 0755); err != nil {
		return fmt.Errorf("failed to write run script: %w", err)
	}

	// Create log service directory
	logDir := filepath.Join(svDir, "log")
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return fmt.Errorf("failed to create log directory: %w", err)
	}

	// PART 24: svlogd writes into the standard log directory, not a relative path.
	svlogdDir := fmt.Sprintf("/var/log/%s/%s", orgName, appName)
	if err := os.MkdirAll(svlogdDir, 0755); err != nil {
		return fmt.Errorf("failed to create log destination: %w", err)
	}

	logRunScript := fmt.Sprintf(`#!/bin/sh
exec svlogd -tt %s
`, svlogdDir)
	logRunPath := filepath.Join(logDir, "run")
	if err := os.WriteFile(logRunPath, []byte(logRunScript), 0755); err != nil {
		return fmt.Errorf("failed to write log run script: %w", err)
	}

	// Copy binary if not already in place
	if exePath, err := os.Executable(); err == nil && exePath != binaryPath {
		if err := copyBinary(exePath, binaryPath); err != nil {
			return fmt.Errorf("failed to copy binary: %w", err)
		}
	}

	// Link into the active runsvdir so the service starts on boot
	os.Symlink(svDir, runitLinkPath())

	fmt.Printf("%sRunit service installed at: %s\n", ok(), svDir)
	return nil
}

// uninstallRunit removes runit service
func uninstallRunit() error {
	svDir := fmt.Sprintf("/etc/sv/%s", appName)
	linkPath := runitLinkPath()

	// Stop service
	exec.Command("sv", "stop", appName).Run()

	// Remove link
	os.Remove(linkPath)

	// Remove service directory
	os.RemoveAll(svDir)

	fmt.Printf("%sRunit service uninstalled\n", ok())
	return nil
}

// findAvailableMacOSSystemID scans the macOS-safe system range from 399 down
// to 200 and returns the first ID usable as both UID and GID: not reserved,
// not in the user database, and not in the group database (PART 23 macOS
// UID/GID ranges — same reservedIDs list as Linux applies).
func findAvailableMacOSSystemID() (int, error) {
	for id := 399; id >= 200; id-- {
		if reservedIDs[id] {
			continue
		}
		// Check if UID is already in use.
		if _, err := user.LookupId(strconv.Itoa(id)); err == nil {
			continue
		}
		// Check if GID is already in use.
		if _, err := user.LookupGroupId(strconv.Itoa(id)); err == nil {
			continue
		}
		return id, nil
	}
	return 0, fmt.Errorf("no available UID/GID in macOS safe range 200-399")
}

// createMacOSServiceUser creates the hidden macOS service account and matching
// group via dscl (PART 23 macOS sequence): group first, then the user with
// matching UID/GID, no shell, starred password, and IsHidden=1 so the account
// never appears on the login window.
func createMacOSServiceUser(name string, id int, homeDir string) error {
	commands := [][]string{
		// Create group
		{"dscl", ".", "-create", "/Groups/" + name},
		{"dscl", ".", "-create", "/Groups/" + name, "PrimaryGroupID", strconv.Itoa(id)},
		{"dscl", ".", "-create", "/Groups/" + name, "Password", "*"},
		// Create user
		{"dscl", ".", "-create", "/Users/" + name},
		{"dscl", ".", "-create", "/Users/" + name, "UniqueID", strconv.Itoa(id)},
		{"dscl", ".", "-create", "/Users/" + name, "PrimaryGroupID", strconv.Itoa(id)},
		{"dscl", ".", "-create", "/Users/" + name, "UserShell", "/usr/bin/false"},
		{"dscl", ".", "-create", "/Users/" + name, "RealName", name + " service account"},
		{"dscl", ".", "-create", "/Users/" + name, "NFSHomeDirectory", homeDir},
		{"dscl", ".", "-create", "/Users/" + name, "Password", "*"},
		// Hide user from login window
		{"dscl", ".", "-create", "/Users/" + name, "IsHidden", "1"},
	}

	for _, cmd := range commands {
		if err := exec.Command(cmd[0], cmd[1:]...).Run(); err != nil {
			return fmt.Errorf("failed to run %v: %w", cmd, err)
		}
	}
	return nil
}

// installLaunchd creates macOS launchd plist
func installLaunchd() error {
	binaryPath := GetBinaryPath()
	plistPath := fmt.Sprintf("/Library/LaunchDaemons/%s.plist", launchdLabel)

	// PART 23: --install only writes, enables, and starts the service. The macOS
	// service account and runtime directories are provisioned by the binary at
	// normal startup (Server Startup Sequence step 8), never here.

	plistContent := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>%s</string>
    <key>ProgramArguments</key>
    <array>
        <string>%s</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>/var/log/%s/%s/stdout.log</string>
    <key>StandardErrorPath</key>
    <string>/var/log/%s/%s/stderr.log</string>
</dict>
</plist>
`, launchdLabel, binaryPath, orgName, appName, orgName, appName)

	// Write plist file
	if err := os.WriteFile(plistPath, []byte(plistContent), 0644); err != nil {
		return fmt.Errorf("failed to write plist file: %w", err)
	}

	// Copy binary
	if exePath, err := os.Executable(); err == nil && exePath != binaryPath {
		if err := copyBinary(exePath, binaryPath); err != nil {
			return fmt.Errorf("failed to copy binary: %w", err)
		}
	}

	fmt.Printf("%sLaunchDaemon installed at: %s\n", ok(), plistPath)
	fmt.Println()
	fmt.Println("To load the service:")
	fmt.Printf("  sudo launchctl load %s\n", plistPath)

	return nil
}

// uninstallLaunchd removes macOS launchd plist
func uninstallLaunchd() error {
	plistPath := fmt.Sprintf("/Library/LaunchDaemons/%s.plist", launchdLabel)

	// Unload if running
	exec.Command("launchctl", "unload", plistPath).Run()

	// Remove plist
	if err := os.Remove(plistPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove plist file: %w", err)
	}

	fmt.Printf("%sLaunchDaemon uninstalled\n", ok())
	return nil
}

// installBSDRC creates BSD rc.d script
func installBSDRC() error {
	binaryPath := GetBinaryPath()
	rcPath := fmt.Sprintf("/usr/local/etc/rc.d/%s", appName)

	rcContent := fmt.Sprintf(`#!/bin/sh

# PROVIDE: %s
# REQUIRE: NETWORKING
# KEYWORD: shutdown

. /etc/rc.subr

name="%s"
rcvar="%s_enable"
command="%s"
pidfile="/var/run/%s.pid"

load_rc_config $name
: ${%s_enable:="NO"}

run_rc_command "$1"
`, appName, appName, appName, binaryPath, appName, appName)

	if err := os.WriteFile(rcPath, []byte(rcContent), 0755); err != nil {
		return fmt.Errorf("failed to write rc.d script: %w", err)
	}

	// Copy binary
	if exePath, err := os.Executable(); err == nil && exePath != binaryPath {
		if err := copyBinary(exePath, binaryPath); err != nil {
			return fmt.Errorf("failed to copy binary: %w", err)
		}
	}

	fmt.Printf("%sBSD rc.d script installed at: %s\n", ok(), rcPath)
	fmt.Println()
	fmt.Printf("Add '%s_enable=\"YES\"' to /etc/rc.conf\n", appName)
	fmt.Println()
	fmt.Println("To start the service:")
	fmt.Printf("  service %s start\n", appName)

	return nil
}

// uninstallBSDRC removes BSD rc.d script
func uninstallBSDRC() error {
	rcPath := fmt.Sprintf("/usr/local/etc/rc.d/%s", appName)

	// Stop service
	exec.Command("service", appName, "stop").Run()

	// Remove script
	if err := os.Remove(rcPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove rc.d script: %w", err)
	}

	fmt.Printf("%sBSD rc.d script uninstalled\n", ok())
	return nil
}

// copyBinary copies the binary to the destination
func copyBinary(src, dst string) error {
	// Create destination directory if needed
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}

	// Read source
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}

	// Write to destination
	if err := os.WriteFile(dst, data, 0755); err != nil {
		return err
	}

	return nil
}

// Start starts the service
func Start() error {
	if handled, err := lifecycleGuard("start"); handled {
		return err
	}
	serviceType := DetectServiceManager()

	switch serviceType {
	case ServiceSystemd:
		return exec.Command("systemctl", "start", appName).Run()
	case ServiceOpenRC:
		return exec.Command("rc-service", appName, "start").Run()
	case ServiceSysV:
		return exec.Command("/etc/init.d/"+appName, "start").Run()
	case ServiceRunit:
		return exec.Command("sv", "start", appName).Run()
	case ServiceLaunchd:
		plistPath := fmt.Sprintf("/Library/LaunchDaemons/%s.plist", launchdLabel)
		return exec.Command("launchctl", "load", plistPath).Run()
	case ServiceWindows:
		return exec.Command("sc.exe", "start", appName).Run()
	case ServiceBSDRC:
		return exec.Command("service", appName, "start").Run()
	default:
		return fmt.Errorf("unsupported service manager")
	}
}

// Stop stops the service
func Stop() error {
	if handled, err := lifecycleGuard("stop"); handled {
		return err
	}
	serviceType := DetectServiceManager()

	switch serviceType {
	case ServiceSystemd:
		return exec.Command("systemctl", "stop", appName).Run()
	case ServiceOpenRC:
		return exec.Command("rc-service", appName, "stop").Run()
	case ServiceSysV:
		return exec.Command("/etc/init.d/"+appName, "stop").Run()
	case ServiceRunit:
		return exec.Command("sv", "stop", appName).Run()
	case ServiceLaunchd:
		plistPath := fmt.Sprintf("/Library/LaunchDaemons/%s.plist", launchdLabel)
		return exec.Command("launchctl", "unload", plistPath).Run()
	case ServiceWindows:
		return exec.Command("sc.exe", "stop", appName).Run()
	case ServiceBSDRC:
		return exec.Command("service", appName, "stop").Run()
	default:
		return fmt.Errorf("unsupported service manager")
	}
}

// Restart restarts the service
func Restart() error {
	if handled, err := lifecycleGuard("restart"); handled {
		return err
	}
	serviceType := DetectServiceManager()

	switch serviceType {
	case ServiceSystemd:
		return exec.Command("systemctl", "restart", appName).Run()
	case ServiceOpenRC:
		return exec.Command("rc-service", appName, "restart").Run()
	case ServiceSysV:
		return exec.Command("/etc/init.d/"+appName, "restart").Run()
	case ServiceRunit:
		return exec.Command("sv", "restart", appName).Run()
	case ServiceLaunchd:
		Stop()
		return Start()
	case ServiceWindows:
		exec.Command("sc.exe", "stop", appName).Run()
		return exec.Command("sc.exe", "start", appName).Run()
	case ServiceBSDRC:
		return exec.Command("service", appName, "restart").Run()
	default:
		return fmt.Errorf("unsupported service manager")
	}
}

// Reload sends reload signal to the service
func Reload() error {
	if handled, err := lifecycleGuard("reload"); handled {
		return err
	}
	serviceType := DetectServiceManager()

	switch serviceType {
	case ServiceSystemd:
		return exec.Command("systemctl", "reload", appName).Run()
	case ServiceOpenRC:
		return exec.Command("rc-service", appName, "reload").Run()
	case ServiceSysV:
		return exec.Command("/etc/init.d/"+appName, "reload").Run()
	case ServiceRunit:
		return exec.Command("sv", "hup", appName).Run()
	default:
		// For others, restart is the fallback
		return Restart()
	}
}

// Disable stops the service and prevents it from starting on boot, but does
// not remove the service files (unlike Uninstall).
func Disable() error {
	if handled, err := lifecycleGuard("disable"); handled {
		return err
	}
	serviceType := DetectServiceManager()

	switch serviceType {
	case ServiceSystemd:
		exec.Command("systemctl", "stop", appName).Run()
		return exec.Command("systemctl", "disable", appName).Run()
	case ServiceOpenRC:
		exec.Command("rc-service", appName, "stop").Run()
		return exec.Command("rc-update", "del", appName).Run()
	case ServiceSysV:
		exec.Command("/etc/init.d/"+appName, "stop").Run()
		if _, err := exec.LookPath("update-rc.d"); err == nil {
			return exec.Command("update-rc.d", appName, "disable").Run()
		}
		return exec.Command("chkconfig", appName, "off").Run()
	case ServiceRunit:
		enabledDir := runitLinkPath()
		exec.Command("sv", "stop", appName).Run()
		// Disable only removes the symlink from the active service directory;
		// the service definition under /etc/sv is left in place.
		os.Remove(enabledDir)
		return nil
	case ServiceLaunchd:
		plistPath := fmt.Sprintf("/Library/LaunchDaemons/%s.plist", launchdLabel)
		exec.Command("launchctl", "unload", plistPath).Run()
		return exec.Command("launchctl", "disable", fmt.Sprintf("system/%s", launchdLabel)).Run()
	case ServiceWindows:
		exec.Command("sc.exe", "stop", appName).Run()
		return exec.Command("sc.exe", "config", appName, "start=", "disabled").Run()
	case ServiceBSDRC:
		exec.Command("service", appName, "stop").Run()
		return exec.Command("sysrc", fmt.Sprintf("%s_enable=NO", appName)).Run()
	default:
		return fmt.Errorf("unsupported service manager")
	}
}

// Status reports the current installed/running/enabled state of the service,
// plus its PID if running (AI.md 30149-30169, PART 8/23).
type Status struct {
	Installed bool
	Running   bool
	Enabled   bool
	PID       int
}

// GetStatus probes the detected service manager (and, as a fallback, the PID
// file) to determine whether the service is installed, running, and enabled
// to auto-start.
func GetStatus() Status {
	var st Status

	switch DetectServiceManager() {
	case ServiceSystemd:
		servicePath := fmt.Sprintf("/etc/systemd/system/%s.service", appName)
		if _, err := os.Stat(servicePath); err == nil {
			st.Installed = true
		}
		if out, err := exec.Command("systemctl", "is-active", appName).Output(); err == nil {
			st.Running = strings.TrimSpace(string(out)) == "active"
		}
		if out, err := exec.Command("systemctl", "is-enabled", appName).Output(); err == nil {
			st.Enabled = strings.TrimSpace(string(out)) == "enabled"
		}
	case ServiceOpenRC:
		svcPath := "/etc/init.d/" + appName
		if _, err := os.Stat(svcPath); err == nil {
			st.Installed = true
		}
		st.Running = exec.Command("rc-service", appName, "status").Run() == nil
		if out, err := exec.Command("rc-update", "show").Output(); err == nil {
			st.Enabled = strings.Contains(string(out), appName)
		}
	case ServiceSysV:
		svcPath := "/etc/init.d/" + appName
		if _, err := os.Stat(svcPath); err == nil {
			st.Installed = true
			st.Running = exec.Command(svcPath, "status").Run() == nil
		}
		if out, err := exec.Command("chkconfig", "--list", appName).Output(); err == nil {
			st.Enabled = strings.Contains(string(out), "3:on")
		}
	case ServiceRunit:
		svcPath := "/etc/sv/" + appName
		if _, err := os.Stat(svcPath); err == nil {
			st.Installed = true
		}
		st.Running = exec.Command("sv", "status", appName).Run() == nil
		if _, err := os.Lstat(runitLinkPath()); err == nil {
			st.Enabled = true
		}
	case ServiceLaunchd:
		plistPath := fmt.Sprintf("/Library/LaunchDaemons/%s.plist", launchdLabel)
		if _, err := os.Stat(plistPath); err == nil {
			st.Installed = true
			st.Enabled = true
		}
		st.Running = exec.Command("launchctl", "list", launchdLabel).Run() == nil
	case ServiceWindows:
		if out, err := exec.Command("sc.exe", "query", appName).Output(); err == nil {
			st.Installed = true
			st.Running = strings.Contains(string(out), "RUNNING")
		}
		if out, err := exec.Command("sc.exe", "qc", appName).Output(); err == nil {
			st.Enabled = strings.Contains(string(out), "AUTO_START")
		}
	case ServiceBSDRC:
		svcPath := "/usr/local/etc/rc.d/" + appName
		if _, err := os.Stat(svcPath); err == nil {
			st.Installed = true
			st.Running = exec.Command("service", appName, "status").Run() == nil
		}
		if out, err := exec.Command("sysrc", "-n", appName+"_enable").Output(); err == nil {
			st.Enabled = strings.TrimSpace(string(out)) == "YES"
		}
	}

	// The PID file is authoritative for "running" and supplies the PID,
	// regardless of what the service manager reports.
	if running, foundPID, err := pid.CheckPIDFile(path.GetPIDFile(appName)); err == nil && running {
		st.Running = true
		st.PID = foundPID
	}

	return st
}

// PrintHelp prints service subcommand help to stdout.
func PrintHelp(_ string) {
	fmt.Print(`Service management commands:

start                                 - Start the service
stop                                  - Stop the service
restart                               - Restart the service
reload                                - Reload configuration without restart

--install                              - Install, enable, and start service
--disable                              - Stop and disable service (keeps data)
--uninstall                            - Stop, disable, and remove everything (keeps binary)
`)

	st := GetStatus()

	installedStr := "not installed"
	if st.Installed {
		installedStr = "installed"
	}

	stateStr := "stopped"
	switch {
	case st.Running:
		stateStr = "running"
	case st.Installed && !st.Enabled:
		stateStr = "disabled"
	}

	enabledStr := "disabled"
	if st.Enabled {
		enabledStr = "enabled"
	}

	fmt.Println()
	fmt.Println("Current status:")
	fmt.Printf("  Service:    %s\n", installedStr)
	fmt.Printf("  State:      %s\n", stateStr)
	fmt.Printf("  Auto-start: %s\n", enabledStr)
	if st.Running {
		fmt.Printf("  PID:        %d\n", st.PID)
	}
}
