# CLI Reference

The `cairnbin-cli` binary is a fully spec-compliant command-line client.

## Global Flags

| Flag | Description |
|------|-------------|
| `--server URL` | Target server URL (default: `https://pste.us`, the embedded official site; or set `$CAIRNBIN_SERVER_PRIMARY`) |
| `--token TOKEN` | Operator/owner API token (or set `$CAIRNBIN_TOKEN`) |
| `--token-file PATH` | Read the API token from a file |
| `--config NAME` | Config profile name or path (default: `cli.yml`; `.yml` checked before `.yaml`) |
| `--json` | Output in JSON format |
| `--output FORMAT` | Select `json`, `yaml`, `table`, `plain`, or `csv`; may follow a command and its positional arguments |
| `--color MODE` | Color output: `auto`, `yes`, or `no` |
| `--lang CODE` | Output language code (default: auto-detect from `LANG`) |
| `--debug` | Enable debug output |
| `--verbose` | Show extra request diagnostics (also configurable with `output.verbose`) |
| `--shell {completions,init,help} [SHELL]` | Emit shell completions or init snippet (auto-detects `$SHELL` if omitted) |
| `--update check\|yes` | Check for or apply CLI updates |
| `-h, --help` | Show help |
| `-v, --version` | Show version |

The CLI reads these environment variables; explicit flags override their matching settings where applicable.

| Variable | Purpose |
|----------|---------|
| `CAIRNBIN_SERVER_PRIMARY` | Default server URL |
| `CAIRNBIN_TOKEN` | API token when no `--token` is supplied |
| `CAIRNBIN_SERVER_API_VERSION` | API version override |
| `CAIRNBIN_SERVER_TIMEOUT` | Request timeout override |
| `CAIRNBIN_SERVER_RETRY` | Number of retries to attempt |
| `CAIRNBIN_SERVER_RETRY_DELAY` | Delay between retries |
| `CAIRNBIN_UPDATE_CHANNEL`, `CAIRNBIN_UPDATE_CHECK_INTERVAL`, `CAIRNBIN_UPDATE_AUTO` | Update channel, check interval, and automatic-check setting |
| `CAIRNBIN_DISPLAY_MODE` | Display mode override: `auto`, `tui`, or `gui` |
| `CAIRNBIN_FORMAT`, `CAIRNBIN_OUTPUT_FORMAT` | Output format (`CAIRNBIN_FORMAT` takes precedence) |
| `CAIRNBIN_OUTPUT_COLOR` | Color mode: `auto`, `yes`, or `no` |
| `CAIRNBIN_OUTPUT_PAGER` | Pager mode or command |
| `CAIRNBIN_OUTPUT_VERBOSE`, `CAIRNBIN_OUTPUT_QUIET` | Verbose diagnostics and quiet output defaults |
| `CAIRNBIN_DEBUG` | Enable debug output |
| `CAIRNBIN_LANG`, `CAIRNBIN_DEFAULTS_LANG` | Output language preference |
| `CAIRNBIN_DEFAULTS_OUTPUT` | Default output format for commands |
| `CAIRNBIN_PUBLIC`, `CAIRNBIN_EXPIRE`, `CAIRNBIN_SYNTAX`, `CAIRNBIN_LIMIT` | Default paste visibility, expiry, syntax, and list limit |
| `CAIRNBIN_TUI_THEME` | TUI theme |
| `CAIRNBIN_TUI_ENABLED`, `CAIRNBIN_TUI_MOUSE`, `CAIRNBIN_TUI_UNICODE` | Enable the TUI and configure mouse and Unicode support |
| `CAIRNBIN_LOGGING_LEVEL`, `CAIRNBIN_LOGGING_FILE`, `CAIRNBIN_LOGGING_MAX_SIZE`, `CAIRNBIN_LOGGING_MAX_FILES` | CLI log level, location, rotation size, and retained-file count |
| `CAIRNBIN_CACHE_ENABLED`, `CAIRNBIN_CACHE_TTL`, `CAIRNBIN_CACHE_MAX_SIZE` | Enable response caching and set its lifetime and size |
| `CLI_CONFIG` | Explicit path to the CLI configuration file |
| `HOME`, `XDG_CONFIG_HOME`, `XDG_DATA_HOME`, `XDG_CACHE_HOME` | Unix home and configuration, data, and cache directory roots |
| `APPDATA`, `LOCALAPPDATA` | Windows configuration, data, cache, and log directory roots |
| `LC_ALL`, `LANG`, `LANGUAGE` | Locale fallback when `--lang` and `defaults.lang` are unset or `auto` |
| `NO_COLOR`, `TERM` | Disable color or indicate a `dumb` terminal |
| `SHELL` | Default shell for `--shell completions` and `--shell init` |
| `TMPDIR` | Temporary directory used for CLI update downloads |
| `PAGER` | Fallback pager command when no configured pager is set |

Response caching and rotating logs can also be configured in `cli.yml` under `cache.*` and `logging.*`.

## First-run connection setup

When no server is configured, an interactive launch opens the setup flow for
the detected display mode: the native GUI on a local desktop, the TUI in a
terminal, or inline prompts when the TUI is disabled. The flow asks for a
server URL and optional API token, tests the connection and token, and offers
to save the connection or set it as the default. The GUI also offers a
separate option to start automatically at login on Linux, macOS, and Windows.
Noninteractive commands use the server selected by `--server`,
`CAIRNBIN_SERVER_PRIMARY`, `cli.yml`, or the build's compiled default site.

