#!/usr/bin/env python3
"""One-time Qobuz LRC-only backfill. Never saves audio tags or downloader config."""

# Per-file failures must not stop the batch or expose credential-bearing errors.
# ruff: noqa: BLE001

import argparse
import asyncio
import logging
import os
import re
import sys
from collections import Counter
from pathlib import Path

import mutagen
from streamrip.client import QobuzClient
from streamrip.config import Config
from streamrip.lyrics import write_sidecar

AUDIO_EXTENSIONS = {".flac", ".mp3", ".m4a", ".ogg", ".opus", ".wav", ".aiff"}


def audio_files(root):
    def failed(error):
        raise error

    for folder, dirs, names in os.walk(root, followlinks=False, onerror=failed):
        dirs[:] = sorted(d for d in dirs if not Path(folder, d).is_symlink())
        for name in sorted(names):
            path = Path(folder, name)
            if (
                path.suffix.lower() in AUDIO_EXTENSIONS
                and not path.is_symlink()
                and path.is_file()
            ):
                yield path


def track_id(path):
    # Mutagen opens the audio for reading. No save/delete/tag-writing API is used.
    audio = mutagen.File(path)
    if audio is None or not audio.tags:
        return None
    for key, value in audio.tags.items():
        normalized = key.casefold()
        if normalized not in {
            "qobuz_track_id",
            "txxx:qobuz_track_id",
            "----:com.apple.itunes:qobuz_track_id",
        }:
            continue
        values = getattr(value, "text", value)
        if not isinstance(values, (list, tuple)):
            values = [values]
        for item in values:
            if isinstance(item, bytes):
                item = item.decode("utf-8", errors="strict")
            item = str(item).strip()
            if re.fullmatch(r"[0-9]+", item):
                return item
    return None


def has_sidecar(path):
    # Preserve existing LRC files, including case variants and symlinks.
    target = path.with_suffix(".lrc").name.casefold()
    return any(name.casefold() == target for name in os.listdir(path.parent))


async def fetch(client, identifier):
    for attempt in range(3):
        try:
            return await client.get_lyrics(identifier)
        except asyncio.CancelledError:
            raise
        except Exception as error:
            if attempt == 2:
                raise
            # Never print exceptions containing request URLs or account details.
            print(f"RETRY id={identifier} ({type(error).__name__})", flush=True)
            await asyncio.sleep(5 * (attempt + 1))


async def backfill(root, client, *, dry_run=False, limit=0, interval=1.0):
    counts = Counter()
    consecutive_errors = 0
    try:
        for path in audio_files(root):
            # A limit counts eligible tracks queried, not untagged/skipped files.
            if limit and counts["queried"] >= limit:
                break
            counts["scanned"] += 1
            if counts["scanned"] % 250 == 0:
                print(
                    f"SCAN scanned={counts['scanned']} queried={counts['queried']}",
                    flush=True,
                )
            if has_sidecar(path):
                counts["existing_sidecar"] += 1
                continue
            try:
                identifier = track_id(path)
            except Exception as error:
                counts["unreadable_tags"] += 1
                print(
                    f"SKIP unreadable tags ({type(error).__name__}): {path}", flush=True
                )
                continue
            if identifier is None:
                counts["no_track_id"] += 1
                continue
            counts["queried"] += 1
            try:
                lyrics = await fetch(client, identifier)
                if not lyrics:
                    counts["unavailable"] += 1
                    print(f"NO LYRICS id={identifier}: {path}", flush=True)
                elif not lyrics.synced:
                    counts["no_timestamps"] += 1
                    print(f"SKIP no timestamps id={identifier}: {path}", flush=True)
                elif dry_run:
                    counts["would_write_lrc"] += 1
                    print(f"WOULD WRITE {path.with_suffix('.lrc')}", flush=True)
                elif has_sidecar(path):
                    counts["existing_sidecar"] += 1
                else:
                    # Only timestamped lyrics reach this call, so it writes LRC only.
                    # Streamrip publishes UTF-8 sidecars atomically, never overwriting.
                    target = write_sidecar(
                        str(path), lyrics, prefer_synced=True, overwrite=False
                    )
                    if target:
                        counts["written_" + target.suffix[1:]] += 1
                        print(f"WROTE {target}", flush=True)
                    else:
                        counts["existing_sidecar"] += 1
                consecutive_errors = 0
            except asyncio.CancelledError:
                raise
            except Exception as error:
                counts["errors"] += 1
                consecutive_errors += 1
                print(
                    f"ERROR id={identifier} ({type(error).__name__}): {path}",
                    flush=True,
                )
                if consecutive_errors >= 5:
                    raise RuntimeError(
                        "Stopping after five consecutive failures; rerun to resume"
                    ) from None
            print(
                "PROGRESS " + " ".join(f"{k}={v}" for k, v in sorted(counts.items())),
                flush=True,
            )
            await asyncio.sleep(interval)
    finally:
        print(
            "SUMMARY " + " ".join(f"{k}={v}" for k, v in sorted(counts.items())),
            flush=True,
        )
    return counts


def arguments():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--root",
        type=Path,
        required=True,
        help="Existing music library; no directories are created",
    )
    parser.add_argument(
        "--config", required=True, help="Existing Streamrip config, opened read-only"
    )
    parser.add_argument(
        "--dry-run",
        action="store_true",
        help="Read tags and fetch lyrics, but write no sidecars",
    )
    parser.add_argument(
        "--limit",
        type=int,
        default=0,
        help="Maximum eligible tracks to query; 0 means all",
    )
    parser.add_argument(
        "--interval",
        type=float,
        default=1.0,
        help="Seconds between tracks (minimum 0.5)",
    )
    args = parser.parse_args()
    if args.root.is_symlink() or not args.root.is_dir():
        parser.error("--root must be an existing real directory")
    if args.limit < 0 or not 0.5 <= args.interval <= 60:
        parser.error("--limit must be nonnegative and --interval between 0.5 and 60")
    return args


async def run(args):
    # Config and login state remain in memory; save_file() is never called.
    config = Config(args.config)
    config.session.downloads.requests_per_minute = 60
    client = QobuzClient(config)
    try:
        await client.login()
        print(
            f"Library: {args.root}; dry_run={args.dry_run}; existing LRC sidecars are always skipped",
            flush=True,
        )
        counts = await backfill(
            args.root,
            client,
            dry_run=args.dry_run,
            limit=args.limit,
            interval=args.interval,
        )
        return 1 if counts["errors"] else 0
    finally:
        if hasattr(client, "session"):
            await client.session.close()


def main():
    # Avoid upstream debug logs that can contain credentials, signatures, or text.
    logging.disable(logging.CRITICAL)
    args = arguments()
    try:
        return asyncio.run(run(args))
    except KeyboardInterrupt:
        print(
            "Stopped. Rerun the same command to resume; existing sidecars will be skipped.",
            flush=True,
        )
        return 130
    except Exception as error:
        print(
            f"Backfill stopped ({type(error).__name__}). No audio or config files were changed.",
            file=sys.stderr,
        )
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
