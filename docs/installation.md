# Installation

## Binary (recommended)

Download the latest binary for your platform from [GitHub Releases](https://github.com/apimgr/pastebin/releases).

```bash
# Linux amd64
curl -Lo pastebin https://github.com/apimgr/pastebin/releases/latest/download/pastebin-linux-amd64
chmod +x pastebin
./pastebin
```

Available platforms: `linux-amd64`, `linux-arm64`, `darwin-amd64`, `darwin-arm64`,
`windows-amd64.exe`, `windows-arm64.exe`, `freebsd-amd64`, `freebsd-arm64`.

## Docker

```bash
docker run -d \
  --name pastebin \
  -p 172.17.0.1:64580:80 \
  -p 9999:9999 \
  -v pastebin-data:/data \
  ghcr.io/apimgr/pastebin:latest
```

## Docker Compose

Copy `docker/docker-compose.yml` into a deployment directory of your choice (never run it from a source checkout) and start it there; `./volumes/config` and `./volumes/data` are created next to the file.

```bash
mkdir -p /srv/pastebin && cp docker/docker-compose.yml /srv/pastebin/
cd /srv/pastebin && docker compose up -d
```

## Systemd Service

```bash
sudo ./pastebin --service --install
sudo systemctl start pastebin
sudo systemctl enable pastebin
```

On first start the binary creates the `pastebin` service account, its directories, binds its port and then drops privileges. Other init systems (OpenRC, SysVinit, runit, rc.d, launchd and Windows services) are installed by the same `--service --install` command.

## Build from Source

Requires Docker only — no local Go installation needed.

```bash
git clone https://github.com/apimgr/pastebin
cd pastebin
make build        # all 8 platforms → binaries/
make local        # current platform → binaries/
make dev          # quick dev build → /tmp/apimgr/pastebin-XXXXXX/
```

## First Run

The server works with zero configuration. On first run it creates:

- Config: `/etc/apimgr/pastebin/server.yml` (Linux)
- Data / DB: `/var/lib/apimgr/pastebin/db/server.db` (Linux)
- Logs: `/var/log/apimgr/pastebin/` (Linux)

On first run the server binds all interfaces on a random port in the 64000-64999 range and saves it to `server.yml`; the chosen URL is printed to the console at startup. Pass `--port` to pin a specific port.
