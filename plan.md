# CineRoute — Implementation Plan

## 1. Project summary

**CineRoute** is a lightweight self-hosted web application for adding movie and TV-show `.torrent` files to qBittorrent.

CineRoute will:

1. Accept a `.torrent` file through a drag-and-drop web interface.

2. Parse the torrent without downloading anything.

3. Determine whether it contains a movie or TV show.

4. Extract a likely title and optional year from the torrent name and file paths.

5. Search TMDB for the canonical title and release/first-air year.

6. Check the existing movie or TV roots across all four HDDs.

7. Keep new seasons of an existing TV show on the same HDD.

8. Place new titles on the HDD with the most usable free space.

9. Create only the canonical parent folder:

   ```text
   Movie Name (Year)
   TV Show Name (Year)
   ```

10. Add the torrent to qBittorrent in the stopped state, with that canonical parent folder as its exact save path.

11. Preserve every filename and every internal torrent directory exactly as supplied by the torrent.

12. Perform no post-download renaming, moving, hardlinking, importing, or restructuring.

CineRoute is finished with a torrent as soon as qBittorrent confirms the stopped add, exact content layout, category and save path, accepts the explicit start request, and reports that the torrent left the stopped state.

---

## 2. Explicit non-goals

CineRoute will not:

* Replace qBittorrent.
* Replace QUI.
* Monitor download completion.
* Rename downloaded files.
* Rename the torrent's own top-level folder.
* Move files after completion.
* Create hardlinks.
* Organize music, books, audiobooks, or other content.
* Scrape indexers.
* Search for torrents.
* Automatically remove torrents.
* Control seeding limits unless explicitly added later.
* Change torrents that are manually edited through QUI.

---

## 3. Correct workflow

The complete CineRoute workflow is:

```text
Upload .torrent
        ↓
Parse torrent metadata
        ↓
Detect movie or TV show
        ↓
Extract likely title and optional year
        ↓
Search TMDB
        ↓
Select canonical title and year
        ↓
Search existing library parent folders
        ↓
Select existing drive or drive with most usable space
        ↓
Create canonical parent folder
        ↓
Add torrent to qBittorrent stopped, with exact save path and explicit root-folder policy
        ↓
Verify qBittorrent save path, content path, file tree and settings
        ↓
Start torrent
        ↓
Done
```

There is intentionally no workflow after the intake transaction has verified the stopped add and started the exact torrent. In particular, there is no completion-time or post-download workflow.

---

## 4. Existing storage model

CineRoute should model the storage as four physical drives, not eight independent media roots.

| Drive  | Movie root | TV root |
| ------ | ---------- | ------- |
| `hdd1` | `/m1`      | `/t1`   |
| `hdd2` | `/m2`      | `/t2`   |
| `hdd3` | `/m3`      | `/t3`   |
| `hdd4` | `/m4`      | `/t4`   |

The host mappings are:

| Container path | Host path              |
| -------------- | ---------------------- |
| `/m1`          | `/volume1/hdd1/movies` |
| `/m2`          | `/volume3/hdd2/movies` |
| `/m3`          | `/volume4/hdd3/movies` |
| `/m4`          | `/volume5/hdd4/movies` |
| `/t1`          | `/volume1/hdd1/tv`     |
| `/t2`          | `/volume3/hdd2/tv`     |
| `/t3`          | `/volume4/hdd3/tv`     |
| `/t4`          | `/volume5/hdd4/tv`     |

CineRoute and qBittorrent must use the same container-side aliases. CineRoute must therefore send paths such as `/t1/Lost (2004)` to qBittorrent, not host paths such as `/volume1/hdd1/tv/Lost (2004)`.

### Startup filesystem validation

At startup, CineRoute should verify:

* `/m1` and `/t1` are on the same filesystem.
* `/m2` and `/t2` are on the same filesystem.
* `/m3` and `/t3` are on the same filesystem.
* `/m4` and `/t4` are on the same filesystem.
* Each configured root exists.
* Each root is writable by UID `1001` and GID `10`.
* Each drive pair resolves to a different filesystem device.
* No two configured drive IDs point at the same mount accidentally.

The filesystem device ID can be obtained using `stat(2)` or the Go equivalent.

---

## 5. Expected final paths

### 5.1 Single-file movie torrent

Torrent contents:

```text
Toy.Story.1995.REPACK.UHD.BluRay.2160p.TrueHD.Atmos.7.1.DV.HEVC.HYBRID.REMUX-FraMeSToR.mkv
```

CineRoute chooses `hdd4` and submits:

```text
savepath=/m4/Toy Story (1995)
root_folder=false
```

Result:

```text
/m4/Toy Story (1995)/
└── Toy.Story.1995.REPACK.UHD.BluRay.2160p.TrueHD.Atmos.7.1.DV.HEVC.HYBRID.REMUX-FraMeSToR.mkv
```

Only `Toy Story (1995)` is created by CineRoute.

The movie filename is unchanged.

### 5.2 Multi-file movie torrent

Torrent contents:

```text
Movie.Release.2026.Group/
├── Movie.Release.2026.Group.mkv
├── Movie.Release.2026.Group.nfo
└── Subtitles/
```

CineRoute submits:

```text
savepath=/m2/Movie Name (2026)
root_folder=true
```

Result:

```text
/m2/Movie Name (2026)/
└── Movie.Release.2026.Group/
    ├── Movie.Release.2026.Group.mkv
    ├── Movie.Release.2026.Group.nfo
    └── Subtitles/
```

The torrent's original root directory remains unchanged.

### 5.3 TV season pack

Existing library folder:

```text
/t1/Lost (2004)
```

Torrent contents:

```text
Lost.S02.1080p.DSNP.WEB-DL.DDP5.1.H.264-FLUX/
├── Lost.S02E01.Man.of.Science.Man.of.Faith.1080p.DSNP.WEB-DL.DDP5.1.H.264-FLUX.mkv
└── ...
```

CineRoute submits:

```text
savepath=/t1/Lost (2004)
root_folder=true
```

Result:

```text
/t1/Lost (2004)/
├── Lost.S01.1080p.DSNP.WEB-DL.DDP5.1.H.264-FLUX/
│   └── Lost.S01E24.Exodus.Part.2.1080p.DSNP.WEB-DL.DDP5.1.H.264-FLUX.mkv
└── Lost.S02.1080p.DSNP.WEB-DL.DDP5.1.H.264-FLUX/
    ├── Lost.S02E01.Man.of.Science.Man.of.Faith.1080p.DSNP.WEB-DL.DDP5.1.H.264-FLUX.mkv
    └── ...
```

CineRoute does not create a `Season 02` directory and does not modify the torrent folder.

---

## 6. Recommended implementation stack

### Backend

Use **Go** for the first implementation:

* Single compiled binary.
* Low idle memory usage.
* Easy multi-architecture Docker builds.
* HTML templates and static assets can be embedded in the binary.
* No Node.js or Python runtime is required in the final image.
* Filesystem and concurrent allocation logic are straightforward.

Suggested components:

* Standard `net/http` or `go-chi/chi`.
* `html/template`.
* `embed` for templates, CSS, and JavaScript.
* SQLite for intake history and allocation records.
* A pure-Go SQLite driver to avoid CGO in multi-platform builds.
* A torrent metainfo library that supports v1, v2, and hybrid torrents.
* Direct HTTP clients for TMDB and qBittorrent.

The `chi` project describes itself as a lightweight HTTP router, which fits a small single-service application.

### Frontend

Use server-rendered HTML with minimal JavaScript:

* Drag-and-drop upload area.
* Torrent summary.
* TMDB result selection.
* Destination preview.
* Submit button.
* Intake history.
* Configuration status page.

HTMX is optional. Plain JavaScript with `fetch()` and drag-and-drop events is sufficient for the MVP and avoids a frontend build pipeline.

---

## 7. Suggested repository structure

