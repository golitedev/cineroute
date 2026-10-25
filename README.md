# CineRoute

Lightweight self-hosted web app that routes movie and TV `.torrent` files
straight into a multi-drive media library.

Drop a torrent, CineRoute parses it (without downloading anything), detects
whether it is a movie or TV show, extracts a search title, finds the canonical
title on TMDB, reuses the existing TV-show folder across four HDDs or picks
the drive with the most free space, creates only the parent folder
(`Title (Year)`), adds the torrent to qBittorrent in the **stopped** state,
verifies the save path, content layout, category and settings exactly, then
starts it.

The routing flow does not rename, move, or post-process downloaded content;
original torrent filenames and internal folder structure are preserved
verbatim. Hardlinks are managed from the Movies, TV and Hardlinks pages.

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
Auto-confirm the top result (pick another if wrong)
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
* Docker image only: `ffmpeg`/`ffprobe` and the vendored `alass` are bundled.
  For a source build, install `ffmpeg` and `alass` (or point
  `subtitles.ffmpeg_path` / `subtitles.alass_path` at existing binaries).

## Configuration

Copy `config.example.yaml` to `config.yaml` (or set `CINEROUTE_CONFIG`).
Secrets can also come from environment variables:

| Setting | Env var |
| --- | --- |
| TMDB API key | `CINEROUTE_TMDB_API_KEY` |
| qBittorrent URL / user / password | `CINEROUTE_QBIT_URL` / `CINEROUTE_QBIT_USERNAME` / `CINEROUTE_QBIT_PASSWORD` |
| Prowlarr URL / API key (future integrations) | `CINEROUTE_PROWLARR_URL` / `CINEROUTE_PROWLARR_API_KEY` |
| OpenSubtitles API key / user / password (Subtitles page) | `CINEROUTE_OS_API_KEY` / `CINEROUTE_OS_USERNAME` / `CINEROUTE_OS_PASSWORD` |
| Enable or disable the Subtitles page | `CINEROUTE_SUBTITLES_ENABLED` |
| Log level (`debug`, `info`, `warn`, `error`; default `info`) | `CINEROUTE_LOG_LEVEL` |
| Web UI username (login form and basic auth, default `cineroute`) | `CINEROUTE_AUTH_USERNAME` |
| Web UI password (login form with a 90-day session cookie; basic auth is the fallback) | `CINEROUTE_AUTH_PASSWORD` |
| Listen address | `CINEROUTE_LISTEN` |

```yaml
listen: "127.0.0.1:8787"   # use 0.0.0.0:8787 inside a container
auth_password: ""          # set a password or use CINEROUTE_AUTH_PASSWORD

tmdb:
  api_key: ""              # v3 API key or v4 read access token, or CINEROUTE_TMDB_API_KEY
  language: "en-US"

qbittorrent:
  url: "http://192.168.18.29:8080"
  username: "admin"
  password: ""

drives:
  - id: "disk1"
    movie_root: "/disk1/media/movies"
    movie_remote_root: "/disk1/media/movies-remote"
    tv_root: "/disk1/media/tv"
    tv_remote_root: "/disk1/media/tv-remote"
    anime_root: "/disk1/media/anime"
  # Repeat the same layout below /disk2/media, /disk3/media and /disk4/media.
```

Run:

```sh
go build -o cineroute ./cmd/cineroute
./cineroute -config config.yaml
```

Open `http://127.0.0.1:8787`.

## Docker Compose

See `compose.example.yaml`. The container should run as UID 1000 / GID 1000 and
mount each HDD once at the same path in CineRoute and qBittorrent, e.g.:

```yaml
volumes:
  - /mnt/disk1/data:/disk1
  - /mnt/disk2/data:/disk2
  - /mnt/disk3/data:/disk3
  - /mnt/disk4/data:/disk4
```

On the Debian NAS, `/mnt/disk1/data/media/movies/6 Years (2015)` is
`/disk1/media/movies/6 Years (2015)` inside CineRoute, qBittorrent and Jellyfin.
The same mapping applies to `movies-remote`, `tv` and `tv-remote` on all four
disks. CineRoute needs writable mounts for folder creation, hardlinks and
subtitles. Its config, databases and scratch files live under
`/srv/docker/cineroute` in the Compose example; make its `data` and `tmp`
directories writable by UID/GID `1000:1000`.

