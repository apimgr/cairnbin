# Installation

## Binary (recommended)

Download the latest binary for your platform from [GitHub Releases](https://github.com/apimgr/cairnbin/releases).

```bash
# Linux amd64
curl -q -LSsf -Lo cairnbin https://github.com/apimgr/cairnbin/releases/latest/download/cairnbin-linux-amd64
chmod +x cairnbin
./cairnbin
```

Available platforms: `linux-amd64`, `linux-arm64`, `darwin-amd64`, `darwin-arm64`,
`windows-amd64.exe`, `windows-arm64.exe`, `freebsd-amd64`, `freebsd-arm64`.

For an automated Linux/macOS install, run the project installer:

```bash
curl -q -LSsf https://github.com/apimgr/cairnbin/raw/main/scripts/install.sh | bash
```

Set `INSTALL_DIR` to choose the destination directory (default: `/usr/local/bin`).
Set `VERSION` to install a specific release tag; otherwise the latest release is used.

## Docker

```bash
docker run -d \
  --name cairnbin \
  -p 172.17.0.1:64580:80 \
  -p 9999:9999 \
  -v cairnbin-data:/data \
  ghcr.io/apimgr/cairnbin:latest
```

## Docker Compose

Copy `docker/docker-compose.yml` into a deployment directory of your choice (never run it from a source checkout) and start it there; `./volumes/config` and `./volumes/data` are created next to the file.

```bash
mkdir -p /srv/cairnbin && cp docker/docker-compose.yml /srv/cairnbin/
cd /srv/cairnbin && docker compose up -d
```

## Systemd Service

```bash
sudo ./cairnbin --service --install
sudo systemctl start cairnbin
sudo systemctl enable cairnbin
```

On first start the binary creates the `cairnbin` service account, its directories, binds its port and then drops privileges. Other init systems (OpenRC, SysVinit, runit, rc.d, launchd and Windows services) are installed by the same `--service --install` command.

## Build from Source

Requires Docker only — no local Go installation needed.

```bash
git clone https://github.com/apimgr/cairnbin
cd cairnbin
make build        # all 8 platforms → binaries/
make local        # current platform → binaries/
make dev          # quick dev build → /tmp/apimgr/cairnbin-XXXXXX/
```

## First Run

The server works with zero configuration. On first run it creates:

- Config: `/etc/apimgr/cairnbin/server.yml` (Linux)
- Data / DB: `/var/lib/apimgr/cairnbin/db/server.db` (Linux)
- Logs: `/var/log/apimgr/cairnbin/` (Linux)

On first run the server binds all interfaces on a random port in the 64000-64999 range and saves it to `server.yml`; the chosen URL is printed to the console at startup. Pass `--port` to pin a specific port.