```text
cineroute/
├── cmd/
│   └── cineroute/
│       └── main.go
├── internal/
│   ├── allocator/
│   │   ├── allocator.go
│   │   └── allocator_test.go
│   ├── classifier/
│   │   ├── classifier.go
│   │   └── classifier_test.go
│   ├── config/
│   │   ├── config.go
│   │   └── validation.go
│   ├── library/
│   │   ├── index.go
│   │   ├── folder.go
│   │   └── filesystem.go
│   ├── qbittorrent/
│   │   ├── client.go
│   │   ├── auth.go
│   │   └── models.go
│   ├── store/
│   │   ├── sqlite.go
│   │   └── migrations/
│   ├── tmdb/
│   │   ├── client.go
│   │   ├── movie.go
│   │   ├── television.go
│   │   └── ranking.go
│   ├── torrentmeta/
│   │   ├── parser.go
│   │   ├── validation.go
│   │   └── models.go
│   └── web/
│       ├── handlers.go
│       ├── middleware.go
│       ├── templates/
│       └── static/
├── testdata/
│   ├── torrents/
│   └── tmdb/
├── .github/
│   └── workflows/
│       ├── test.yml
│       └── container.yml
├── config.example.yaml
├── compose.example.yaml
├── Dockerfile
├── go.mod
├── go.sum
├── LICENSE
└── README.md
```

There should be no `postprocessor`, `importer`, `renamer`, or completion-monitor package.

---

## 8. Configuration design

Use a YAML configuration file for non-secret settings.

```yaml
server:
  mode: "production"
  listen_address: "0.0.0.0:8787"
  public_url: "https://cineroute.example.com"
  trusted_proxy_cidrs:
    - "172.20.0.0/16"
  secure_cookies: true

auth:
  mode: "local"
  session_lifetime: "12h"

database:
  path: "/data/cineroute.sqlite"

intake:
  archive_torrents: false
  archive_directory: "/data/torrents"
  maximum_upload_bytes: 67108864
  maximum_bencode_depth: 64
  maximum_collection_items: 1000000
  maximum_files: 100000
  maximum_path_components: 64
  maximum_path_component_bytes: 255
  maximum_path_bytes: 4096
  maximum_trackers: 256
  maximum_tracker_url_bytes: 4096
  automatic_submit_threshold: 0.95
  require_confirmation: true

tmdb:
  language: "en-US"
  include_adult: false

qbittorrent:
  url: "http://qbittorrent:8080"
  movie_category: "cineroute-movie"
  tv_category: "cineroute-tv"
  automatic_torrent_management: false
  add_stopped: true
  start_after_verification: true

library:
  folder_format: "{title} ({year})"
  scan_depth: 1
  movie_duplicate_action: "review"
  existing_tv_insufficient_space_action: "block"

drives:
  - id: "hdd1"
    movie_root: "/m1"
    tv_root: "/t1"
    reserve_bytes: 107374182400

  - id: "hdd2"
    movie_root: "/m2"
    tv_root: "/t2"
    reserve_bytes: 107374182400

  - id: "hdd3"
    movie_root: "/m3"
    tv_root: "/t3"
    reserve_bytes: 107374182400

  - id: "hdd4"
    movie_root: "/m4"
    tv_root: "/t4"
    reserve_bytes: 107374182400
```

The `100 GiB` reserve values above are examples and should be configurable per drive.

### Secrets

Store secrets in environment variables or secret files, not `config.yaml`:

```env
CINEROUTE_QBIT_USERNAME_FILE=/run/secrets/qbittorrent_username
CINEROUTE_QBIT_PASSWORD_FILE=/run/secrets/qbittorrent_password
CINEROUTE_TMDB_API_KEY_FILE=/run/secrets/tmdb_api_key
CINEROUTE_AUTH_USERNAME_FILE=/run/secrets/cineroute_auth_username
CINEROUTE_AUTH_PASSWORD_HASH_FILE=/run/secrets/cineroute_auth_password_hash
CINEROUTE_SESSION_SECRET_FILE=/run/secrets/cineroute_session_secret
```

TMDB v3 application authentication supports either the `api_key` query parameter or an access token sent as a Bearer token. CineRoute can support both while preferring a Bearer token when supplied.

---

## 9. Torrent parsing

CineRoute must parse the uploaded `.torrent` before communicating with qBittorrent.

Extract:

```text
Torrent display name
Torrent version: v1, v2, or hybrid
Info hash or hashes
Total content size
Single-file or multi-file layout
Topology: single file, rooted file tree, or rootless BEP 52 file tree
All internal paths
Top-level root name when one is structurally present
File extensions
Trackers
Private flag when available
```

BitTorrent metainfo stores a suggested content name in the `info.name` field, and the metainfo `info` dictionary is also used when deriving the torrent info hash.

### Torrent validation

Reject torrents containing:

* Absolute paths.
* `..` path components.
* Empty path components, excluding the BEP 52 zero-length file-properties key, which is syntax rather than a path component.
* NUL characters.
* Invalid bencoding.
* No files.
* Files exceeding configured path lengths.
* A total size that cannot fit on any eligible drive.
* Duplicate paths after normalization.
* Duplicate bencode dictionary keys.
* Negative lengths, integer overflow, or a total length outside signed 64-bit bounds.
* Symbolic-link entries or any declared non-regular-file type other than recognized BEP 47 padding files, including `attr` values containing `l` or a `symlink path` field.
* Unsupported metainfo structures.

CineRoute should not extract or execute anything from the torrent. It only inspects metadata and forwards the original torrent bytes to qBittorrent.

Parsing must be bounded before allocating decoded structures. Enforce configurable limits on upload bytes, bencode nesting depth, total dictionary/list items, file count, path-component count, component length, full relative-path length, tracker count and tracker URL length. Reject a torrent as soon as a limit is exceeded; calculate lengths with checked arithmetic; and derive info hashes from the original byte span of the `info` dictionary rather than re-encoding it.

### Parser feasibility spike

Before building classification or submission, run a time-boxed spike against candidate pure-Go metainfo libraries. The spike must prove, with checked-in fixtures, that the chosen parser:

* Reads v1, BEP 52 v2, and hybrid torrents without changing the original bytes.
* Locates the exact raw `info` byte span for hash calculation.
* Distinguishes a single file, a v1/rooted multi-file tree, and a rootless BEP 52 file tree.
* Exposes symlink attributes and all path components so CineRoute can reject them.
* Allows all decoder allocations and recursion to be bounded.

If no library satisfies those requirements, implement a small bounded metainfo decoder for the required fields or wrap a library with a bounded pre-scan. Do not continue to qBittorrent integration with ambiguous topology semantics.

---

## 10. Movie and TV classification

Classification should use several signals rather than only the uploaded filename.

### TV indicators

Examples:

```regex
(?i)\bS\d{1,3}E\d{1,4}\b
(?i)\bS\d{1,3}\b
(?i)\b\d{1,3}x\d{1,4}\b
(?i)\bSeason[ ._-]*\d+\b
```

Additional signals:

* Multiple video files with sequential episode numbers.
* `Complete Series`.
* `Complete Season`.
* `Miniseries`.
* Episode ranges.
* Season ranges.

### Movie indicators

* A four-digit year near the title.
* One primary video file.
* No season or episode markers.
* Release tokens such as source, resolution, codec, HDR format, audio format, and release group.

### Ambiguous content

When confidence is insufficient, show:

```text
Content type:
[ Movie ] [ TV show ]
```

Do not submit an ambiguous torrent automatically.

---

## 11. Release-name cleaning

The parser should derive a search title, not a final folder name.

Example:

```text
Toy.Story.1995.REPACK.UHD.BluRay.2160p.TrueHD.Atmos.7.1.DV.HEVC.HYBRID.REMUX-FraMeSToR
```

Derived search input:

```text
Title: Toy Story
Year: 1995
Type: Movie
```

Example:

```text
Lost.S02.1080p.DSNP.WEB-DL.DDP5.1.H.264-FLUX
```

Derived search input:

```text
Title: Lost
Year: unknown
Season: 2
Type: TV
```

Tokens removed for searching can include:

* Resolution.
* Source.
* Video codec.
* Audio codec.
* Audio channel count.
* HDR/Dolby Vision indicators.
* Remux/encode indicators.
* Repack/proper indicators.
* Season and episode markers.
* Release group.
* Language tokens.
* Container extension.

The raw torrent name must remain untouched.

---

## 12. TMDB integration