The example connects to qBittorrent at `http://192.168.18.29:8080`, so the
containers can run in separate Compose projects. Set `CINEROUTE_QBIT_URL`
to override it. Set `CINEROUTE_PROWLARR_URL` to a reachable NAS address if
Prowlarr does not share CineRoute's Docker network.

When migrating from UGOS, update the `drives` section of your existing
`/srv/docker/cineroute/config.yaml` to the four entries in
`config.example.yaml`, keeping your credentials and other settings. Existing
config entries override built-in defaults. Copy your existing CineRoute state
to `/srv/docker/cineroute/data` if you want to keep it, update the Compose
mounts, then recreate the container and rescan Movies, TV, hardlinks and subtitles to
index the new paths.

Set `CINEROUTE_LISTEN=0.0.0.0:8787` (already in the example) — the default
`127.0.0.1` listen address is unreachable from outside the container.

### Prebuilt image (GHCR)

The GitHub Actions workflow in `.github/workflows/docker.yml` runs the tests
and builds `linux/amd64` + `linux/arm64` images to
`ghcr.io/<your-username>/cineroute` on every push to `main` and on `v*` tags
(`v1.2.3` → `1.2.3`, `1.2`, plus `main`, `latest` and short-SHA tags). No
extra secrets are needed — it uses the built-in `GITHUB_TOKEN`. Pull requests
get a build-only check without pushing.

## qBittorrent requirements

CineRoute refuses to submit unless:

* Web API version is 2.11+.
* **Pre-allocate disk space** is disabled (`preallocate_all = false`).
* **Keep incomplete torrents in** is disabled (`temp_path_enabled = false`).

Every submission is manual (`autoTMM=false`), stopped, with no category and
no tags, and an explicit topology-derived `root_folder` value. Nothing is
started until the stopped add passes exact verification (hash, save path,
content path, file tree, empty category/tags, size, state).

## Behavior notes

* **Layout is derived from torrent structure, never guessed:**
  single-file → `root_folder=false`; rooted v1/v2/hybrid multi-file →
  `root_folder=true`; rootless BEP 52 → `root_folder=false`.
* **v1, v2 and hybrid torrents** are supported. Hash verification uses the
  hash qBittorrent actually reports: v1 for v1/hybrid, v2 (SHA-256) for
  pure-v2 torrents.
* **Existing movies and TV shows** are always kept on their drive. Main
  intake checks both the main and remote roots, so a title found only under
  `movies-remote` or `tv-remote` anchors a main download to the matching HDD.
  If that drive is tight, a warning is shown but the submission is never
  blocked.
* **New titles** go to the drive with the most plain free space.
* **Anime is a separate shared library destination.** Select Anime during
  intake to route the title to the matching drive's `anime_root`. Existing
  anime stays on its current drive and new anime uses the drive with the most
  free space. Anime has no remote copy, hardlink workflow.
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
auto-submit mode, subtitles for the remote **TV** roots, OCR for
image-based subtitles (PGS/DVD) and audio/voice-activity reference mode.

## Movies, TV and hardlinks

The **Movies** and **TV** pages scan the main and remote library roots across
all configured drives. Each title shows its disk, video files and whether it
exists in main, remote or both. Filter the inventory by title, disk or path,
or select Main only, Remote only or Both copies.

Open a title and use **Hardlink to remote** or **Hardlink to main** to fill
whichever side has no video. The complete source tree, including subtitles,
is preserved. Existing links to the same inode are safe to retry; symlinks,
special files and conflicting destination files are rejected. A fresh scan
before linking revalidates the title and destination. Titles with no video
on either side are omitted. Inventories are filesystem-backed: scan again
after restarting CineRoute or changing files externally.

Main and remote roots must be below the same `/diskN` mount and the CineRoute
UID/GID must be allowed to create files in the destination root. Hardlinks
cannot cross filesystems, container mounts or Btrfs subvolumes.

