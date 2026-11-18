#!/usr/bin/env bash
set -euo pipefail
umask 027

library=/mnt/disk4/media/music-main
output=/srv/scripts/flaccheck
scanner="$HOME/.local/bin/flaccheck"

mkdir -p -- "$output/logs"
exec 9>"$output/.scan.lock"
if ! flock -n 9; then
    printf 'A flaccheck scan is already running.\n' >&2
    exit 1
fi

log="$output/logs/$(date -u +%Y%m%dT%H%M%SZ)-$$.log"
exec > >(tee -a "$log" 9>&-) 2>&1

# The scanner gets a private namespace with read-only mounts and no network.
# shellcheck disable=SC2016
sandbox='
    root=$1
    mount --bind "$root" "$root"
    mount --bind "$2" "$root/music"
    mount -o remount,bind,ro,nosuid,nodev,noexec,nosymfollow "$root/music"
    mount --bind /usr "$root/usr"
    mount -o remount,bind,ro,nosuid,nodev "$root/usr"
    mount --bind "$3" "$root/flaccheck"
    mount -o remount,bind,ro,nosuid,nodev "$root/flaccheck"
    mount -o remount,bind,ro,nosuid,nodev "$root"

    cd "$root"
    exec /usr/sbin/chroot . /usr/bin/setpriv --no-new-privs \
        --bounding-set=-all --inh-caps=-all --ambient-caps=-all \
        /flaccheck scan "$4" --mode "$5" --workers 2 --format json --quiet
'

exec python3 -u - "$library" "$output" "$scanner" "$sandbox" <<'PY'
import hashlib
import json
import os
import signal
import sqlite3
import stat
import subprocess
import sys
from collections import Counter, defaultdict
from contextlib import closing
from datetime import datetime, timezone
from pathlib import Path, PurePosixPath
from tempfile import TemporaryDirectory

MODE = "balanced"
# Keep this list in sync with flaccheck's supported scan inputs.
AUDIO = {".flac", ".wav", ".wave", ".aiff", ".aif", ".m4a", ".alac", ".ape", ".wv", ".opus"}


def display(text):
    return text.replace("\n", "\\n").replace("\r", "\\r").replace("\t", "\\t")


def raise_walk_error(error):
    raise error


def snapshot(group):
    """Describe supported regular files without following symlinks."""
    paths = (
        (Path(directory) / name for directory, _, names in os.walk(group, onerror=raise_walk_error)
         for name in names)
        if group.is_dir() else (group,)
    )
    tracks = []
    for path in paths:
        if path.suffix.lower() not in AUDIO:
            continue
        info = path.lstat()
        if stat.S_ISREG(info.st_mode):
            tracks.append((str(path.relative_to(group.parent)), info.st_dev, info.st_ino,
                           info.st_size, info.st_mtime_ns, info.st_ctime_ns))
    return sorted(tracks)


def scan_group(root, library, scanner, sandbox, name):
    command = [
        "unshare", "--user", "--map-root-user", "--mount", "--net", "--kill-child",
        "/bin/bash", "-euc", sandbox, "_", root, library, scanner, f"/music/{name}", MODE,
    ]
    result = subprocess.run(command, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, check=True)
    data = json.loads(result.stdout)
    tracks = [
        (item["path"], item["transcode_verdict"], item.get("codec_guess"))
        for item in data["results"]
    ]
    return tracks, data["skipped"], data["errors"]


def write_report(path, library, tracks, skipped, errors):
    albums = defaultdict(list)
    counts = Counter(verdict for _, verdict, _ in tracks)
    for path_in_sandbox, verdict, codec in tracks:
        if verdict != "TRANSCODED":
            continue
        parts = PurePosixPath(path_in_sandbox).relative_to("/music").parts
        album, track = (parts[0], "/".join(parts[1:])) if len(parts) > 1 else ("(Library root)", parts[0])
        albums[album].append((track, codec))

    with path.open("w", encoding="utf-8") as report:
        print(f"Library: {library}", file=report)
        print(f"Tracks flagged as lossy: {counts['TRANSCODED']} across {len(albums)} album(s)", file=report)
        print(f"Scanned: {sum(counts.values())} | Skipped: {skipped} | Errors: {errors}", file=report)
        if skipped or errors:
            print("Some tracks could not be analyzed. See the run log.", file=report)
        for album, flagged in sorted(albums.items(), key=lambda item: item[0].casefold()):
            print(f"\n{display(album)}", file=report)
            for track, codec in sorted(flagged, key=lambda item: item[0].casefold()):
                hint = f" ({display(codec)})" if codec else ""
                print(f"  {display(track)}{hint}", file=report)

    for verdict, count in sorted(counts.items()):
        print(f"{verdict}: {count}")
    print(f"Skipped: {skipped} | Errors: {errors}")