Use the separate TMDB movie and TV search endpoints after CineRoute determines the likely media type.

TMDB's movie search endpoint searches original, translated, and alternative movie titles and supports a release-year parameter.

TMDB's TV search endpoint searches original, translated, and alternative TV names and exposes first-air-year filtering.

### Movie lookup

Request conceptually:

```http
GET /3/search/movie
    ?query=Toy%20Story
    &primary_release_year=1995
    &language=en-US
```

Canonical folder components:

```text
title = TMDB title
year  = year portion of release_date
```

Result:

```text
Toy Story (1995)
```

### TV lookup

Request conceptually:

```http
GET /3/search/tv
    ?query=Lost
    &language=en-US
```

Canonical folder components:

```text
title = TMDB name
year  = year portion of first_air_date
```

Result:

```text
Lost (2004)
```

### Candidate ranking

Rank results using:

1. Exact normalized title match.
2. Exact year match when a year was extracted.
3. Original-title match.
4. Alternative-title match.
5. Token similarity.
6. Search-result ordering as a final tie-breaker.

Popularity should not override a clearly better title/year match.

### Confirmation behavior

The review screen should show:

```text
Detected type: TV show
Torrent: Lost.S02.1080p.DSNP.WEB-DL.DDP5.1.H.264-FLUX
TMDB match: Lost
First aired: 2004
Canonical folder: Lost (2004)
Existing location: /t1/Lost (2004)
Torrent size: 68.4 GiB
qBittorrent save path: /t1/Lost (2004)
```

Initial releases should require confirmation.

A later optional mode can auto-submit only when:

* Classification confidence exceeds the configured threshold.
* Exactly one strong TMDB match exists.
* The destination is writable.
* Sufficient space exists.
* No duplicate torrent is already loaded.

---

## 13. Canonical folder naming

The canonical parent folder format is:

```text
{TMDB title} ({year})
```

Examples:

```text
Ben-Hur (1959)
Alice in Wonderland (1951)
Scary Movie 3 (2003)
Game of Thrones (2011)
Widow's Bay (2026)
Lord of the Flies (2026)
```

### Sanitization rules

Only sanitize characters that cannot be represented safely as a single Linux path component:

* Replace `/` with `-`.
* Remove NUL.
* Trim leading and trailing whitespace.
* Normalize Unicode to NFC.
* Prevent `.` and `..`.
* Collapse repeated whitespace.
* Do not remove apostrophes.
* Do not replace spaces with periods.
* Do not remove colons unless the configured filesystem requires it.
* Do not modify the torrent's internal names.

---

## 14. Existing-library lookup

Scan only the immediate children of each configured root.

Do not recursively scan every episode and movie file.

### TV lookup

For the canonical folder:

```text
Lost (2004)
```

Search:

```text
/t1/Lost (2004)
/t2/Lost (2004)
/t3/Lost (2004)
/t4/Lost (2004)
```

Rules:

1. No match:

   * Treat as a new TV show.
   * Select the drive with the most usable space.

2. Exactly one match:

   * Always use that drive.
   * Do not move a later season to another drive merely because another drive has more space.

3. Multiple matches:

   * Block automatic submission.
   * Show all matching locations.
   * Require the user to select one.
   * Record a warning because the show is split across drives.

### Movie lookup

Search the four movie roots for the canonical movie folder.

Rules:

1. No match:

   * Select the drive with the most usable space.

2. Existing movie folder:

   * Default to a duplicate warning.
   * Allow an explicit override to add the torrent into the existing movie folder.
   * Never silently create the same canonical movie folder on another drive.

This also supports alternate releases or replacements without forcing them onto a separate drive.

### Matching rules

Use the exact canonical folder name first.

A normalized comparison can handle:

* Unicode normalization.
* Repeated spaces.
* Case differences.
* Trailing spaces.
* Different apostrophe characters.

Do not use loose fuzzy matching to automatically decide that two library folders are the same title.

The index shown during review is advisory. The authoritative lookup is a fresh scan of every relevant movie or TV root while the allocation lock is held immediately before reservation and directory creation. If that final scan finds a newly created canonical folder, a second copy, or a destination that differs from the reviewed choice, stop and return the intake to destination review; do not create a directory or call qBittorrent from stale scan results.

---

## 15. Drive selection

For a new title, select the drive with the greatest usable available space.

Do not treat `/m1` and `/t1` as separate capacity pools. They share `hdd1`.

### Basic formula

```text
usable space =
    filesystem available bytes
  - configured reserve
  - committed incomplete torrent bytes
  - pending CineRoute reservations
```

### qBittorrent commitments

Free-space reporting might not reflect the entire eventual size of every incomplete torrent. Before choosing a drive, CineRoute should query qBittorrent and sum the remaining bytes for incomplete torrents whose save paths begin with that drive's movie or TV root.

qBittorrent's torrent-list response includes fields such as `amount_left`, `save_path`, `state`, `total_size`, and `auto_tmm`, making this calculation possible.

Conceptually:

```go
usable := availableBytes -
    reserveBytes -
    incompleteTorrentBytes -
    localPendingReservations
```

### Selection algorithm

```text
Acquire allocation lock

Refresh filesystem statistics

Query qBittorrent incomplete torrents

Group incomplete remaining bytes by physical drive

Add pending CineRoute reservations

Discard drives where:
    usable space < torrent size

Select drive with maximum usable space

Run the final under-lock library rescan and duplicate-torrent check

Record a leased temporary reservation in a short SQLite transaction

Create the parent, add stopped, verify, and start

Convert the reservation to a qBittorrent commitment by deleting it only after start is accepted
```

Reservation rows are crash-recoverable leases, not permanent capacity deductions. Each row has an owner token and expiry time; the active process refreshes the lease while a submission is in progress. A retry for the same intake must reuse its existing reservation instead of double-counting it.

### Existing TV show with insufficient space

If an existing TV show is on `hdd1` but `hdd1` cannot safely hold the new torrent:

* Do not silently use another HDD.
* Do not split the show automatically.
* Block submission.
* Show the shortage.
* Offer an explicit manual drive override only if the user accepts splitting the show.

Example:

```text
Lost (2004) already exists on hdd1.

Required: 184 GiB
Usable on hdd1: 122 GiB
Shortfall: 62 GiB

Automatic submission blocked.
```

---

## 16. qBittorrent integration

CineRoute should talk directly to qBittorrent's WebUI API.

It should not send requests through QUI.

qBittorrent's current WebUI API supports uploading raw torrent files and supplying a per-torrent download folder through `savepath`. It also supports category, tags, paused state, root-folder behavior, torrent renaming, and Automatic Torrent Management options.

### Authentication

CineRoute should:

1. POST credentials to:

   ```text
   /api/v2/auth/login
   ```

2. Store the returned `SID` cookie.

3. Reauthenticate when the session expires.

4. Send an `Origin` or `Referer` matching the qBittorrent host and port when required by qBittorrent's WebUI protection.

The qBittorrent API documentation requires the `Origin` or `Referer` domain and port to match the HTTP `Host` when those checks are active.

### Compatibility and readiness gate

The MVP targets qBittorrent `5.0+` and Web API `2.11+`; it should not silently fall back to older pause/resume or layout behavior. After authenticating, call:

```text
GET /api/v2/app/version
GET /api/v2/app/webapiVersion
GET /api/v2/app/preferences
GET /api/v2/torrents/categories
```

Parse versions semantically and fail readiness on an untested qBittorrent major version or an API below the tested minimum. Maintain a small compatibility test matrix so a future qBittorrent major is enabled deliberately rather than assumed compatible.

For the MVP, require these qBittorrent preferences:

```text
preallocate_all = false
temp_path_enabled = false
```

Preallocation would make filesystem free-space accounting overlap with CineRoute's remaining-byte commitments, and an incomplete-torrent directory would violate direct-to-HDD placement. Report the current values and remediation in `/api/status`; do not change global qBittorrent preferences automatically.

Ensure `cineroute-movie` and `cineroute-tv` exist. CineRoute may create a missing category with an empty `savePath`, then must read it back. If either existing category has a non-empty save path, fail readiness and refuse submissions rather than editing an administrator's category. Repeat the version, preference and category gate immediately before a submission if the cached check is stale.