The **Manage hardlinks** tab scans all configured remote movie and TV roots and
identifies hardlinks by inode, including links whose primary folder was renamed
outside CineRoute. It shows source and remote paths, file maps, link counts,
linked data, health and relink warnings. Folders whose remote copy contains
files the primary folder does not have, whose primary folder was renamed, or
whose links sit at older relative paths are reported as **Needs relink**.
**Relink** normally moves a remote folder to
the current primary folder name and then mirrors the primary tree into it:
missing links are created and remote files the primary folder does not have —
old links left behind by renames or replaced subtitles, and unrelated extras —
are removed. The **Relink mirror** toggle flips the direction: with
**Remote → main**, remote files the primary folder does not have are
hardlinked into the primary folder instead. That direction is additive only
and never removes or replaces anything in the primary library. In both
directions the remote copy is treated as disposable and the primary library
stays intact.
**Remove** unlinks only verified hardlinked files and keeps the primary files
and unrelated remote files. The tab can be rescanned at any time and supports
searching and status/media filters.

The Prowlarr client and optional URL/API-key configuration remain available
for future integrations. The app does not perform tracker searches or offer
release approvals. Old `companion` settings and `prowlarr.indexer_name` entries
are ignored; existing workflow databases are no longer opened or migrated and
can be archived separately.

CineRoute preserves original torrent filenames and does not rename seeded files.

## Subtitles

The **Subtitles** page adds external subtitles to movies in the remote movie
libraries (`movie_remote_root`). `subtitles.target_languages` lists the languages
to add — `["sv", "es", "en"]` by default — and a movie is only finished once it
has all of them. For every remote video file CineRoute:

1. searches OpenSubtitles.com once for every language the movie is missing,
   resolving the canonical feature and auditing every candidate (title, year,
   source, edition, forced, machine-translated and multi-part/collection
   releases). A matching title may be one year away from the movie's year —
   films open in different countries in different years, so a 2017 film is
   routinely released as `Movie.2018...` — while a two-year gap is still treated
   as a different film. A language with no safe candidate is reported per
   language and does not stop the others, and a movie with no safe candidate at
   all is reported as **No match** — nothing is read from the video file;
2. picks a timing reference — an external `en`/`es` subtitle next to the movie,
   else an embedded English stream, else embedded Spanish, else any other
   embedded **text** stream (image-based PGS/DVD subtitles cannot be used). This
   is the step that demuxes a whole video file for an embedded stream, so it runs
   at most once, after a subtitle to sync actually exists, and the same reference
   is reused for every language;
3. downloads the best candidates per language (up to `max_candidates`, default 5
   each) and syncs each one against the reference with **alass**;
4. installs `<video-basename>.<language>.srt` next to the movie only when the
   alignment passes the timing gate (90 % of cues within 2 s, p90 ≤ 2.5 s,
   bounded start and end gaps, no clamped cues, no over-long junk cues). Anything
   else stays in the work directory with its metrics and is reported as **Needs
   review** for that language.

Spanish is treated as three OpenSubtitles languages in one: the search asks for
`es`, `ea` (Spanish (LA)) and `sp` (Spanish (EU)), and a Latin American candidate
is tried before a Castilian one — scene releases tag them `LATINO`/`LATAM` and
`CASTELLANO`. Variants are also read from a filename or stream tag such as
`Movie.es-419.srt`, so an existing Latin American subtitle counts as Spanish.

When only some of the configured languages work out, the movie is reported as
**Partly added**, keeps the subtitles it got, and shows a line per language, for
example `sv added`, `es no match`, `en already there (embedded stream #3)`.

**Scan remote movies** reads `movie_remote_root` on every drive and lists every
remote movie; movies that already have every target subtitle (an external
`*.<language>.srt` or an embedded stream) are shown as **Has subtitles** and are
never queued. Movies that still miss one become **Needs subtitles**, and the page
shows for each whether an external text subtitle is available or whether the
reference must be extracted from an embedded stream — the **No external SRT** /
**Has external SRT** filters and the header counters split those two groups.

The scan analyzes each video's subtitle streams and caches the result by
path+size+mtime, so later scans are quick. A successful scan removes deleted
videos and obsolete library paths from the queue and database, together with
their cached searches, candidates and attempts. Missing or unreadable roots and
canceled scans preserve the previous inventory. With the default
`scan_batch_size: 0` every video is analyzed in the first scan; if you cap it,
movies that were not reached yet are marked **Not analyzed** and are probed on
demand when you run them, so an incomplete scan never produces a false "no
reference". When a movie is processed, every usable reference is tried in order
(external `en`/`es` files, then embedded streams, forced/signs-only tracks
last), so one unusable track does not abandon the movie.

