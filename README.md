<p align="center" style="text-align: center">
  <img src="https://github.com/YouROK/TorrServer/assets/144587546/53f7175a-cda4-4a06-86b6-2ac07582dcf1" width="33%"><br/>
</p>

<p align="center">
  <b>Simple and powerful torrent streaming server.</b>
  <br/>
  <br/>
  <a href="https://github.com/YouROK/TorrServer/blob/master/LICENSE">
    <img alt="GitHub" src="https://img.shields.io/github/license/YouROK/TorrServer"/>
  </a>
  <a href="https://github.com/YouROK/TorrServer/tags" rel="nofollow">
    <img alt="GitHub tag (latest SemVer pre-release)" src="https://img.shields.io/github/v/tag/YouROK/TorrServer?include_prereleases&label=version"/>
  </a>
</p>

---

# TorrServer Extended 

🔥 **TorrServer Extended** is a self‑hosted torrent streaming server with a modern web UI, simple HTTP API and first‑class Docker support. Point it to a torrent or magnet link and start watching immediately – no pre‑download required.

> Recommended deployment: **Docker Compose** (with persistent volumes and well‑structured data layout).

---

## ✨ Features

- 🎬 **Instant torrent streaming** – play while downloading, no manual pre‑download.
- 🌐 **Web UI** – manage torrents and playback from any browser.
- 🧲 **Magnet & .torrent support** – drop `.torrent` or `.magnet` files into a watched folder and they are picked up automatically.
- 📂 **Blackhole integration** – out‑of‑the‑box directory layout for Sonarr/Radarr‑like apps (`downloads/stream` + categories).
- 📺 **Smart TV friendly** – works great with Media Station X and other DLNA / HTTP clients.
- 🔐 **HTTP auth & IP allow/deny lists** – protect access to your server.
- 🧱 **Persistent data model** – separates config, data, downloads and streaming outputs.
- 🐳 **Official Docker images** – simple to run, update and back up.

---

## 🚀 Quick Start (Docker Compose – Recommended)

Create a `docker-compose.yml` next to a fresh project directory and adjust paths/ports if needed:

```yaml
version: "3.8"

services:
  torrserver:
    image: ghcr.io/yourok/torrserver:latest
    container_name: torrserver
    network_mode: host              # required if you want DLNA to work
    environment:
      - TS_PORT=8090                # web UI / API port inside container
      - TS_DONTKILL=1               # keep running on signals
      - TS_HTTPAUTH=0               # set to 1 to enable basic auth
      - TS_CONF_PATH=/opt/ts/config # config & db
      - TS_TORR_DIR=/opt/ts/torrents
    volumes:
      - ./data:/opt/ts              # persistent data, config, downloads, stream, torrents
    restart: unless-stopped
```

Then run:

```bash
docker compose up -d
```

Open the web UI at:

- <http://localhost:8090>

> The `./data` directory on the host will hold everything: config, database, downloads, stream files and the `torrents` blackhole tree.

---

## 🧱 Directory Layout (inside container)

By default TorrServer stores everything under `/opt/ts`:

- `/opt/ts/config` – configuration & database.
- `/opt/ts/data` – main data root.
  - `/opt/ts/data/downloads/<category>` – downloaded files by category.
  - `/opt/ts/data/stream/<category>` – streaming output & STRM files.
- `/opt/ts/torrents` – **blackhole root** for external download managers / media apps:
  - `/opt/ts/torrents/downloads/<category>` – `.torrent` / `.magnet` files that should be **auto‑downloaded**.
  - `/opt/ts/torrents/stream/<category>` – `.torrent` / `.magnet` files that should be **streamed only**.

Categories are the same as in the web UI (for example: `movie`, `series`, `music`, `other`, `uncategorized`).

You can override locations with env variables (see below), but the structure is always the same.

---

## ⚙️ Environment Variables

Most configuration can be done via environment variables (recommended for Docker). Below is a summary of commonly used ones:

| Variable        | Default (inside container)   | Description |
|-----------------|------------------------------|-------------|
| `TS_PORT`       | `8090`                       | HTTP port TorrServer listens on. Map it with `-p HOST:TS_PORT`. |
| `TS_HTTPAUTH`   | `0`                          | `1` to enable basic HTTP auth using `accs.db`. |
| `TS_RDB`        | `0`                          | `1` to start in read‑only DB mode. |
| `TS_DONTKILL`   | `0`                          | `1` to ignore shutdown signals (useful with some supervisors). |
| `TS_CONF_PATH`  | `/opt/ts/config`             | Path where config and DB are stored. |
| `TS_DATA_PATH`  | `/opt/ts/data` (effective)   | Root of internal data (downloads/stream). Auto‑resolved if not set. |
| `TS_TORR_DIR`   | `/opt/ts/torrents`           | Root for watched torrents directory (blackhole). Can be pointed elsewhere. |
| `TS_LOG_PATH`   | `/opt/ts/log` or inside data | Custom log file location. |

Other flags are available as CLI arguments as well (see “Server Arguments” below).

---

## 📥 Blackhole / Autoload Torrents