### Add request

Example:

```http
POST /api/v2/torrents/add
Content-Type: multipart/form-data
Cookie: SID=...
```

Fields:

```text
torrents = original uploaded .torrent bytes
savepath = /t1/Lost (2004)
category = cineroute-tv
tags     = cineroute,cineroute-<request-id>,tmdb-4607,hdd1
autoTMM  = false
paused   = true
root_folder = <explicit true or false from parsed topology>
```

The qBittorrent 5 Web API calls the add field `paused`; CineRoute uses it to add the torrent in qBittorrent's stopped state. No payload may start before verification. Do not rely on qBittorrent's global start-stopped preference.

For a movie:

```text
savepath = /m4/Toy Story (1995)
category = cineroute-movie
tags     = cineroute,cineroute-<request-id>,tmdb-862,hdd4
```

### Deterministic content layout

Never leave `root_folder` unset. Derive it from the parser's structural topology, not from a filename heuristic:

| Parsed topology | `root_folder` | Expected `content_path` |
| --------------- | ------------- | ----------------------- |
| Single file | `false` | `<savepath>/<original filename>` |
| Rooted v1, v2, or hybrid multi-file tree | `true` | `<savepath>/<original structural root>` |
| Rootless BEP 52 multi-file tree | `false` | `<savepath>` |

For a hybrid torrent, the v1 and v2 path views must describe the same topology and files; reject the torrent if they disagree. A BEP 52 `info.name` is not by itself proof of a structural root. The parser must retain the structural-root decision and the exact expected qBittorrent-relative file list in the intake record so the review and submission paths cannot reinterpret it differently.

The real-qBittorrent compatibility suite is the authority for this mapping. If a supported qBittorrent version reports a different `content_path` or file tree for any topology fixture, mark that version unsupported; never compensate by renaming or moving content.

### Field that must be omitted

Do not send:

```text
rename
```

Omitting `rename` preserves the torrent name.

### Automatic Torrent Management

Always submit with:

```text
autoTMM=false
```

This protects the explicitly chosen `savepath` from category-based relocation behavior. qBittorrent exposes settings that can relocate torrents after category or category-save-path changes, so CineRoute should make each submitted torrent manually managed.

The categories should therefore be used only for organization and filtering:

```text
cineroute-movie
cineroute-tv
```

They should not have category save paths configured in qBittorrent.

### Stopped-add verification and start

The add endpoint reports `200` for most scenarios, so CineRoute should verify the result immediately rather than treating the HTTP status alone as proof.

Each add request should include a unique tag:

```text
cineroute-550e8400-e29b-41d4-a716-446655440000
```

Then query:

```text
GET /api/v2/torrents/info?tag=cineroute-550e8400-e29b-41d4-a716-446655440000
```

qBittorrent supports filtering the torrent list by tag or hash.

Use a short bounded retry because the add endpoint can return before the torrent appears in the list. Once the unique tag resolves, query `/api/v2/torrents/files?hash=<hash>` as well.

Verify:

```text
Exactly one torrent is returned
Returned hash is compatible with the parsed v1/v2 hashes
Returned save_path equals the expected path
Returned content_path equals the topology-derived expected content path
Returned category equals the expected category
Returned auto_tmm is false
Returned total_size matches the parsed torrent size
Returned state is stopped
Returned file names, sizes, and count exactly match the parsed payload-relative file list
```

Path comparisons may normalize only redundant trailing separators; they must not case-fold, resolve symlinks, or normalize away a changed component. This check is what proves that single-file, rooted multi-file, and rootless BEP 52 content will land under the canonical parent exactly as previewed.

Only after every check passes, call:

```http
POST /api/v2/torrents/start
hashes=<verified-hash>
```

Then re-read that exact hash with a bounded retry and require that it has left the stopped state. Only then delete the local reservation and mark the intake `submitted`. CineRoute performs no completion polling after that point.

CineRoute does not continue polling that torrent.

### Failure rollback

If qBittorrent does not confirm the add or any verification differs:

1. Never start the torrent.
2. Record the expected and actual values and mark the intake `needs_recovery` when the uniquely tagged torrent exists, otherwise `failed`.
3. Release the local reservation once the torrent is known to qBittorrent, because its `amount_left` is then counted as a qBittorrent commitment. If no torrent exists, release the reservation immediately.
4. Delete the canonical folder only when:

   * CineRoute created it during this request.
   * No qBittorrent torrent points to it.
   * It is still empty.
5. Never delete a pre-existing media folder or torrent payload.

Leave an unverified torrent stopped for recovery or explicit removal through QUI. A recovery pass may repeat the same tag/hash verification and start the exact torrent if all invariants now pass; it must never submit a second copy speculatively.

---

## 17. Duplicate torrent detection

Before submitting:

1. Calculate the torrent's supported info hash or hashes.
2. Query qBittorrent for an existing matching torrent.
3. Check CineRoute's own submission history.
4. Block duplicate submission by default.
5. Allow a manual override only when there is a clear reason.

Archiving is disabled by default because private `.torrent` metadata can contain tracker passkeys. If the administrator explicitly enables it, archive the original uploaded bytes under:

```text
/data/torrents/<intake-id>.torrent
```

This archive resides on `/volume2`, contains only small torrent metadata files, and does not contain the media payload. Create the archive directory as `0700` and files atomically as `0600`, owned by UID `1001`; never expose them through a static-file route, include them in logs, or archive rejected uploads. Document retention and deletion explicitly.

---

## 18. QUI interaction

QUI requires no special integration.

QUI is a web interface for managing qBittorrent instances, while CineRoute will independently use qBittorrent's API.

Both tools will operate on the same qBittorrent instance:

```text
Browser → QUI ───────────────┐
                             ├──→ qBittorrent
Browser → CineRoute → API ───┘
```

Torrents added by CineRoute should appear normally in QUI with:

* Their original torrent names.
* Their selected save paths.
* `cineroute-movie` or `cineroute-tv` category.
* CineRoute, TMDB, and drive tags.

Manual changes made in QUI are treated as intentional user overrides.

CineRoute should not:

* Revert manual QUI changes.
* Reassign paths after submission.
* Restore deleted torrents.
* Reapply categories.
* Continuously reconcile qBittorrent state.

The existing media mounts in QUI do not affect CineRoute's design.

---

## 19. Frontend plan

### Main page

```text
┌──────────────────────────────────────────┐
│                 CineRoute                │
├──────────────────────────────────────────┤
│                                          │
│       Drop a .torrent file here          │
│                                          │
│              [ Browse ]                  │
│                                          │
└──────────────────────────────────────────┘
```

### Analysis page

Show:

```text
Torrent name
Torrent size
Topology: single file, rooted multi-file, or rootless multi-file
Likely media type
Extracted search title
Extracted year
Season number when detected
Structural torrent root when present
Planned qBittorrent root-folder policy
```

### TMDB result selection

Show up to five results:

```text
1. Lost (2004)
2. Lost (2021)
3. Lost Ollie (2022)
```

Poster images should be optional and lazy-loaded. Text-only operation should remain fully usable.

### Destination preview

```text
Media type: TV show
TMDB: Lost
Year: 2004
Existing show: Yes
Selected drive: hdd1
Parent folder: /t1/Lost (2004)
Torrent root: Lost.S02.1080p.DSNP.WEB-DL.DDP5.1.H.264-FLUX
Expected content path: /t1/Lost (2004)/Lost.S02.1080p.DSNP.WEB-DL.DDP5.1.H.264-FLUX
Available after reservation: 2.14 TiB
```

Buttons:

```text
[ Add to qBittorrent ]
[ Change TMDB match ]
[ Override destination ]
[ Cancel ]
```

### Success page

```text
Torrent added successfully

qBittorrent name:
Lost.S02.1080p.DSNP.WEB-DL.DDP5.1.H.264-FLUX

Save path:
/t1/Lost (2004)

Content path:
/t1/Lost (2004)/Lost.S02.1080p.DSNP.WEB-DL.DDP5.1.H.264-FLUX

Category:
cineroute-tv

Drive:
hdd1

CineRoute will perform no further operations on this torrent.
```