**Add missing subtitles** processes the next batch; every row can also be run,
retried with more candidates (up to 20 per language), skipped or reset on its
own. **Skip** removes a movie from the work queue and it stays skipped across
rescans until you press **Reset**. Each movie shows its detected languages,
external subtitle files, embedded streams, the chosen reference, the per-language
outcome with the installed path and timing metrics, and the full attempt history
with the language and Spanish variant of every attempt.

Every step is logged: the resolved subtitle configuration at startup, each
scan (roots, folders, videos, queued movies, probe failures), each movie's
chosen reference, OpenSubtitles searches and quota, downloads, the alass run,
the timing metrics and the accept/reject decision, and every HTTP request the
browser makes. Set `CINEROUTE_LOG_LEVEL=debug` (or pass `-log-level debug`) to
add per-request OpenSubtitles and per-file probe details.

Set the credentials with `subtitles.opensubtitles.*` or
`CINEROUTE_OS_API_KEY` / `CINEROUTE_OS_USERNAME` / `CINEROUTE_OS_PASSWORD`.
Searches never consume download quota; downloads do, so CineRoute stops as soon
as `subtitles.quota_reserve` downloads remain and reports the reset time. That
stop is checked before the reference is extracted as well as before each
download, so a movie that cannot be downloaded is not demuxed for nothing. An API
key alone works, but a logged-in account has the higher free-tier quota. Three
target languages can cost up to three downloads per movie, so `quota_reserve`
stops the batch before the reserve is gone and the movie stays queued for a later
run.

Processing one movie takes seconds when the reference is an external `.srt`,
but extracting an **embedded** reference means demuxing the whole video file,
which can take several minutes for a large movie on a spinning disk; the alass
sync itself is fast. Because the OpenSubtitles search runs first, that cost is
only paid for movies that have a candidate to sync — a movie OpenSubtitles has
nothing for is reported as **No match** without reading the video at all. While
the extraction does run, the movie's row shows how far ffmpeg has demuxed, for
example `reference… · 42% · 0:50:00 of 2:00:00 · 4m 12s in this step`, so a slow
extraction can be told apart from a stuck job; the percentage comes from ffmpeg's
own progress stream and the elapsed time from the stage start.

`subtitles.extract_timeout_seconds` (default 900) bounds that step, so a batch of
20 large movies can run for a while. **Cancel** stops the run immediately: the
movie stays queued as **Needs subtitles** (a canceled run learns nothing about
it, so it is never filed as **No reference**) and the log records the cancel.
The next run starts the demux again from the first byte, because a partial
extraction cannot be resumed — cancelling and restarting a movie that is already
minutes into its extraction only pays for those minutes twice. If the extraction
does time out, the movie is reported as **Failed** with an explicit timeout
message instead of being written off as having no reference, and the remaining
embedded streams are not tried: they would have to read the same video file
again, so one timeout does not become four.

The queue lives in `/data/subtitles.db`; every intermediate file
(`reference.srt`, `<file_id>.raw.srt`, `<file_id>.aligned.srt`) lives under
`subtitles.work_dir`, which defaults to `/tmp/cineroute-subtitles`.

If processing a movie fails with `create work directory ...: permission denied`,
the container user cannot write `subtitles.work_dir`. That happens when `/tmp`
is bind-mounted from a host directory owned by another user, because the mount
replaces the image's world-writable `/tmp`. Either make the host directory
writable by the container user (`chown 1000:1000 <host-dir>` to match the compose
`user:` setting) or point `subtitles.work_dir` at a path that is already
writable inside the container, for example `/data/subtitles-work`. The Subtitles
page shows the problem at the top of the page, and the startup log reports
`subtitles: work directory is not writable`. Mount `/tmp`
on real storage (see `compose.example.yaml`), use **Clear work files** to reclaim
space, and `work_retention_days` prunes stale folders at startup.

The workflow — reference selection, the multi-query search, candidate auditing,
the alass invocation, the timing gate and the promo/malformed cue cleanup — is a
direct port of the batch pipeline that produced the remote library's existing
Swedish subtitles, so results match that proven run. One caveat: a **main →
remote** hardlink relink removes remote files the primary folder does not have,
including generated `*.sv.srt` files. Run **remote → main** first if you want a
subtitle linked into the main copy as well; the Hardlinks toggle picks the
direction. alass is vendored under `third_party/alass` (GPL-3.0) and is always
executed as a separate process.
