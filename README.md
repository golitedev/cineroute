# CineRoute

Self-hosted movie and TV requests, library routing and hardlink management
across a multi-drive media library. Users search TMDB and request titles;
administrators control approval, trackers, release profiles and destinations.
CineRoute fulfills requests directly through Prowlarr and qBittorrent, without
requiring Seerr, Radarr or Sonarr.

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

* **Go 1.26.10+ and Node.js 24** to build, or use the Docker image.
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
| Prowlarr URL / API key (Requests searches and downloads) | `CINEROUTE_PROWLARR_URL` / `CINEROUTE_PROWLARR_API_KEY` |
| Request state database | `CINEROUTE_REQUESTS_STATE_PATH` |
| OpenSubtitles API key / user / password (Subtitles page) | `CINEROUTE_OS_API_KEY` / `CINEROUTE_OS_USERNAME` / `CINEROUTE_OS_PASSWORD` |
| Enable or disable the Subtitles page | `CINEROUTE_SUBTITLES_ENABLED` |
| Log level (`debug`, `info`, `warn`, `error`; default `info`) | `CINEROUTE_LOG_LEVEL` |
| Legacy admin username (used only without `users`) | `CINEROUTE_AUTH_USERNAME` |
| Legacy admin password (used only without `users`) | `CINEROUTE_AUTH_PASSWORD` |
| Admin listen address | `CINEROUTE_LISTEN` |
| Public request portal address (empty disables) | `CINEROUTE_PUBLIC_LISTEN` |

```yaml
listen: "127.0.0.1:8787"   # admin; use 0.0.0.0:8787 inside a container
public_listen: "127.0.0.1:8788" # public portal; 0.0.0.0:8788 inside a container
users:
  - name: "admin"
    role: "admin"
    password: "replace-with-your-admin-password"
  - name: "alice"
    role: "user"
    password: "replace-with-a-different-password"

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
make build
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

### LAN administration and Cloudflare Tunnel

One process/container serves two independent interfaces:

| Listener | Default/example | Access |
| --- | --- | --- |
| `listen` / `CINEROUTE_LISTEN` | port 8787 | Admin accounts, direct LAN only |
| `public_listen` / `CINEROUTE_PUBLIC_LISTEN` | port 8788 | User accounts, request portal only |

`public_listen` defaults to empty (disabled). Enable it and add at least one
admin and one user in the config. The public router contains no administration
routes, page markup or administration JavaScript. Admin credentials and admin
sessions are rejected on this listener. Admins can still search and request
from **Search & request** on the LAN interface.

Use `http://192.168.18.29:8787` for administration. The Compose example publishes
8787 specifically on the NAS LAN IP (`CINEROUTE_ADMIN_BIND_IP` overrides it),
and publishes 8788 only on host loopback. Point a host-installed `cloudflared`
at **`http://127.0.0.1:8788`**. If `cloudflared` is another container on a shared
Docker network, point it at **`http://cineroute:8788`** and remove the unnecessary
8788 host publication. Never target 8787 with a tunnel or router port forward.
For example, a locally managed tunnel can use:

```yaml
ingress:
  - hostname: requests.example.com
    service: http://127.0.0.1:8788
  - service: http_status:404
```