---

## 20. Internal HTTP API

Suggested endpoints:

```text
GET  /                         Main interface
GET  /health                   Container health check
GET  /api/status               qBittorrent, TMDB, database and drive status
POST /api/intakes              Upload and parse .torrent
GET  /api/intakes/{id}         Retrieve analysis
POST /api/intakes/{id}/type    Override movie/TV classification
POST /api/intakes/{id}/match   Select TMDB result
POST /api/intakes/{id}/drive   Override selected drive
POST /api/intakes/{id}/submit  Add to qBittorrent
GET  /api/history              Submission history
```

There is deliberately no endpoint such as:

```text
/api/import
/api/rename
/api/postprocess
/api/completed
```

---

## 21. State model

Keep the state model limited to the intake process:

```text
uploaded
parsed
needs_type_review
searching_tmdb
needs_match_review
matched
needs_destination_review
ready
submitting
verifying
starting
needs_recovery
submitted
duplicate
failed
cancelled
```

`submitted` is terminal and means that the stopped torrent passed verification, qBittorrent accepted the explicit start request, and the exact hash left the stopped state. `needs_recovery` is an intake-saga state, not a download-monitoring state.

There are no states for:

```text
downloading
completed
seeding
organizing
importing
renaming
```

---

## 22. SQLite data model

### `intakes`

```sql
CREATE TABLE intakes (
    id TEXT PRIMARY KEY,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,

    original_filename TEXT NOT NULL,
    torrent_name TEXT NOT NULL,
    torrent_size INTEGER NOT NULL,
    torrent_kind TEXT NOT NULL,
    torrent_topology TEXT,
    structural_root TEXT,
    root_folder INTEGER CHECK (root_folder IN (0, 1)),

    info_hash_v1 TEXT,
    info_hash_v2 TEXT,

    detected_media_type TEXT,
    selected_media_type TEXT,

    extracted_title TEXT,
    extracted_year INTEGER,
    detected_season INTEGER,

    tmdb_id INTEGER,
    tmdb_title TEXT,
    tmdb_year INTEGER,

    drive_id TEXT,
    parent_folder TEXT,
    save_path TEXT,
    expected_content_path TEXT,
    parent_folder_created INTEGER NOT NULL DEFAULT 0
        CHECK (parent_folder_created IN (0, 1)),

    qbittorrent_hash TEXT,
    qbittorrent_category TEXT,
    verification_tag TEXT,

    status TEXT NOT NULL,
    error_message TEXT
);
```

### `torrent_files`

```sql
CREATE TABLE torrent_files (
    intake_id TEXT NOT NULL,
    file_index INTEGER NOT NULL,
    relative_path TEXT NOT NULL,
    length INTEGER NOT NULL CHECK (length >= 0),
    PRIMARY KEY (intake_id, file_index),
    UNIQUE (intake_id, relative_path),
    FOREIGN KEY (intake_id) REFERENCES intakes(id)
);
```

Persist the validated structural topology, explicit `root_folder` decision, expected content path, and exact file list before review. Recovery must compare qBittorrent against these stored values, not reparse user-controlled form fields or infer layout again.

### `allocation_reservations`

```sql
CREATE TABLE allocation_reservations (
    intake_id TEXT PRIMARY KEY,
    drive_id TEXT NOT NULL,
    reserved_bytes INTEGER NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    lease_owner TEXT NOT NULL,
    lease_expires_at TEXT NOT NULL,
    FOREIGN KEY (intake_id) REFERENCES intakes(id)
);
```

### `service_leases`

```sql
CREATE TABLE service_leases (
    name TEXT PRIMARY KEY,
    owner_id TEXT NOT NULL,
    acquired_at TEXT NOT NULL,
    heartbeat_at TEXT NOT NULL,
    expires_at TEXT NOT NULL
);
```

The MVP supports exactly one active CineRoute process for a database. At startup, acquire the singleton lease in an immediate SQLite transaction and renew it periodically. Refuse readiness while another unexpired owner holds it. A replacement may take over only after expiry, then run recovery before accepting traffic. This makes an accidental second container fail closed rather than relying only on an in-process mutex.

### `settings`

Only needed if settings become editable through the frontend.

Secrets should not be stored in the database.

---

## 23. Docker Compose deployment

### Recommended external network

If qBittorrent and CineRoute are in the same Compose project, the service name `qbittorrent` works automatically on the default network.

If they are separate Compose projects, connect both to an external bridge network. Docker Compose services on a shared network can discover each other by service name, and Docker recommends names rather than container IP addresses because IPs can change.

Create the network once:

```fish
docker network create media-control
```

### CineRoute service

```yaml
services:
  cineroute:
    image: ghcr.io/YOUR_GITHUB_USERNAME/cineroute:latest
    container_name: cineroute
    user: "1001:10"

    environment:
      TZ: America/Asuncion
      CINEROUTE_CONFIG: /config/config.yaml

    env_file:
      - /volume2/docker/cineroute/config/cineroute.env

    volumes:
      - /volume2/docker/cineroute/config:/config:ro
      - /volume2/docker/cineroute/secrets:/run/secrets:ro
      - /volume2/docker/cineroute/data:/data:rw

      - /volume1/hdd1/movies:/m1
      - /volume3/hdd2/movies:/m2
      - /volume4/hdd3/movies:/m3
      - /volume5/hdd4/movies:/m4

      - /volume1/hdd1/tv:/t1
      - /volume3/hdd2/tv:/t2
      - /volume4/hdd3/tv:/t3
      - /volume5/hdd4/tv:/t4

    ports:
      - "127.0.0.1:8787:8787"

    networks:
      - media-control

    read_only: true
    tmpfs:
      - /tmp:rw,noexec,nosuid,nodev,size=16m
    cap_drop:
      - ALL
    security_opt:
      - no-new-privileges:true
    init: true
    restart: unless-stopped

networks:
  media-control:
    external: true
```

Run exactly one replica; the singleton database lease intentionally rejects a second active container. The loopback port assumes an HTTPS reverse proxy on the host. If the reverse proxy is another container on `media-control`, remove `ports`, use `expose: ["8787"]`, and do not publish CineRoute directly to the LAN.

### Environment file

Host path:

```text
/volume2/docker/cineroute/config/cineroute.env
```

Contents:

```env
CINEROUTE_QBIT_USERNAME_FILE=/run/secrets/qbittorrent_username
CINEROUTE_QBIT_PASSWORD_FILE=/run/secrets/qbittorrent_password
CINEROUTE_TMDB_API_KEY_FILE=/run/secrets/tmdb_api_key
CINEROUTE_AUTH_USERNAME_FILE=/run/secrets/cineroute_auth_username
CINEROUTE_AUTH_PASSWORD_HASH_FILE=/run/secrets/cineroute_auth_password_hash
CINEROUTE_SESSION_SECRET_FILE=/run/secrets/cineroute_session_secret
```

Every secret setting should support a `_FILE` form. Reject configurations that supply both the direct value and `_FILE`, and trim only the single trailing newline added by a text editor; do not trim meaningful surrounding secret characters.

Set restrictive permissions:

The environment file contains paths rather than secret values, but still restrict it to `0640` and ownership `1001:10`.

### Required host directories and files

```fish
sudo install -d -o 1001 -g 10 -m 0750 /volume2/docker/cineroute/config
sudo install -d -o 1001 -g 10 -m 0750 /volume2/docker/cineroute/data
sudo install -d -o 1001 -g 10 -m 0700 /volume2/docker/cineroute/secrets
sudo install -o 1001 -g 10 -m 0640 config.example.yaml /volume2/docker/cineroute/config/config.yaml
sudo install -o 1001 -g 10 -m 0640 /dev/null /volume2/docker/cineroute/config/cineroute.env
```

Edit both non-secret files explicitly; mounting a directory does not create `config.yaml`:

```fish
micro /volume2/docker/cineroute/config/config.yaml
micro /volume2/docker/cineroute/config/cineroute.env
```