## Commands

### create

Create a new paste from stdin or a file.

```bash
cairnbin-cli create [file] [flags]
```

**Flags:**

| Flag | Description |
|------|-------------|
| `--lang LANG` | Chroma language identifier (e.g. `go`, `python`, `text`); default follows `defaults.syntax` |
| `--syntax LANG` | Alias for `--lang` |
| `--expire DURATION`, `--expiry DURATION` | `1h`, `1d`, `1w`, `1m`, `3m`, `6m`, `1y`, `18m`, `2y`, `never`, or seconds; default follows `defaults.expire` |
| `--burn N` | Delete after N views (1–9999; `0` = disabled) |
| `--public public\|unlisted` | Set visibility; default is unlisted and follows `defaults.public` |
| `--unlisted` | Alias for `--public unlisted` |
| `--title TITLE` | Optional paste title |
| `--file PATH` | Read content from this file, even when the value could be treated as literal text |
| `--text TEXT` | Use this literal text as content, even when it names an existing file |
| `--link` | Treat the content or positional argument as an absolute HTTP or HTTPS link paste |
| `--output FORMAT` | Select `json`, `yaml`, `table`, `plain`, or `csv` output |
| `--json` | Alias for `--output json` |

Without `--file` or `--text`, an existing positional file is read as content;
otherwise positional arguments are joined as literal text. With no positional
arguments, content is read from standard input.

**Examples:**

```bash
# Paste from stdin
echo "Hello, World!" | cairnbin-cli create

# Paste from stdin without an explicit command
echo "Hello, World!" | cairnbin-cli

# Paste a file
cairnbin-cli create myfile.go --lang go
cairnbin-cli myfile.go --syntax go

# Burn-after-read with expiry
cairnbin-cli create --burn 1 --expiry 1h secret.txt

# Unlisted paste with title
cairnbin-cli create notes.txt --title "Meeting Notes" --unlisted

# Target a custom server (default: https://pste.us)
cairnbin-cli --server https://your-server.example.com create myfile.txt
```

**Output:**

```
URL:          https://pste.us/abc12345
Delete Token: own_AbCdEfGh12345678ijklMNOP87654321
```

The owner token remains viewable, masked by default, at `/server/preferences/tokens`. CLI delete requests send it as `Authorization: Bearer {token}`.

### update

Check whether a newer platform-specific CLI binary is available:

```bash
cairnbin-cli update check
```

Apply the update after checking its published SHA-256 checksum:

```bash
cairnbin-cli update yes
```

`cairnbin-cli --update check` and `cairnbin-cli --update yes` are equivalent.
The update check uses the selected server's `/api/autodiscover` metadata and
`/cli/binaries/` download route. It reports when no binary is published for
the current OS and architecture.

### version

Print the CLI version/build information and the selected server URL:

```bash
cairnbin-cli version
```

The global `--version` (or `-v`) flag prints the same information.

### shell integration

Print shell completion scripts or an initialization command for the current
session. Supported shells are Bash, Zsh, Fish, POSIX `sh`, Dash, Ksh,
PowerShell, and PowerShell Core (`pwsh`). When omitted, the shell is detected
from `SHELL`.

```bash
# Print persistent shell completions
cairnbin-cli --shell completions bash

# Add shell initialization to the Bash profile
cairnbin-cli --shell init bash >> ~/.bashrc

# Initialize the current session
eval "$(cairnbin-cli --shell init bash)"

# Show shell integration help
cairnbin-cli --shell help
```

### get

Fetch and print the raw content of a paste.

```bash
cairnbin-cli get <id> [--output FORMAT]
```

**Example:**

```bash
cairnbin-cli get abc12345
cairnbin-cli get abc12345 --output json
```

### delete

Delete a paste using its owner token. The CLI sends the token in the native Bearer authorization header.

```bash
cairnbin-cli delete <id> <delete-token>
```

**Example:**

```bash
cairnbin-cli delete abc12345 own_AbCdEfGh12345678ijklMNOP87654321
```

### list

List recent public pastes.

```bash
cairnbin-cli list [--limit N] [--page N] [--output FORMAT]
```

**Example:**

```bash
cairnbin-cli list --limit 20
```

### Scheduler maintenance

Inspect and control scheduled tasks with the server binary. Commands require root or operator authorization.

```bash
cairnbin --maintenance scheduler list
cairnbin --maintenance scheduler show <id>
cairnbin --maintenance scheduler history <id>
cairnbin --maintenance scheduler run <id>
cairnbin --maintenance scheduler enable <id>
cairnbin --maintenance scheduler disable <id>
```

Task state and history are read from the configured database. Enable/disable changes are stored in `yaml.d/maintenance.yml`; manual runs use the same registered task function and persistence path as scheduled runs.

## Shell Pipeline Examples

```bash
# Paste from pipe
cat file.go | cairnbin-cli create --lang go

# Paste output of a command
kubectl logs my-pod | cairnbin-cli create --lang text --title "Pod Logs"

# Fetch paste content
cairnbin-cli get abc12345 > downloaded.txt
```
