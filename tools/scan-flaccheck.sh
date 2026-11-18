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
work=$(mktemp -d "$output/.scan.XXXXXXXX")
cleanup() {
    local status=$1
    rm -rf -- "$work"
    if (( status != 0 )); then
        printf 'Scan failed (exit %s). See %s\n' "$status" "$log" >&2
    fi
}
trap 'cleanup "$?"' EXIT

printf 'Scanning %s at %s\n' "$library" "$(date -u +%FT%TZ)"
[[ -d "$library" && -r "$library" && -x "$library" ]]
[[ -x "$scanner" ]]
root="$work/root"
mkdir -p -- "$root/music" "$root/usr"
ln -s usr/lib "$root/lib"
ln -s usr/lib64 "$root/lib64"
touch "$root/flaccheck"

# The mount namespace keeps these mounts private. The scanner sees only
# read-only mounts, has no host network access, and writes JSON to stdout.
# shellcheck disable=SC2016
unshare --user --map-root-user --mount --net --fork /bin/bash -euc '
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
        /flaccheck scan /music --mode balanced --workers 2 --format json --quiet
' _ "$root" "$library" "$scanner" </dev/null >"$work/scan.json" 9>&-

# Album folders are directly under music-main. Keep disc subdirectories
# in the track names so tracks on different discs remain distinguishable.
python3 - "$work/scan.json" "$work/lossy-tracks.txt" "$library" <<'PY'
import json
import sys
from collections import Counter, defaultdict
from pathlib import Path, PurePosixPath

scan_path, report_path, library = sys.argv[1:]
scan = json.loads(Path(scan_path).read_text())
albums = defaultdict(list)
counts = Counter()


def display(text):
    return text.replace("\n", "\\n").replace("\r", "\\r").replace("\t", "\\t")


for result in scan["results"]:
    verdict = result["transcode_verdict"]
    counts[verdict] += 1
    if verdict != "TRANSCODED":
        continue
    parts = PurePosixPath(result["path"]).relative_to("/music").parts
    album, track = (parts[0], "/".join(parts[1:])) if len(parts) > 1 else ("(Library root)", parts[0])
    codec = result.get("codec_guess")
    albums[album].append((track, codec))

skipped, errors = scan["skipped"], scan["errors"]
with open(report_path, "w", encoding="utf-8") as report:
    print(f"Library: {library}", file=report)
    print(f"Tracks flagged as lossy: {counts['TRANSCODED']} across {len(albums)} album(s)", file=report)
    print(f"Scanned: {sum(counts.values())} | Skipped: {len(skipped)} | Errors: {len(errors)}", file=report)
    for album, tracks in sorted(albums.items(), key=lambda item: item[0].casefold()):
        print(f"\n{display(album)}", file=report)
        for track, codec in sorted(tracks, key=lambda item: item[0].casefold()):
            hint = f" ({display(codec)})" if codec else ""
            print(f"  {display(track)}{hint}", file=report)

for verdict, count in sorted(counts.items()):
    print(f"{verdict}: {count}")
print(f"Skipped: {len(skipped)} | Errors: {len(errors)}")
for label, messages in (("Skipped", skipped), ("Error", errors)):
    for message in messages:
        print(f"{label}: {display(message)}")
PY

mv -fT -- "$work/lossy-tracks.txt" "$output/lossy-tracks.txt"
printf 'Report written to %s\n' "$output/lossy-tracks.txt"