Create each file named in `cineroute.env` as UID `1001`, GID `10`, mode `0600`. Generate the session secret with at least 32 random bytes and generate the password hash with CineRoute's Argon2id `hash-password` subcommand; store the hash, never the cleartext CineRoute password. Enter the qBittorrent username/password and TMDB key into their separate secret files. Verify ownership and modes before `docker compose up`; startup must fail if a secret file is group/world-readable, missing, empty, or not a regular file.

Do not create `/data/torrents` unless archival is explicitly enabled. If enabled, create it with ownership `1001:10` and mode `0700`.

The NAS ACLs on all eight media roots must grant UID `1001`/GID `10` traversal, directory creation and writes, and qBittorrent must see the same aliases. Validate this without recursively changing existing media ownership or permissions.

CineRoute does not need these mounts:

```text
/downloads
/music
/videos
/books
/torrents
```

Uploaded torrent metadata can be kept under `/data/torrents` on volume 2.

The actual movie or TV payload is written directly by qBittorrent to `/m1`–`/m4` or `/t1`–`/t4`.

---

## 24. qBittorrent settings

Required qBittorrent configuration for the MVP:

```text
qBittorrent:
5.0 or newer within the tested 5.x compatibility range

Web API:
2.11 or newer within the tested compatibility range

Default Torrent Management Mode:
Manual

Pre-allocate disk space for all files:
Disabled

Keep incomplete torrents in:
Disabled globally
```

A global incomplete-torrent directory on volume 2 would defeat the direct-to-HDD design and cause payload writes to the SSD. Preallocation must also remain disabled so CineRoute does not subtract both allocated filesystem blocks and qBittorrent `amount_left`. These are readiness requirements, not suggestions, until the allocation model explicitly supports either feature.

CineRoute explicitly supplies `root_folder`, adds stopped, and sets `autoTMM=false` for every request, so qBittorrent's global subfolder, start and Automatic Torrent Management defaults are not used for layout decisions.

Categories:

```text
cineroute-movie
cineroute-tv
```

Both categories must have an empty save path. CineRoute refuses readiness if an administrator later assigns one.

Tags are preferred for additional metadata:

```text
cineroute
tmdb-862
hdd4
cineroute-<request-id>
```

---

## 25. Concurrency and consistency

CineRoute must serialize drive allocation and submission preparation.

Without locking, two simultaneous uploads could both observe the same free-space values and select the same drive.

Use:

* A database-backed singleton service lease for the whole instance.
* A process-level allocation mutex.
* Short SQLite transactions; never keep a database transaction open across HTTP or filesystem calls.
* Leased temporary reservation rows with an owner token and expiry.
* A final filesystem-space refresh immediately before submission.

Sequence:

```text
Lock
  ↓
Refresh the qBittorrent version, preference, and category readiness gate
  ↓
Rescan all relevant library roots and recheck canonical-folder duplicates
  ↓
Recheck the torrent's info hashes and unique intake tag in qBittorrent
  ↓
Refresh drive space
  ↓
Refresh qBittorrent commitments
  ↓
Expire or reconcile stale reservations, then sum live reservations
  ↓
Select drive
  ↓
Write or renew this intake's leased reservation
  ↓
Create parent folder
  ↓
Add torrent stopped
  ↓
Verify hash, stopped state, save path, content path, file tree, category and `auto_tmm`
  ↓
Start the verified hash and confirm it left the stopped state
  ↓
Delete reservation
Unlock
```

TMDB searches and user review do not need to hold the allocation lock.

The destination should be recalculated immediately before submission because the library and free space may have changed while the review page was open. If the authoritative rescan changes an existing-title decision or exposes a duplicate, release the lock and require review rather than silently changing the confirmed destination.

### Crash recovery

Acquire the singleton service lease and run recovery before the HTTP server becomes ready. For every nonterminal `submitting`, `verifying`, `starting`, or `needs_recovery` intake and every expired reservation:

1. Look up qBittorrent by the unique intake tag and parsed info hashes.
2. If exactly one torrent exists, treat qBittorrent's `amount_left` as the commitment, remove the duplicate local reservation, and repeat verification. Start it only if every stored invariant still matches.
3. If no torrent exists and the lease expired, release the reservation and remove only an empty directory proven to have been created by that intake; return the intake to a safe retryable state.
4. If the outcome is ambiguous or mismatched, leave any torrent stopped, mark `needs_recovery`, and require explicit operator resolution.

Recovery and submit are idempotent by intake ID, unique tag, and info hash. Never issue a second add after a timeout until reconciliation proves the first add did not reach qBittorrent. Refresh active reservation and singleton leases with bounded heartbeats; losing the singleton lease makes the process unready and disables new mutations.

---

## 26. Error handling

### TMDB unavailable

* Keep the parsed intake.
* Show retry.
* Allow manual title, year, and media type entry.
* Do not submit without a confirmed canonical folder.

### qBittorrent unavailable

* Keep a pre-add intake in `ready` and release its reservation.
* If an add request may have reached qBittorrent, keep the intake in `needs_recovery` until tag/hash reconciliation determines the outcome.
* Allow retry.
* Never retry the add until absence is proven.

### Drive unavailable

* Mark the drive unhealthy.
* Exclude it from new-title allocation.
* Existing TV shows on that drive should be blocked rather than silently redirected.

### Parent directory cannot be created

* Do not call qBittorrent.
* Return the exact filesystem error.
* Show UID/GID and target path in diagnostics.

### Duplicate canonical TV folders

Example:

```text
/t1/Lost (2004)
/t3/Lost (2004)
```

* Require manual selection.
* Do not pick based on free space.

### Duplicate movie

* Show existing path.
* Default to cancel.
* Allow explicit use of the existing folder.

### qBittorrent accepted wrong path

* Mark the intake `needs_recovery`.
* Leave the torrent stopped.
* Do not move the torrent automatically.
* Show the expected and actual paths.
* Let the user correct or remove it through QUI.

---

## 27. Security requirements

* Treat `.torrent` uploads as untrusted input.
* Limit upload size.
* Validate bencoding depth and collection sizes.
* Apply HTTP request timeouts.
* Apply TMDB and qBittorrent client timeouts.
* Do not expose qBittorrent credentials to the browser.
* Escape all torrent names and file paths in HTML.
* Use CSRF protection for state-changing frontend actions.
* Use SameSite cookies.
* Require authentication for every page and API except `/health`; the MVP has no unauthenticated mode.
* Hash local passwords with Argon2id, rate-limit login attempts, rotate the session ID after login, and require a separate high-entropy session secret file.
* In `production` mode, require an HTTPS `public_url`, `Secure`, `HttpOnly`, and `SameSite` session cookies, and a configured trusted reverse-proxy CIDR. Reject untrusted forwarded headers and fail startup if secure-cookie or secret requirements are missing.
* Permit `development` mode only with a loopback listen address; authentication remains required, forwarded headers are ignored, and startup must refuse a non-loopback bind.
* Do not log qBittorrent passwords or TMDB credentials.
* Do not include tracker passkeys in normal logs.
* Redact tracker URLs when they may contain private passkeys.
* Run as UID `1001`, GID `10`, not root.
* Use a read-only container root filesystem where practical.
* Give the container only the media mounts it needs.
* Default `.torrent` archival to off; when enabled, enforce the private `0700` directory and `0600` file policy from section 17.

---

## 28. Observability

Log structured events to stdout:

```json
{
  "event": "torrent_submitted",
  "intake_id": "550e8400-e29b-41d4-a716-446655440000",
  "media_type": "tv",
  "tmdb_id": 4607,
  "canonical_folder": "Lost (2004)",
  "drive": "hdd1",
  "save_path": "/t1/Lost (2004)",
  "torrent_size": 73443940762
}
```

Never log:

* qBittorrent passwords.
* TMDB credentials.
* Raw cookies.
* Full private tracker URLs.
* Tracker passkeys.

### Health endpoint

`GET /health` should report process health without requiring external dependencies.

`GET /api/status` can report:

```text
Database: healthy
TMDB: reachable
qBittorrent: authenticated
qBittorrent version/API: supported
qBittorrent preallocation: disabled
qBittorrent temporary path: disabled
qBittorrent categories: valid, empty save paths
hdd1: healthy
hdd2: healthy
hdd3: healthy
hdd4: healthy
```

---

## 29. Docker image design

Use a multi-stage Dockerfile.