TorrServer Extended can automatically pick up `.torrent` and `.magnet` files from a watched directory and create torrents (and download jobs) for them.

### Structure

Under `TS_TORR_DIR` (default `/opt/ts/torrents` in Docker, or next to `data` when running directly) TorrServer expects:

```text
torrents/
  downloads/
    movie/
    series/
    music/
    other/
    uncategorized/
  stream/
    movie/
    series/
    music/
    other/
    uncategorized/
```

- Files in `downloads/<category>` → create torrent **and** a **download job**.
- Files in `stream/<category>` → create torrent for **stream‑only**.

TorrServer watches this directory periodically and will:

- Read `.torrent` files and add them as torrents (then remove the file).
- Read `.magnet` files (single magnet/URL per file), parse them, add as torrents (then remove the file).

This makes it easy to integrate with Sonarr/Radarr and other apps that support “blackhole” style download directories.

---

## 🐳 Docker `run` Examples

Minimal, non‑persistent run:

```bash
docker run --rm -d \
  --name torrserver \
  -p 8090:8090 \
  ghcr.io/yourok/torrserver:latest
```

Persistent run with volumes and basic configuration:

```bash
docker run --rm -d \
  --name torrserver \
  -p 8090:8090 \
  -e TS_PORT=8090 \
  -e TS_DONTKILL=1 \
  -e TS_CONF_PATH=/opt/ts/config \
  -e TS_TORR_DIR=/opt/ts/torrents \
  -v "$PWD/data":/opt/ts \
  ghcr.io/yourok/torrserver:latest
```

If you need DLNA on the host network, add `--network host` (Linux only) instead of `-p`:

```bash
docker run --rm -d \
  --name torrserver \
  --network host \
  -e TS_PORT=8090 \
  -v "$PWD/data":/opt/ts \
  ghcr.io/yourok/torrserver:latest
```

---

## 🖥️ Running Natively (No Docker)

You can still run TorrServer directly on your OS.

### Linux & macOS

```bash
cd server
go run ./cmd
```

Then open <http://localhost:8090>.

Alternatively, use the install scripts from the original project if you want systemd service integration.

### Windows

Download the binary from the releases page and run it directly:

```text
TorrServer-windows-amd64.exe
```

---

## 🛠️ Server Arguments

The server binary accepts command‑line flags (these can also be combined with env variables):

- `--port`, `-p` – HTTP port (default `8090`).
- `--ssl` – enable HTTPS for the web server.
- `--sslport` – HTTPS port (default `8091`).
- `--sslcert` – path to SSL certificate file.
- `--sslkey` – path to SSL private key file.
- `--path`, `-d` – database and config directory path.
- `--logpath`, `-l` – server log file path.
- `--weblogpath`, `-w` – web access log file path.
- `--rdb`, `-r` – start in read‑only DB mode.
- `--httpauth`, `-a` – enable HTTP auth on all requests.
- `--dontkill`, `-k` – don’t stop on signals.
- `--ui`, `-u` – open the TorrServer page in the default browser.
- `--torrentsdir`, `-t` – override the root directory for autoloaded torrents.
- `--torrentaddr` – override torrent client listen address.
- `--pubipv4`, `-4` – public IPv4 address.
- `--pubipv6`, `-6` – public IPv6 address.
- `--searchwa`, `-s` – allow search without authentication.
- `--help`, `-h` – show help and exit.
- `--version` – show version and exit.

---

## 🔐 Authentication

If HTTP auth is enabled (`TS_HTTPAUTH=1` or `-a/--httpauth`), TorrServer expects a file named `accs.db` in the config directory (e.g. `/opt/ts/config/accs.db`).

Example `accs.db` content:

```json
{
  "User1": "Pass1",
  "User2": "Pass2"
}
```

Basic authentication is then required for all web and API requests.

---

## 📜 IP Whitelist / Blacklist

Place the following files next to `config.db` (usually in `/opt/ts/config`):

- `wip.txt` – whitelist.
- `bip.txt` – blacklist.

Whitelist has priority over blacklist. Example content:

```text
local:127.0.0.0-127.0.0.255
127.0.0.0-127.0.0.255
local:127.0.0.1
127.0.0.1
# lines starting with # are comments
```

---

## 📺 Smart TV (Media Station X)

1. Install **Media Station X** on your Smart TV (see [platform support](https://msx.benzac.de/info/?tab=PlatformSupport)).
2. Open it and go to: **Settings → Start Parameter → Setup**.
3. Enter the current IP and port of TorrServer, for example: `http://192.168.1.10:8090`.

---

## 📚 API & Swagger

TorrServer exposes a simple HTTP API which is documented via Swagger/OpenAPI.

- Swagger UI: `http://<host>:<port>/swagger/index.html`

This can be used to integrate custom tools, bots, or media frontends.

---

## 🤝 Credits & Thanks

TorrServer is originally created and maintained by the [YouROK](https://github.com/YouROK/TorrServer) project and a large community of contributors.

Thanks to everyone who has tested, translated, packaged, and integrated TorrServer across platforms and devices.
