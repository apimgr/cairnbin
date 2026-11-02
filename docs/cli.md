# CLI Reference

The `pastebin-cli` binary is a fully spec-compliant command-line client.

## Global Flags

| Flag | Description |
|------|-------------|
| `--server URL` | Target server URL (default: `https://pste.us`, the embedded official site; or set `$PASTEBIN_SERVER_PRIMARY`) |
| `--token TOKEN` | Operator/owner API token (or set `$PASTEBIN_TOKEN`) |
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

Environment variables: `PASTEBIN_SERVER_PRIMARY`, `PASTEBIN_TOKEN`, `PASTEBIN_OUTPUT_FORMAT`, `PASTEBIN_OUTPUT_PAGER`, `PASTEBIN_OUTPUT_VERBOSE`, `PASTEBIN_OUTPUT_QUIET`, `PASTEBIN_PUBLIC`, `PASTEBIN_EXPIRE`, `PASTEBIN_SYNTAX`, `PASTEBIN_LIMIT`, `CLI_CONFIG`, and `NO_COLOR`. Response caching and rotating logs use the configured `cache.*` and `logging.*` settings in `cli.yml`.

## Commands

### create

Create a new paste from stdin or a file.

```bash
pastebin-cli create [file] [flags]
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

**Examples:**

```bash
# Paste from stdin
echo "Hello, World!" | pastebin-cli create

# Paste from stdin without an explicit command
echo "Hello, World!" | pastebin-cli

# Paste a file
pastebin-cli create myfile.go --lang go
pastebin-cli myfile.go --syntax go

# Burn-after-read with expiry
pastebin-cli create --burn 1 --expiry 1h secret.txt

# Unlisted paste with title
pastebin-cli create notes.txt --title "Meeting Notes" --unlisted

# Target a custom server (default: https://pste.us)
pastebin-cli --server https://your-server.example.com create myfile.txt
```

**Output:**

```
URL:          https://pste.us/abc12345
Delete Token: 64-char-hex-token (save this — shown once only)
```

### get

Fetch and print the raw content of a paste.

```bash
pastebin-cli get <id> [--output FORMAT]
```

**Example:**

```bash
pastebin-cli get abc12345
pastebin-cli get abc12345 --output json
```

### delete

Delete a paste using its delete token.

```bash
pastebin-cli delete <id> <delete-token>
```

**Example:**

```bash
pastebin-cli delete abc12345 64-char-hex-token
```

### list

List recent public pastes.

```bash
pastebin-cli list [--limit N] [--output FORMAT]
```

**Example:**

```bash
pastebin-cli list --limit 20
```

### scheduler

Inspect and control the server's built-in scheduled tasks.

```bash
pastebin-cli scheduler list
pastebin-cli scheduler show <id>
pastebin-cli scheduler history <id>
pastebin-cli scheduler run <id>
pastebin-cli scheduler enable <id>
pastebin-cli scheduler disable <id>
```

`list`, `show` and `history` are read-only. `run`, `enable` and `disable` need the operator token (`--token` or `auth.token` in `cli.yml`). Status symbols: `✓` success, `●` running, `✗` failed, `○` pending, `◐` skipped. Supports `--output json|yaml|csv|table`.

## Shell Pipeline Examples

```bash
# Paste from pipe
cat file.go | pastebin-cli create --lang go

# Paste output of a command
kubectl logs my-pod | pastebin-cli create --lang text --title "Pod Logs"

# Fetch paste content
pastebin-cli get abc12345 > downloaded.txt
```