### Builder stage

* Download Go modules.
* Run tests.
* Compile a static Linux binary.
* Embed frontend assets and timezone data.
* Set version information from build arguments.

### Runtime stage

The runtime image should contain:

* CineRoute binary.
* CA certificates for TMDB HTTPS.
* No compiler.
* No package manager.
* No shell unless deliberately required.
* Non-root user.

The binary can expose a health-check subcommand:

```text
/cineroute healthcheck
```

This avoids depending on `curl` or `wget` inside a minimal image.

### Image architectures

Build:

```text
linux/amd64
linux/arm64
```

Docker's official GitHub Actions documentation supports multi-platform image builds with Buildx and the `platforms` option.

---

## 30. GitHub Actions and GHCR

GitHub is the authoritative source repository. Local clones keep the GitHub URL as `origin`; pull requests, branch protection, releases, CI status, and release tags are managed on GitHub. GitHub Actions remains the only CI/release system, and GHCR remains the only published container registry.

Create the empty `cineroute` repository on GitHub, then configure the local repository with one of these canonical origins:

```fish
# SSH, recommended after adding the workstation key to GitHub
git remote add origin git@github.com:YOUR_GITHUB_USERNAME/cineroute.git

# Or HTTPS
git remote add origin https://github.com/YOUR_GITHUB_USERNAME/cineroute.git
```

Use only one `git remote add origin` command. Verify it with `git remote -v`, then make the first push with `git push -u origin main`. Forgejo must not replace this remote.

### Pull-request workflow

Run on pull requests:

```text
go test ./...
go vet ./...
static analysis
Docker build without push
```

### Release workflow

Run on version tags:

```text
v0.1.0
v0.2.0
v1.0.0
```

Build and publish:

```text
ghcr.io/YOUR_GITHUB_USERNAME/cineroute:0.1.0
ghcr.io/YOUR_GITHUB_USERNAME/cineroute:0.1
ghcr.io/YOUR_GITHUB_USERNAME/cineroute:0
ghcr.io/YOUR_GITHUB_USERNAME/cineroute:latest
```

Also publish a commit tag:

```text
ghcr.io/YOUR_GITHUB_USERNAME/cineroute:sha-abcdef1
```

Generate and publish a provenance attestation for the pushed multi-platform image digest. GitHub Actions can publish to GHCR using the repository's `GITHUB_TOKEN` with `packages: write`; GitHub's recommended workflow uses `docker/login-action`, `docker/metadata-action`, `docker/build-push-action`, and `actions/attest-build-provenance`.

### Workflow permissions

```yaml
permissions:
  contents: read
  packages: write
  attestations: write
  id-token: write
```

### OCI labels

Add:

```text
org.opencontainers.image.title=CineRoute
org.opencontainers.image.description=Route movie and TV torrents directly into a multi-drive media library
org.opencontainers.image.source=<repository>
org.opencontainers.image.revision=<commit>
org.opencontainers.image.version=<version>
org.opencontainers.image.licenses=<license>
```

GitHub uses the `org.opencontainers.image.source` label to associate an image with its source repository.

---

## 31. Forgejo 15 pull-mirror backup

Forgejo 15 is a disaster-recovery pull mirror only, not a second development forge. Create it from the Forgejo web interface before any repository with the same owner/name exists:

1. Open the top-right **Create** menu and select **New Migration**.
2. Select **GitHub**, enter the canonical GitHub repository URL, and choose the backup owner/name.
3. Check **This repository will be a mirror**.
4. For a public GitHub repository, do not supply a credential. For a private repository, use a dedicated fine-grained GitHub token limited to this one repository with read-only repository-contents access; grant no write, workflow, package, administration, issue, or pull-request scopes.
5. Select **Migrate repository**, then use **Synchronize Now** once and verify the default branch, all branches, tags, and representative commit IDs against GitHub.
6. Leave periodic pull synchronization enabled and monitor its last-success timestamp. Test that a fresh clone from Forgejo contains the expected refs as part of the backup drill.

The mirror synchronizes Git commits, branches, and tags from GitHub to Forgejo. GitHub-only pull requests, Actions runs, release assets, packages, attestations, and registry images are outside this Git mirror and must not be treated as backed up by it.

Do not push development changes to Forgejo, convert the mirror to a normal repository, configure a push mirror back to GitHub, or change local `origin` away from GitHub. Do not enable Forgejo Actions, install a Forgejo runner, duplicate workflow execution, or publish a Forgejo container package. Release builds and images continue to come only from GitHub Actions and `ghcr.io`.

The existing Forgejo 15 Compose service needs no Runner, Actions, registry, or additional port for this role. Mirror data is stored below Forgejo's `/data` mount, which is `/volume2/docker/forgejo/data` on the NAS. The separate `/volume1/hdd1/backup/forgejo:/backup` bind mount does not create backups by itself. If a second physical copy of Forgejo is desired, schedule a consistent Forgejo dump or NAS snapshot into `/backup`, apply a retention policy, and test restoration separately from the Git pull-mirror check.

---

## 32. Implementation phases

### Phase 1 — Project foundation

Deliver:

* Go application.
* Configuration loading.
* Required local authentication and secure production/development mode validation.
* Structured logging.
* SQLite migrations.
* Singleton and reservation lease primitives.
* Embedded frontend assets.
* Health endpoint.
* Dockerfile.
* Compose example.

Acceptance:

* Container runs as `1001:10`.
* Config and database are on `/volume2`.
* All eight media roots are visible.
* Drive pairs are validated.
* Incorrect secret ownership/modes and a second active instance fail readiness.

### Phase 2 — Torrent parsing

Deliver:

* Bounded-parser feasibility spike.
* `.torrent` upload.
* Metainfo parser.
* Total-size calculation.
* File-tree display.
* Info-hash calculation.
* Path-security validation.
* Structural topology model and deterministic `root_folder` policy.
* Movie/TV classification.
* Search-title extraction.

Acceptance:

* No data is sent to qBittorrent during analysis.
* Original torrent bytes remain unchanged.
* Invalid paths, symlinks, malformed metainfo, arithmetic overflow, and configured-limit violations are rejected.
* v1, rooted and rootless v2, and hybrid fixtures are tested with exact raw info hashes.

### Phase 3 — TMDB matching

Deliver:

* Movie search.
* TV search.
* Candidate ranking.
* Manual match selection.
* Manual title/year fallback.
* Canonical folder preview.

Acceptance:

* `Toy.Story.1995...` resolves to `Toy Story (1995)`.
* `Lost.S02...` resolves to `Lost (2004)`.
* Ambiguous titles require confirmation.

### Phase 4 — Library indexing

Deliver:

* Immediate-child directory scans.
* Canonical folder matching.
* Duplicate detection.
* Existing-TV placement.
* Existing-movie warning.
* Drive health checks.
* Authoritative under-lock rescan.

Acceptance:

* Existing `Lost (2004)` is found regardless of current free-space ranking.
* Split shows are detected and not silently routed.
* A folder created after review is detected before reservation or submission.

### Phase 5 — Allocation

Deliver:

* Filesystem free-space reader.
* Per-drive reserve.
* qBittorrent incomplete-byte accounting.
* Pending allocation records.
* Concurrency lock.
* Reservation expiry, singleton enforcement, and startup recovery.
* Manual override.

Acceptance:

* New titles choose the drive with the most usable space.
* Multiple simultaneous submissions cannot overbook one drive.
* Existing TV shows remain on their existing drive.
* A crash at each submission boundary neither leaks capacity forever nor causes a speculative duplicate add.

### Phase 6 — qBittorrent submission

Deliver:

* Authentication.
* Session renewal.
* qBittorrent application/Web API version gate.
* Preference and empty-category readiness checks.
* Multipart torrent upload.
* Explicit save path.
* Explicit topology-derived `root_folder`.
* Categories and tags.
* `autoTMM=false`.
* Stopped add, exact content/file-tree verification, then explicit start.
* Empty-folder rollback.

Acceptance:

* qBittorrent receives the exact expected save path.
* Single-file movie filenames remain unchanged.
* Multi-file torrent folders remain unchanged.
* Rootless BEP 52 torrents remain rootless beneath the canonical parent.
* No payload starts before verification, and CineRoute stops processing after the verified start.

