#!/usr/bin/env python3
"""Render path-labeled spectrograms for the music library."""

import fcntl
import hashlib
import json
import os
import signal
import stat
import subprocess
import textwrap
from concurrent.futures import ThreadPoolExecutor, as_completed
from pathlib import Path
from tempfile import NamedTemporaryFile, TemporaryDirectory

LIBRARY = Path("/mnt/disk4/media/music-main")
OUTPUT = Path("/srv/scripts/flaccheck/specs")
FONT = "/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf"
PNG_SIGNATURE = b"\x89PNG\r\n\x1a\n"
AUDIO_EXTENSIONS = {".flac", ".wav", ".wave", ".aiff", ".aif", ".m4a", ".alac", ".ape", ".wv", ".opus"}
SANDBOX = """
root=$1
mount --bind "$root" "$root"
mount --bind "$2" "$root/music"
mount -o remount,bind,ro,nosuid,nodev,noexec,nosymfollow "$root/music"
mount --bind /usr "$root/usr"
mount -o remount,bind,ro,nosuid,nodev "$root/usr"
mount --bind /etc/alternatives "$root/etc/alternatives"
mount -o remount,bind,ro,nosuid,nodev "$root/etc/alternatives"
mount --bind /etc/ld.so.cache "$root/etc/ld.so.cache"
mount -o remount,bind,ro,nosuid,nodev "$root/etc/ld.so.cache"
mount -o remount,bind,ro,nosuid,nodev "$root"
cd "$root"
shift 2
exec /usr/sbin/chroot . /usr/bin/setpriv --no-new-privs \\
    --bounding-set=-all --inh-caps=-all --ambient-caps=-all /usr/bin/ffmpeg "$@"
"""


def raise_walk_error(error):
    raise error


def list_tracks(library, name_limit):
    entries = []
    for directory, _, names in os.walk(library, onerror=raise_walk_error):
        for name in names:
            source = Path(directory) / name
            if source.suffix.lower() not in AUDIO_EXTENSIONS or not stat.S_ISREG(source.lstat().st_mode):
                continue

            path = source.relative_to(library)
            # Keep existing image paths and distinguish files with the same stem.
            filename = path.name + ".png"
            if len(os.fsencode(filename)) > name_limit:
                digest = hashlib.sha256(os.fsencode(path.name)).hexdigest()
                filename = f"spectrogram-{digest}.png"
            entries.append({"track": str(path), "image": str(path.with_name(filename))})
    return sorted(entries, key=lambda entry: (entry["track"].casefold(), entry["track"]))


def signature(status):
    return status.st_dev, status.st_ino, status.st_size, status.st_mtime_ns, status.st_ctime_ns


def render(entry, library, output):
    track = entry["track"]
    source = library / track
    if source.resolve(strict=True) != source or not source.is_file():
        raise ValueError("Source is not a regular file at the library path.")
    before = source.stat()
    dest = output / entry["image"]
    if dest.parent.resolve() != dest.parent:
        raise ValueError("The image directory contains a symlink.")
    dest.parent.mkdir(parents=True, exist_ok=True)
    if dest.is_file() and not dest.is_symlink() and dest.stat().st_mtime_ns >= max(
        before.st_ctime_ns, before.st_mtime_ns
    ):
        with dest.open("rb") as image:
            if image.read(len(PNG_SIGNATURE)) == PNG_SIGNATURE:
                return "reused"

    label = textwrap.wrap(track, width=105, break_on_hyphens=False, replace_whitespace=False)
    header_height = 42 + 26 * (len(label) - 1)
    filters = (
        "showspectrumpic=s=1400x700:fscale=lin:legend=1,"
        f"pad=iw:ih+{header_height}:0:{header_height}:black,"
        f"drawtext=fontfile={FONT}:textfile=/label:expansion=none:"
        "fontcolor=white:fontsize=20:line_spacing=6:x=20:y=11"
    )
    with TemporaryDirectory(prefix=".render-", dir=output) as directory:
        root = Path(directory)
        (root / "music").mkdir()
        (root / "usr").mkdir()
        (root / "etc" / "alternatives").mkdir(parents=True)
        (root / "etc" / "ld.so.cache").touch()
        (root / "lib").symlink_to("usr/lib")
        (root / "lib64").symlink_to("usr/lib64")
        (root / "label").write_text("\n".join(label) + "\n", encoding="utf-8")
        image = root / "spectrogram.png"
        with image.open("wb") as png:
            result = subprocess.run(
                [
                    "unshare", "--user", "--map-root-user", "--mount", "--net", "--kill-child",
                    "/bin/bash", "-euc", SANDBOX, "_", root, library,
                    "-hide_banner", "-loglevel", "error", "-nostdin",
                    "-filter_complex_threads", "1", "-threads", "1", "-i", f"/music/{track}",
                    "-lavfi", filters, "-frames:v", "1", "-threads", "1", "-c:v", "png",
                    "-f", "image2pipe", "pipe:1",
                ],
                stdin=subprocess.DEVNULL, stdout=png, stderr=subprocess.PIPE,
            )
        if result.returncode:
            raise ValueError(result.stderr.decode(errors="replace").strip() or f"FFmpeg exited {result.returncode}.")
        with image.open("rb") as png:
            if png.read(len(PNG_SIGNATURE)) != PNG_SIGNATURE:
                raise ValueError("FFmpeg did not produce a PNG.")
        if signature(before) != signature(source.stat()):
            raise ValueError("The track changed during rendering. Retry this batch.")
        image.replace(dest)
    return "created"


def write_manifest(output, library, entries):
    with NamedTemporaryFile(mode="w", encoding="utf-8", dir=output, delete_on_close=False) as temporary:
        json.dump({"library": str(library), "tracks": entries}, temporary, ensure_ascii=False, indent=2)
        temporary.write("\n")
        temporary.close()
        Path(temporary.name).replace(output / "manifest.json")


def run(library, output):
    output.mkdir(exist_ok=True)
    if output.resolve() != output:
        raise ValueError("The output directory is a symlink.")
    with (output / ".render.lock").open("a") as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise ValueError("A spectrogram batch is already running.") from None

        entries = list_tracks(library, os.pathconf(output, "PC_NAME_MAX"))
        if not entries:
            raise ValueError(f"No supported audio files found in {library}.")
        write_manifest(output, library, entries)
        print(f"Checking {len(entries)} library tracks; rendering missing or stale PNGs into {output}", flush=True)
        counts = {"created": 0, "reused": 0, "failed": 0}
        with ThreadPoolExecutor(max_workers=2) as workers:
            jobs = {workers.submit(render, entry, library, output): entry for entry in entries}
            try:
                for index, job in enumerate(as_completed(jobs), 1):
                    try:
                        counts[job.result()] += 1
                    except (OSError, ValueError) as error:
                        counts["failed"] += 1
                        print(f"Failed {jobs[job]['track']}: {error}", flush=True)
                    if index % 50 == 0 or index == len(entries):
                        print(f"{index}/{len(entries)}: {counts}", flush=True)
            except KeyboardInterrupt:
                workers.shutdown(wait=True, cancel_futures=True)
                raise
        return int(counts["failed"] != 0)


def stop(_signum, _frame):
    raise KeyboardInterrupt


if __name__ == "__main__":
    os.umask(0o027)
    signal.signal(signal.SIGTERM, stop)
    try:
        raise SystemExit(run(LIBRARY, OUTPUT))
    except KeyboardInterrupt:
        raise SystemExit("Stopped. Run the same command to reuse completed PNGs.")
    except (OSError, ValueError) as error:
        raise SystemExit(str(error)) from None
