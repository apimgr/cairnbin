# Service Rules (PART 23, 24)

⚠️ **These rules are NON-NEGOTIABLE. Violations are bugs.** ⚠️

## CRITICAL - NEVER DO

- NEVER prompt for privilege escalation if the user cannot actually escalate (not in sudoers/wheel/admin) — show an informative error instead
- NEVER skip the "already root/admin" check — binary must check EUID==0 first and skip escalation prompt entirely if true
- NEVER use reserved/well-known UIDs/GIDs (65534, 999, 998, 997, 996, 995, 994, 993, 992, 991, 990, 989, 988, 987, 986, 985, 984, 983, 982, 981, 980, 101-110, 170-179) even if they appear available on the current system
- NEVER pick a UID that doesn't match the GID — UID and GID MUST be the same numeric value
- NEVER search the UID/GID range outside 200-899 (Linux/BSD) or 200-399 (macOS)
- NEVER run `--service --uninstall` without prompting for confirmation first ("This will delete ALL data, configs, and the system user. Continue? [y/N]")
- NEVER use Local System, Administrator, or a logged-in user account for a Windows service — use Virtual Service Account (VSA) by default
- NEVER create a dedicated service user when the project is explicitly approved in IDEA.md to run permanently as root/Administrator — skip user creation in that documented exception case only
- NEVER let `--service --install` do more than install, enable, and start the service — user/group creation, privilege drop, and directory setup happen at NORMAL STARTUP, not at install time
- NEVER let `--service --disable` remove data, config, or the service file — it only stops and disables auto-start

## CRITICAL - ALWAYS DO

- ALWAYS implement the full escalation method order per OS:
  - Linux: root → sudo → su → pkexec → doas
  - macOS: root → sudo → osascript (admin prompt)
  - BSD: root → doas → sudo → su
  - Windows: Administrator token → UAC prompt → runas
- ALWAYS have the binary handle user/group creation, privilege escalation, directory setup, and permissions during normal startup (not during `--service --install`)
- ALWAYS use system user type: no password, no login, shell `/sbin/nologin` (Linux) or `/usr/bin/false` (macOS) or `/usr/sbin/nologin` (BSD)
- ALWAYS set Gecos/comment to `{internal_name} service account`
- ALWAYS find UID/GID starting at the top of the safe range and working down, skipping reserved IDs
- ALWAYS support ALL major service managers per platform: systemd, OpenRC, SysVinit, runit (Linux); launchd (macOS); rc.d (BSD); Windows Service
- ALWAYS drop privileges after binding to a privileged port (Unix), except when the project explicitly requires permanent root (documented in IDEA.md, and the service file/docs must explain why)
- ALWAYS create required directories before creating the user, then create the user, then set ownership
- ALWAYS provide `--service`, `--maintenance`, `--shell`, and `--update` help output matching the documented command surface (start/stop/restart/reload, install/disable/uninstall, backup/restore/update/mode/setup, completions/init, check/yes/branch)

## Key Rules Summary

### System User Requirements

| Requirement | Value |
|-------------|-------|
| Username / Group | `{internal_name}` (same for both) |
| UID/GID | Must match; same numeric value for both |
| UID/GID range (Linux/BSD) | 200-899 |
| UID/GID range (macOS) | 200-399 |
| Shell | `/sbin/nologin` or equivalent (no login) |
| Home | Config dir or data dir |
| Type | System user, no password |

### Service Manager Install Paths

| Init System | Install Path |
|-------------|--------------|
| systemd | `/etc/systemd/system/{internal_name}.service` |
| OpenRC | `/etc/init.d/{internal_name}` |
| SysVinit | `/etc/init.d/{internal_name}` |
| runit | `/etc/sv/{internal_name}/` |
| rc.d (FreeBSD) | `/usr/local/etc/rc.d/{internal_name}` |
| launchd (macOS) | `/Library/LaunchDaemons/{plist_name}.plist` |
| Windows | Service Control Manager, `NT SERVICE\{internal_name}` |

### systemd Unit Conventions

- `Type=simple`, `Restart=on-failure`, `RestartSec=5`
- `StandardOutput=journal`, `StandardError=journal`
- Hardening: `ProtectSystem=strict`, `ProtectHome=yes`, `PrivateTmp=yes`
- `ReadWritePaths=` for config, data, cache, and log dirs
- `WantedBy=multi-user.target`

### Windows Service Account Priority

| Option | Used |
|--------|:----:|
| Virtual Service Account (VSA) | ✅ Default |
| Local Service | Network-less only |
| Network Service | Network services |
| Local System | ❌ Avoid |
| Administrator | ❌ Avoid |
| Logged-in User | ❌ Avoid |

### Run Mode Matrix

| Run Mode | Who Runs Binary | Port Restriction | Privilege Drop |
|----------|-----------------|-------------------|-----------------|
| Service (escalated) | root/admin | Any port | Yes (after binding) |
| User mode ($USER) | Calling user | >1024 only | No |

### Service Install/Uninstall/Disable

| Command | Effect |
|---------|--------|
| `--service --install` | Install, enable, start service (user creation happens at startup, not here) |
| `--service --disable` | Stop, disable auto-start; keeps data/config/user |
| `--service --uninstall` | Stop, disable, remove service file, delete ALL data + user (requires confirmation); binary itself remains |

For complete details, see AI.md PART 23, 24