### Phase 7 — GitHub release pipeline

Deliver:

* Tests on pull requests.
* Multi-platform Docker build.
* GHCR publishing.
* Semantic-version tags.
* OCI labels.
* README deployment instructions.

Acceptance:

```fish
docker pull ghcr.io/YOUR_GITHUB_USERNAME/cineroute:latest
```

works on supported NAS architecture.

### Phase 8 — Forgejo backup mirror

Deliver:

* Forgejo 15 pull mirror created through **New Migration** from the GitHub repository.
* Periodic mirror synchronization and failure visibility.
* Written restore/check procedure for commits, branches, and tags.

Acceptance:

* GitHub remains local `origin` and the authoritative forge.
* A manual **Synchronize Now** reproduces representative GitHub branch and tag commit IDs in Forgejo.
* No Forgejo Action, runner, release pipeline, or registry publication is configured.

---

## 33. Test plan

### Unit tests

Test:

* Release-name tokenization.
* Movie versus TV classification.
* Year extraction.
* Season extraction.
* Canonical folder sanitization.
* Existing-folder normalization.
* Drive selection.
* Reserve calculations.
* qBittorrent committed-byte calculations.
* Duplicate detection.
* Path traversal rejection.
* Bounded bencode depth, collection, file, tracker, and path limits.
* Negative-length, integer-overflow, duplicate-key, and symlink rejection.
* Raw v1/v2 info-hash calculation and hybrid path-view agreement.
* Single-file, rooted multi-file, and rootless BEP 52 topology mapping to `root_folder` and expected `content_path`.
* Reservation lease renewal/expiry, singleton lease takeover, and recovery classification.
* Production/development security-mode and secret-file validation.
* TMDB result ranking.

### Integration tests

Use mock HTTP servers for:

* TMDB success.
* TMDB ambiguity.
* TMDB rate limiting.
* TMDB outage.
* qBittorrent authentication.
* qBittorrent expired session.
* Supported, too-old, malformed, and untested-major qBittorrent/Web API versions.
* `preallocate_all=true` and `temp_path_enabled=true` readiness failures.
* Missing categories created with empty paths and non-empty category paths rejected.
* qBittorrent duplicate.
* qBittorrent incorrect save path.
* qBittorrent incorrect content path, file tree, category, size, `auto_tmm`, or stopped state.
* Add timeout followed by tag/hash reconciliation without a duplicate add.
* Start is never called after failed verification and is called once after successful verification.
* qBittorrent unavailable.
* A canonical library folder appearing between review and the final under-lock rescan.
* Required login, session rotation, CSRF rejection, secure cookies, and login rate limiting.
* Archival disabled by default and private archive permissions when enabled.

### Required qBittorrent compatibility suite

Run a pinned real qBittorrent 5.x container in a dedicated CI profile for every version declared supported and verify:

* Torrent upload.
* Add occurs in the stopped state and no transfer begins before verification.
* Category assignment.
* Tag assignment.
* Explicit save path.
* `auto_tmm=false`.
* Explicit `root_folder=false` single-file content path and file list.
* Explicit `root_folder=true` rooted v1/v2/hybrid content path and file list.
* Explicit `root_folder=false` rootless BEP 52 content path and file list.
* Start occurs only after all checks pass and the torrent leaves the stopped state.
* The version, preference, and empty-category readiness gate uses the real API response shapes.

This suite is a release gate, not an optional smoke test, because qBittorrent's observed layout semantics are part of CineRoute's data-placement contract.

### NAS acceptance tests

#### Existing TV show

Input:

```text
Lost.S02.1080p.DSNP.WEB-DL.DDP5.1.H.264-FLUX.torrent
```

Existing:

```text
/t1/Lost (2004)
```

Expected qBittorrent save path:

```text
/t1/Lost (2004)
```

#### New TV show

Input:

```text
New.Show.S01.1080p.WEB-DL-GROUP.torrent
```

Expected:

* TMDB supplies canonical first-air year.
* No existing folder is found.
* Drive with most usable space is selected.
* `/tN/New Show (Year)` is created.
* qBittorrent receives that path.

#### Single-file movie

Input:

```text
Movie.Name.2026.2160p.REMUX-GROUP.torrent
```

Expected:

```text
/mN/Movie Name (2026)/Movie.Name.2026.2160p.REMUX-GROUP.mkv
```

#### Multi-file movie

Expected:

```text
/mN/Movie Name (2026)/Original.Torrent.Root/...
```

#### Existing show without enough space

Expected:

* Submission blocked.
* Show is not silently split across drives.

#### Duplicate torrent

Expected:

* Duplicate detected before add.
* No directory is created.

#### Library race during review

Create the canonical folder on another drive after the destination page is rendered but before submit.

Expected:

* The final scan under the allocation lock detects the changed library.
* No new folder is created and no torrent is added.
* The intake returns to destination review with the new locations.

#### Crash recovery

Terminate CineRoute after reservation, after the stopped add, and after verification but before start confirmation.

Expected:

* The replacement waits for or takes over the singleton lease, then reconciles by tag and hash.
* Capacity is counted exactly once.
* No second add is issued.
* A verified stopped torrent may be started; a mismatch remains stopped for operator review.

#### No post-processing

After completion in qBittorrent:

* CineRoute performs no filesystem changes.
* Original torrent paths remain unchanged.
* No completion job appears in CineRoute.

---

## 34. MVP definition

The first usable release should include:

* `.torrent` drag-and-drop.
* Movie/TV detection.
* TMDB movie and TV search.
* Match confirmation.
* Existing TV folder lookup.
* Existing movie warning.
* Four-drive free-space allocation.
* Per-drive reserve.
* Exact qBittorrent save path.
* Original torrent structure preservation.
* Categories and tags.
* Required authenticated access and secure production configuration.
* qBittorrent 5/Web API compatibility and preference/category readiness gates.
* Explicit single-file, rooted, and rootless layout handling.
* Stopped add, exact qBittorrent verification, and explicit start.
* Leased reservations, singleton enforcement, and crash recovery.
* SQLite audit history.
* Docker image.
* AMD64 and ARM64 GHCR publishing.

The MVP should not include:

* Magnets.
* Indexer search.
* Automatic completion monitoring.
* Media-file inspection after download.
* Filename renaming.
* File moves.
* Hardlinks.
* Plex/Jellyfin library refreshes.
* Music support.
* Multi-user accounts, roles, external identity providers, or an unauthenticated mode.

---

## 35. Final architectural decisions

The implementation should follow these invariants:

```text
1. The physical drive is selected before qBittorrent starts downloading.

2. Existing TV-show placement overrides free-space balancing.

3. New titles use the drive with the most usable space.

4. Movie and TV roots on the same HDD share one capacity pool.

5. Only the canonical parent folder is created by CineRoute.

6. Torrent filenames and internal folders are never renamed.

7. qBittorrent receives an explicit per-torrent save path and an explicit topology-derived root_folder value.

8. Automatic Torrent Management is disabled for CineRoute torrents.

9. Every torrent is added stopped, verified by hash, path, content path, file tree, category and settings, and only then started explicitly.

10. The authoritative library rescan, duplicate recheck, capacity calculation and reservation occur under the allocation lock immediately before add.

11. The MVP runs as one database-leased active instance; reservations expire and are reconciled after crashes without speculative duplicate adds.

12. qBittorrent preallocation and its incomplete-torrent path remain disabled, and category save paths remain empty.

13. CineRoute requires authenticated access; production mode is HTTPS-origin and secure-cookie only.

14. Torrent archival is off by default and, when enabled, stores private metadata with owner-only permissions.

15. QUI remains the manual qBittorrent-management interface.

16. CineRoute is finished immediately after the stopped torrent passes verification and qBittorrent accepts and confirms start.

17. There is no post-download processing.

18. Actual media payloads never pass through volume 2.

19. GitHub is the authoritative origin, GitHub Actions is the only CI/release pipeline, and GHCR is the only image registry.

20. Forgejo 15 is a pull-mirror backup of Git commits, branches and tags only; it has no Actions, runner, registry or development writes.
```