def run(library, output, scanner, sandbox):
    print(f"Scanning {library} at {datetime.now(timezone.utc):%Y-%m-%dT%H:%M:%SZ}")
    with scanner.open("rb") as executable:
        scanner_hash = hashlib.file_digest(executable, "sha256").hexdigest()
    # Reuse checkpoints only with the same library, binary and scan semantics.
    cache_identity = (str(library.resolve()), scanner_hash, MODE, 1)
    entries = sorted(library.iterdir(), key=lambda path: path.name.casefold())
    with TemporaryDirectory(prefix=".scan.", dir=output) as scratch, closing(
        sqlite3.connect(output / "resume.sqlite3")
    ) as cache:
        cache.execute(
            "CREATE TABLE IF NOT EXISTS albums (name TEXT PRIMARY KEY, fingerprint TEXT, tracks TEXT)"
        )
        work = Path(scratch)
        root = work / "root"
        (root / "music").mkdir(parents=True)
        (root / "usr").mkdir()
        (root / "lib").symlink_to("usr/lib")
        (root / "lib64").symlink_to("usr/lib64")
        (root / "flaccheck").touch()

        tracks, live = [], set()
        reused = scanned = skipped = errors = 0
        for group in entries:
            if group.is_symlink():
                continue
            name = group.name
            try:
                files = snapshot(group)
                if not files:
                    continue
                live.add(name)
                fingerprint = hashlib.sha256(json.dumps((cache_identity, files)).encode()).hexdigest()
                saved = cache.execute(
                    "SELECT tracks FROM albums WHERE name = ? AND fingerprint = ?", (name, fingerprint)
                ).fetchone()
                if saved is not None:
                    tracks.extend(json.loads(saved[0]))
                    reused += 1
                    continue

                print(f"Scanning {display(name)} ({len(files)} track(s))")
                found, not_scanned, failed = scan_group(root, library, scanner, sandbox, name)
                if files != snapshot(group):
                    raise ValueError("files changed during the scan; retry this album")
                if len(found) + len(not_scanned) + len(failed) != len(files):
                    raise ValueError("the scanner returned a different number of tracks than expected")

                tracks.extend(found)
                scanned += 1
                skipped += len(not_scanned)
                errors += len(failed)
                for label, messages in (("Skipped", not_scanned), ("Error", failed)):
                    for message in messages:
                        print(f"{label}: {display(message)}")
                if not not_scanned and not failed:
                    with cache:
                        cache.execute(
                            "INSERT OR REPLACE INTO albums VALUES (?, ?, ?)",
                            (name, fingerprint, json.dumps(found)),
                        )
            except (OSError, subprocess.CalledProcessError, ValueError) as error:
                errors += 1
                print(f"Error scanning {display(name)}: {error}")

        print(f"Completed groups: {scanned} scanned, {reused} reused from checkpoints")
        stale = [(name,) for (name,) in cache.execute("SELECT name FROM albums") if name not in live]
        with cache:
            cache.executemany("DELETE FROM albums WHERE name = ?", stale)
        report = work / "lossy-tracks.txt"
        write_report(report, library, tracks, skipped, errors)
        report.replace(output / report.name)
        print(f"Report written to {output / report.name}")
        return int(errors != 0)


def stop(_signum, _frame):
    raise KeyboardInterrupt


signal.signal(signal.SIGTERM, stop)
try:
    library, output, scanner = map(Path, sys.argv[1:4])
    sys.exit(run(library, output, scanner, sys.argv[4]))
except KeyboardInterrupt:
    print("Scan interrupted. Completed checkpoints will be reused on the next run.")
    sys.exit(130)
PY