For a dashboard-managed tunnel, set the published application's HTTP service
URL to the same public listener. No admin hostname is needed.
[Cloudflare ingress configuration](https://developers.cloudflare.com/tunnel/features/locally-managed-tunnels/configuration-file/)
documents hostname routing and the final catch-all.

`admin.allowed_networks` restricts the actual TCP peer to private/loopback
CIDRs; narrow it to your LAN as shown in `config.example.yaml`. Defaults allow
loopback, RFC 1918 and IPv6 ULA networks. Public CIDRs and empty lists are
rejected. Docker may present a private bridge peer for connections from the
host; add that bridge CIDR if required. Forwarded addresses never grant access:
the admin listener rejects proxy/Cloudflare forwarding headers outright.
Local reverse proxies in front of administration are intentionally unsupported.

With `admin.allowed_hosts: []`, administration accepts `localhost` and literal
private/loopback IP hosts. If you use a LAN name, configure exact names/IPs,
for example `["nas.lan", "192.168.18.29", "127.0.0.1", "localhost"]`. There are
no wildcard hostnames. This check, together with same-origin browser checks,
limits DNS rebinding and access from the public portal's browser origin.

Cookie-authenticated mutations require the CSRF token returned by
`GET /api/session` in `X-CineRoute-CSRF`; both interfaces' JavaScript handles it.
Explicit Basic Auth remains available to non-browser clients, with rate limits
on failed password attempts. Admin pages/API responses are not cacheable and
cannot be embedded in frames. Both listeners must bind successfully before the
single shared request worker starts.

Docker port publishing can bypass UFW rules, so retain explicit host-IP port
bindings rather than relying on a generic UFW deny rule.
[Docker networking documentation](https://docs.docker.com/engine/network/packet-filtering-firewalls/)
explains the interaction. The example also runs without Linux capabilities,
disables privilege escalation and makes the container root filesystem read-only;
media, state and scratch mounts remain writable for the app's existing functions.

See [SECURITY_REVIEW.md](SECURITY_REVIEW.md) for the review scope, verified
boundaries and remaining deployment assumptions. No firewall, tunnel or live
NAS configuration is changed by these source changes.

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

Persistent manual-intake history, manual-intake recovery after crash, archives,
manual-intake auto-submit mode, subtitles for the remote **TV** roots, OCR for
image-based subtitles (PGS/DVD) and audio/voice-activity reference mode.

## Accounts and user requests

Create accounts in the YAML `users` list, using `name`, `role` (`admin` or
`user`), and either `password` or bcrypt `password_hash`. Roles default to
`user`; at least one enabled administrator is required. Names are unique
ignoring case. Set `disabled: true` to disable an account. Restart after edits.
To generate a hash without putting the password in a shell command:

```sh
read -r -s -p 'Password: ' request_password; echo
printf '%s' "$request_password" | ./cineroute hash-password
unset request_password
```

Paste the output into `password_hash` and remove `password` for that account.
When `users` is populated it replaces the legacy `auth_username` /
`auth_password` login. Anonymous administration is disabled: without admin
credentials the service refuses to start. The public listener also requires an
enabled account with role `user`; legacy credentials authorize administration
only. Existing installations can migrate by adding admin and user accounts together.

Admins retain the full management interface and can use **Search & request**
to open the request portal. Users land on a single movie/show search bar with
portrait posters, title details, TV season selection and their own request
history with progress and cancellation. They cannot read administration APIs,
other users' requests, filesystem paths, tracker URLs or credentials. TMDB API
access stays on the server. Password, role, disable or removal changes invalidate
the account's existing sessions after restart. Admin and user sessions last up to
90 days; admin sessions use SameSite Strict. Separate cookie names
and signatures isolate the listeners. Keep `session.key` beside the request
database persistent across restarts. This update requires signing in again.

Catalog discovery uses TMDB's official search API, including its translated
and alternative-title matching. Successful searches retain TMDB's results and
pagination. An empty first page can trigger up to eight broader fragment
searches; suggestions are deduplicated and ranked by title similarity, ignoring
spacing/punctuation and tolerating letter edits/transpositions. A few alternative
title lookups help with translated names. These extra lookups share a six-second
budget and at most three simultaneous connections. Weak matches are discarded.
The portal labels fallback results as similar titles, and users must select the
intended title before requesting it. This is best-effort assistance, not a local
copy of the entire TMDB catalog or TMDB website scraping.

In **Requests → General → Catalog visibility**, the administrator can choose
all titles (including upcoming releases), or **Digitally released movies / aired
TV shows only**. The latter requires a reached TMDB digital release date (type 4)
in any country for movies; theatrical, physical-only and unknown dates do not
qualify. TV uses a reached first-air or regular-episode air date because TMDB
does not provide the equivalent movie release-type dates for TV series. The
filter covers search, title details and new requests, so a direct TMDB ID cannot
bypass it. Native TMDB ordering/page numbers are preserved; Load more stays
available if an earlier page contains only hidden titles. Release checks have
a 15-minute cache, at most four concurrent requests and a ten-second page budget.
Existing requests remain visible and are not requeued by a visibility-only
policy change. **Movie availability** separately controls when the worker starts
searching trackers for a requested movie.

Movie and TV request details include **Prefer Spanish audio**. It is a soft,
per-user request preference, not a language requirement or an admin-policy edit.
Within the same quality, confirmed Spanish audio ranks before other language
preferences. Lat-Team receives the highest effective tracker priority only if
the administrator selected it and it is enabled in Prowlarr. Its identity comes
from the configured Prowlarr definition (or an exact normalized display name),
never a release's claimed tracker name. Tracker priority remains a tie-breaker:
confirmed Spanish on another tracker beats unknown audio on Lat-Team, and all
quality/codec/size/keyword/audio filters still apply. All selected trackers are
still searched. Changing the checkbox updates the same request, preserves
approval and quota accounting, and is shown in personal requests and admin
queue/history. Existing copies and shared active downloads may still satisfy
the request without a separate Spanish version or replacement.

The **Requests** admin tab, immediately after TV, contains the shared policy
and a **Queue** for approval, decline, cancellation, retry and diagnostic details.
Requests either enter this approval queue or are approved automatically according
to policy. Limits are enforced per user; admins are exempt. Re-requesting a title
merges seasons into the user's existing request instead of creating extra downloads.

* **General:** allowed media types, automatic approval, per-user movie/series
  limits and their time window, searches on approval, search pacing, retry
  interval, release delay, minimum seeders, download concurrency and recovery.
* **Trackers:** load indexers from the configured Prowlarr connection, select
  torrent trackers and assign priorities (1 is strongest). Disabled and
  non-torrent indexers cannot be newly selected. No trackers are selected by
  default. Refreshing an unavailable server preserves the selected tracker IDs.
* **Movies / TV:** independent Main or Remote destinations, automatic disk
  placement or a selected disk for new titles, ordered allowed qualities,
  upgrade targets, minimum keyword scores, codecs, audio languages and sizes.
  Movie limits apply to a whole release; TV has separate episode and pack caps.
  A maximum size of 0 means unlimited. TV monitoring, specials and season-pack
  preference are separate from movie release availability.
* **Subtitles after download (Movies):** **Automatically run the alass subtitle
  flow** is off by default and applies to new movie downloads started while it
  is checked. Once the feature video is complete and verified, it enters a
  durable background queue for its selected Main or Remote library. The flow
  adds only missing configured languages (normally Swedish, Spanish and English),
  preserves existing full text subtitles, and requires an existing usable text
  subtitle as its timing reference. Image-based and forced-only tracks do not
  satisfy full-language targets. It uses the existing OpenSubtitles, alignment,
  acceptance and quota settings; outcomes appear in **Subtitles**. It does not
  scan or run unrelated movies. Queueing or subtitle failures do not fail or
  redownload the movie. Interrupted work resumes after restart; quota/service
  hard stops defer automatic work for an hour, while no-match and no-reference
  outcomes wait for an administrator's retry.
* **Language priorities (Movies / TV):** admin-only checkboxes prefer audio and
  full subtitle languages independently for each profile. Quality order remains
  first; each distinct preferred audio or full-subtitle match adds one language
  point, before optional text-subtitle preference, keyword scores, pack preference,
  tracker priority and seeders.
  These points do not bypass filters or the minimum keyword score, and language
  preferences alone do not replace an already conforming library copy. With no language or
  format preference selected, existing behavior is preserved and no native
  metadata lookups are performed.
  Regional variants are grouped (for example all Spanish audio), and original
  audio is resolved using TMDB's original language without double-counting it.
  Forced-only, signs/songs-only and commentary tracks do not count as full
  subtitles; commentary audio does not earn language points.
  **Prefer text-based subtitles** favors SRT/SubRip, ASS/SSA, WebVTT, timed text
  and other recognized text formats over PGS, VobSub and other image formats.
  It ranks after quality and preferred-language matches, so text subtitles in
  another language do not outrank a preferred language. With no subtitle
  languages selected, any full text subtitle track earns a format preference;
  otherwise only text tracks in the preferred subtitle languages earn points.
  Unknown formats are neutral and no OCR or format conversion is performed.
  Compatible UNIT3D trackers supply MediaInfo/BDInfo through their native API,
  authenticated with their individual API keys read from Prowlarr's configuration.
  CineRoute batches the first 100 native results per tracker/search and matches
  only the exact same-origin torrent IDs already returned by Prowlarr. Missing,
  unsupported or unavailable reports earn no points, and remain eligible under
  the other rules. A TV pack's report can describe a representative episode;
  reported languages are preferences, not verification of every video stream.
  Lookups respect the configured search interval, use bounded in-memory caches
  (six-hour reports, five-minute configuration/failure refresh), and never expose
  tracker keys to either browser or persist keys in the requests database.
  Admin History shows the selected release's reported languages and its language
  points at selection time. Users have only the fixed Spanish-audio preference;
  other language/format settings and tracker selections remain admin-only.
* **History:** after TV, see requests from every user, their destination, current
  status, progress, retry time and all recorded download attempts. Search by title,
  user, tracker, disk or hash, filter by status and page through older requests.
  Stalled, paused, queued, checking and failed downloads remain visible alongside
  completed additions. History refreshes every 15 seconds while open, using the
  worker's stored qBittorrent observations without issuing tracker searches.
  New downloads retain their requesting user, tracker details-page link and
  completion time. Downloads shared by several requests show their actual
  initiator separately. Older records remain visible; missing source details are
  labeled rather than inferred. Tracker download URLs and credentials are never
  used as history links. This endpoint and its interface are admin-only.
* **Keywords:** per-media text or regular-expression rules can prefer a release
  with a positive or negative score, require a match, or reject a match. Every
  required rule must match and any rejection blocks the release. Text matching
  ignores case; regex matching is case-sensitive unless `(?i)` is specified.
  Regexes are validated using Go's regular-expression syntax.

Destinations default to **Remote** for both movies and TV. The chosen target
determines which library satisfies a request. Each request captures its target
and new-title disk setting when created; changing them applies to new requests.
Existing titles are anchored by either main or remote folders, and shows stay on one disk. The
new-title disk setting applies only when no existing title anchors the download;
automatic selection uses the disk with the most free space. Library root paths
come from the existing drive configuration.

Use **Save settings** to persist the complete policy. Unsaved edits survive
switching between settings panels and other tabs; Reload saved asks before
discarding edits. Invalid settings are rejected without replacing saved policy.
Settings, request history, episode coverage, disk assignments and recovery
receipts survive restarts in `/data/requests.db`, configurable through
`requests.state_path` or `CINEROUTE_REQUESTS_STATE_PATH`. Keep the existing
`/data` volume writable; the read-only YAML configuration is not rewritten.

The worker searches only selected, enabled Prowlarr torrent indexers, ranks
releases by allowed quality, language preference points, text-subtitle preference,
keyword score, pack preference, tracker priority
and seeders, and validates actual torrent files before adding anything. Unknown
or ambiguous identities, qualities, episode numbering and overlapping packs
remain waiting with an admin diagnostic. Codec rules use release/filename
evidence. Allowed audio languages use tracker reports when language priorities
enable native lookups, otherwise falling back to the existing title-based rule;
CineRoute does not inspect downloaded audio streams at search time.
TV packs must contain only requested, aired episodes that are missing or eligible
for upgrade. New episodes and future seasons are polled according to monitoring.
Quality upgrades retain existing files and seeding torrents, adding a separate
release only when it improves the profile; conflicting filenames are rejected.

New downloads use the same stopped-add, destination/layout verification and
start transaction as manual routing. Durable intents let a restart verify an
existing add before resuming it. A completed matching torrent in the opposite
library can have its selected torrent files hardlinked on the same disk without
changing its seed location or linking unrelated episodes.
A single database worker lease prevents multiple instances processing the same
queue. The worker checks every 30 seconds, with search pacing and retry intervals
from the policy. Stalled recovery pauses only request-managed torrents and keeps
partial files; manually paused downloads remain paused. Cancelling a request
withdraws monitoring, while files and downloads already started are retained.

“Ready” means the expected files are present in the chosen library. Jellyfin
must watch or scan those folders as usual. This workflow supports conventional
season/episode numbering; ambiguous absolute/anime numbering is left for admin
review rather than guessed. Configure TMDB, Prowlarr, selected trackers and
qBittorrent before expecting automatic fulfillment.

The settings design was informed by upstream source inspected in temporary
checkouts: [Seerr request defaults](https://github.com/seerr-team/seerr/blob/c57dca9/server/lib/settings/index.ts),
[Radarr quality profiles](https://github.com/Radarr/Radarr/blob/4d9bd47/src/NzbDrone.Core/Profiles/Qualities/QualityProfile.cs)
and [Sonarr release profiles](https://github.com/Sonarr/Sonarr/blob/465b519/src/NzbDrone.Core/Profiles/Releases/ReleaseProfile.cs).
CineRoute stores its own policy and does not require these applications.

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

The Prowlarr client and optional URL/API-key configuration supply the Requests
tracker list. The app does not perform release searches or offer
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

### Unified public portal and music

The public listener serves a static React interface embedded in CineRoute's Go
binary. Albums, Movies and TV share the centered search controls. The existing
bottom queue button opens **My requests**, with active work and recent history
across all three media types; history stays out of the search page. Movie and
TV details include season selection, future-season monitoring and the Spanish
audio preference. Downloading and release searches appear in the same bottom
status bar as music downloads. Administration remains on the LAN listener.

Music runs in the CineRoute process, without another service or container.
Enable `music.enabled` in `config.yaml`, set `music.qobuz.user_id` and
`music.qobuz.token` (or `CINEROUTE_QOBUZ_USER_ID` and `CINEROUTE_QOBUZ_TOKEN`),
and mount existing writable library and staging directories at the configured
`music.paths.library` and `music.paths.staging`. Persistent music state lives in
`/data/music` by default. The Compose example includes optional mounts. Keep
staging outside the library so media servers never discover partial albums.

The bundled streamrip CLI downloads complete Qobuz releases. Choosing a track
requests its containing album, EP or single. CineRoute verifies completeness,
FLAC decoding and MD5, cleans metadata, and imports only complete releases.
Already imported albums are skipped. Music requests queue immediately and do
not use the movie/TV approval or quota policy; each user can have up to 50
outstanding music requests. The public API shows only the signed-in user's jobs
and only lets that user cancel, retry or dismiss their own music requests.
Qobuz account credentials, downloader settings and raw logs stay on the server.
An optional `music.navidrome` section accepts `url`, `username` and `password`
to trigger a scan after importing.

Docker builds the frontend and bundles Python/streamrip, FLAC, FFmpeg and alass
in the same runtime image. No Node.js server runs in production. For a native
build, install Node.js 24 and run `make build`; this exports the frontend before
compiling Go. Running `go build` alone without first running `make web` produces
a build-instructions page on the public listener. Native music additionally
requires Python/streamrip, `flac` and `metaflac` on PATH. The Dockerfile pins the
tested streamrip revision.

Frontend checks: `cd web && npm ci && npm run lint && npm run build && npm test`.
The browser tests use fixture catalog responses and never start real downloads.
The music engine includes unit tests for download verification, metadata cleanup,
queue recovery and naming; `go test ./...` runs them with CineRoute's API tests.

The public interface incorporates the MIT-licensed [Qobuz-DL frontend](https://github.com/QobuzDL/Qobuz-DL).
Its copyright and license are preserved in `web/LICENSE`. Streamrip is GPL-3.0
and runs as a separate executable; its pinned source is linked in the Dockerfile.
