# CineRoute

Lightweight self-hosted web app that routes movie and TV `.torrent` files
straight into a multi-drive media library.

Drop a torrent, CineRoute parses it (without downloading anything), detects
whether it is a movie or TV show, extracts a search title, finds the canonical
title on TMDB, reuses the existing TV-show folder across four HDDs or picks
the drive with the most usable free space, creates only the parent folder
(`Title (Year)`), adds the torrent to qBittorrent in the **stopped** state,
verifies the save path, content layout, category and settings exactly, then
starts it.

That's it. No renaming, moving, hardlinking, completion monitoring, or
post-processing — original torrent filenames and internal folder structure
are preserved verbatim.

## Workflow

```text
Upload .torrent
        ↓
Parse torrent metadata
        ↓
Detect movie or TV show
        ↓
Extract likely title, year and season
        ↓
Search TMDB
        ↓
User confirms the TMDB result
        ↓
Search existing media folders
        ↓
Reuse an existing TV folder or choose an HDD
        ↓
Create the canonical Title (Year) parent folder
        ↓
Add torrent to qBittorrent in the stopped state
        ↓
Verify save path, content layout, category and settings
        ↓
Start the torrent
        ↓
Done
```

## Requirements

* **Go 1.24+** to build, or use the Docker image.
* **qBittorrent 5.x** (Web API 2.11+) with the WebUI enabled.
* Four HDDs with movie/TV roots (see config below). CineRoute and
  qBittorrent must see the **same** paths.

## Configuration

Copy `config.example.yaml` to `config.yaml` (or set `CINEROUTE_CONFIG`).
Secrets can also come from environment variables:

| Setting | Env var |
| --- | --- |
| TMDB API key | `CINEROUTE_TMDB_API_KEY` |
| qBittorrent URL / user / password | `CINEROUTE_QBIT_URL` / `CINEROUTE_QBIT_USERNAME` / `CINEROUTE_QBIT_PASSWORD` |
| Web UI password (basic auth, user `cineroute`) | `CINEROUTE_AUTH_PASSWORD` |

```yaml
listen: "127.0.0.1:8787"   # use 0.0.0.0:8787 inside a container
auth_password: ""          # set a password or use CINEROUTE_AUTH_PASSWORD

tmdb:
  api_key: ""              # v3 API key or v4 read access token, or CINEROUTE_TMDB_API_KEY
  language: "en-US"

qbittorrent:
  url: "http://localhost:8080"
  username: "admin"
  password: ""
  movie_category: "cineroute-movie"
  tv_category: "cineroute-tv"

drives:
  - id: "hdd1"
    movie_root: "/m1"      # movie root
    tv_root: "/t1"         # TV root on the same physical drive
    reserve_bytes: 107374182400   # 100 GiB safety reserve
  # hdd2..hdd4: /m2-/t2, /m3-/t3, /m4-/t4
```

Run:

```sh
go build -o cineroute ./cmd/cineroute
./cineroute -config config.yaml
```

Open `http://127.0.0.1:8787`.

## Docker Compose

See `compose.example.yaml`. The container should run as UID 1001 / GID 10 and
mount the same media paths both CineRoute and qBittorrent use, e.g.:

```yaml
volumes:
  - /volume1/hdd1/movies:/m1
  - /volume1/hdd1/tv:/t1
  # ... hdd2..hdd4 as /m2-/t2, /m3-/t3, /m4-/t4
```

Set `CINEROUTE_LISTEN=0.0.0.0:8787` (already in the example) — the default
`127.0.0.1` listen address is unreachable from outside the container.

### Prebuilt image (GHCR)

The GitHub Actions workflow in `.github/workflows/docker.yml` runs the tests
and builds `linux/amd64` + `linux/arm64` images to
`ghcr.io/<your-username>/cineroute` on every push to `main` and on `v*` tags
(`v1.2.3` → `1.2.3`, `1.2`, plus `main` and short-SHA tags). No extra secrets
are needed — it uses the built-in `GITHUB_TOKEN`. Pull requests get a
build-only check without pushing.

## qBittorrent requirements

CineRoute refuses to submit unless:

* Web API version is 2.11+.
* **Pre-allocate disk space** is disabled (`preallocate_all = false`).
* **Keep incomplete torrents in** is disabled (`temp_path_enabled = false`).
* Categories `cineroute-movie` and `cineroute-tv` exist with **empty save
  paths** (CineRoute creates them if missing).

Every submission is manual (`autoTMM=false`), stopped, tagged
(`cineroute`, `cineroute-<intake>`, `tmdb-<id>`, `<drive>`), with an explicit
topology-derived `root_folder` value. Nothing is started until the stopped
add passes exact verification (hash, save path, content path, file tree,
category, size, state).

## Behavior notes

* **Layout is derived from torrent structure, never guessed:**
  single-file → `root_folder=false`; rooted v1/v2/hybrid multi-file →
  `root_folder=true`; rootless BEP 52 → `root_folder=false`.
* **v1, v2 and hybrid torrents** are supported. Hash verification uses the
  hash qBittorrent actually reports: v1 for v1/hybrid, v2 (SHA-256) for
  pure-v2 torrents.
* **Existing TV show** is always kept on its drive — every season goes into
  the same `Title (Year)` folder regardless of free space. If that drive is
  tight, a warning is shown but the submission is never blocked.
* **New titles** go to the drive with the most usable space
  (free − reserve − qBittorrent incomplete bytes).
* **Forgiving TMDB search:** if the year filter returns nothing (a year
  that is part of the title, like *Blade Runner 2049*, or a season pack
  carrying the season's air year instead of the show's first-air year), the
  search is retried with an alternate title and then without a year.
* **Duplicates** (same info hash already in qBittorrent) are blocked.
* The destination is recomputed authoritatively under the allocation lock at
  submit time; a library that changed while you reviewed is never silently
  overwritten.
* Intakes live in memory (no database yet); qBittorrent is the source of
  truth for what was submitted.

## Not implemented yet

SQLite history, intake recovery after crash, singleton lease, archives,
auto-submit mode.
