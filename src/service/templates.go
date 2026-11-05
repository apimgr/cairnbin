package service

import "fmt"

// appTitle is the human-readable application name ({app_name} in PART 24).
const appTitle = "Cairnbin API Server"

// buildSystemdUnit returns the system-wide systemd unit (PART 24). The binary
// starts elevated only when it needs to, binds its ports, then drops to the
// service user itself, so no User= directive is present.
func buildSystemdUnit(binaryPath string) string {
	return fmt.Sprintf(`[Unit]
Description=%s service
Documentation=https://%s.github.io/%s
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
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
ReadWritePaths=/etc/%s/%s
ReadWritePaths=/var/lib/%s/%s
ReadWritePaths=/var/cache/%s/%s
ReadWritePaths=/var/log/%s/%s

[Install]
WantedBy=multi-user.target
`, appName, orgName, appName, binaryPath,
		orgName, appName, orgName, appName, orgName, appName, orgName, appName)
}

// buildOpenRCScript returns the OpenRC init script (PART 24).
func buildOpenRCScript(binaryPath string) string {
	return fmt.Sprintf(`#!/sbin/openrc-run
# Service identity comes from %[1]s so config_dir/data_dir paths stay
# stable across binary renames.

name="%[1]s"
description="%[2]s"
# actual binary (may differ from %[1]s after rename)
command="%[3]s"
command_args=""
command_user="%[4]s:%[4]s"
pidfile="/var/run/%[5]s/%[1]s.pid"
command_background=true
output_log="/var/log/%[5]s/%[1]s/server.log"
error_log="/var/log/%[5]s/%[1]s/error.log"

depend() {
    need net
    after firewall
    use dns logger
}

start_pre() {
    checkpath -d -m 0755 -o %[4]s:%[4]s /var/run/%[5]s
    checkpath -d -m 0755 -o %[4]s:%[4]s /var/log/%[5]s/%[1]s
}
`, appName, appTitle, binaryPath, serviceUser, orgName)
}

// buildSysVScript returns the SysVinit init.d script (PART 24).
func buildSysVScript(binaryPath string) string {
	return fmt.Sprintf(`#!/bin/sh
### BEGIN INIT INFO
# Provides:          %[1]s
# Required-Start:    $network $remote_fs $syslog
# Required-Stop:     $network $remote_fs $syslog
# Default-Start:     2 3 4 5
# Default-Stop:      0 1 6
# Short-Description: %[2]s
# Description:       %[2]s daemon
### END INIT INFO

NAME=%[1]s
DAEMON=%[3]s
DAEMON_USER=%[4]s
PIDFILE=/var/run/%[5]s/%[1]s.pid
LOGFILE=/var/log/%[5]s/%[1]s/server.log

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
`, appName, appTitle, binaryPath, serviceUser, orgName)
}

// buildRunitRunScript returns the runit run script (PART 24).
func buildRunitRunScript(binaryPath string) string {
	return fmt.Sprintf(`#!/bin/sh
exec %s 2>&1
`, binaryPath)
}

// buildRunitLogRunScript returns the runit log/run script (PART 24).
func buildRunitLogRunScript() string {
	return fmt.Sprintf(`#!/bin/sh
exec svlogd -tt /var/log/%s/%s
`, orgName, appName)
}

// buildBSDRCScript returns the FreeBSD rc.d script (PART 24).
func buildBSDRCScript(binaryPath string) string {
	return fmt.Sprintf(`#!/bin/sh

# PROVIDE: %[1]s
# REQUIRE: NETWORKING
# KEYWORD: shutdown

. /etc/rc.subr

name="%[1]s"
rcvar="%[1]s_enable"
command="%[2]s"

load_rc_config $name
run_rc_command "$1"
`, appName, binaryPath)
}

// buildLaunchdPlist returns the macOS LaunchDaemon plist (PART 24).
func buildLaunchdPlist(binaryPath string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
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
}
