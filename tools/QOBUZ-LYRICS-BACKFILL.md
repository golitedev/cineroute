# One-time Qobuz LRC-only sidecar backfill

The NAS launcher `backfill-qobuz-lyrics.sh` scans
`/mnt/disk4/media/music-main`. It runs the Python helper in a separate container
using the image already running as `cineroute`. It does not restart CineRoute.
The image must include Streamrip's `QobuzClient.get_lyrics` support.

The container root filesystem, script, and Streamrip config are read-only. Only
`music-main` is mounted writable; the service's databases and other media roots
are not mounted. `/tmp` is temporary memory storage removed with the container.

The helper reads `QOBUZ_TRACK_ID` tags with Mutagen; it never saves tags, downloads
audio, renames audio, or changes downloader settings. It queries Qobuz directly
for each ID, saving UTF-8 `.lrc` lyrics only when timestamps exist. Plain lyrics
without timings are skipped and counted as `no_timestamps`; no `.txt` is created.
Sidecars use the same filename stem as the audio. Existing `.lrc` files (including
case variants and symlinks) cause the track to be skipped. Other files are left
alone.
Symlinked audio and directories, missing/invalid IDs, and unavailable lyrics are
skipped. It never overwrites a sidecar. Streamrip publishes complete sidecars
atomically using a short-lived temporary file in the same directory.

There is one track request at a time, with a one-second pause between tracks.
Transient failures are retried; five consecutive failures stop the batch.
Progress and a summary go to the terminal; no log, cache, or report files are
created in the library. Rerunning resumes by skipping existing sidecars; tracks
with unavailable lyrics are checked again.

On the NAS:

```sh
# Read tags and query Qobuz for three eligible tracks, without writing sidecars.
bash /srv/docker/cineroute/tools/backfill-qobuz-lyrics.sh --dry-run --limit 3

# Backfill the whole music-main library.
bash /srv/docker/cineroute/tools/backfill-qobuz-lyrics.sh
```

For a long-running session:

```sh
tmux attach -t qobuz-lyrics-backfill
bash /srv/docker/cineroute/tools/backfill-qobuz-lyrics.sh
```

Detach with Ctrl+B, then D. Ctrl+C stops the helper. Reattach with the same
`tmux attach` command. The launcher names its temporary container
`cineroute-lyrics-backfill`, preventing two copies running concurrently.

The full backfill is intentionally started by the user. A prepared tmux session
does not automatically run the command.

Tests require the same Streamrip and Mutagen dependencies as the image:

```sh
python -m unittest discover -s tools/tests -v
```

They cover byte-for-byte audio preservation (including mtime and inode), skipping plain lyrics
and writing synced output, dry-run, existing sidecars, symlinks, malformed IDs, resuming,
and stopping on repeated errors.
