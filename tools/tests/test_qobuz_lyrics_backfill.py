import asyncio
import hashlib
import importlib.util
import tempfile
import unittest
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import AsyncMock, patch

from mutagen.flac import FLAC
from streamrip.lyrics import Lyrics

spec = importlib.util.spec_from_file_location(
    "backfill", Path(__file__).parents[1] / "backfill-qobuz-lyrics.py"
)
backfill = importlib.util.module_from_spec(spec)
spec.loader.exec_module(backfill)


class BackfillTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name)
        self.addCleanup(self.temp.cleanup)
        self.client = SimpleNamespace(
            get_lyrics=AsyncMock(return_value=Lyrics("Hola", "[00:00.00]Hola"))
        )

    def audio(self, name, identifier="123"):
        path = self.root / name
        # Minimal FLAC STREAMINFO; enough to exercise real Vorbis tag I/O.
        streaminfo = (
            b"\x10\x00\x10\x00"
            + bytes(6)
            + ((44100 << 44) | (1 << 41) | (15 << 36)).to_bytes(8, "big")
            + bytes(16)
        )
        path.write_bytes(b"fLaC\x80\x00\x00\x22" + streaminfo)
        audio = FLAC(path)
        if identifier is None:
            audio.pop("qobuz_track_id", None)
        else:
            audio["qobuz_track_id"] = identifier
        audio.save()
        return path

    def signature(self, path):
        stat = path.stat()
        return (
            hashlib.sha256(path.read_bytes()).hexdigest(),
            stat.st_mtime_ns,
            stat.st_size,
            stat.st_ino,
        )

    def run_backfill(self, **kwargs):
        return asyncio.run(
            backfill.backfill(self.root, self.client, interval=0, **kwargs)
        )

    def test_lrc_and_plain_fallback_leave_audio_identical(self):
        first = self.audio("01.flac")
        second = self.audio("02.flac", "456")
        before = {p: self.signature(p) for p in [first, second]}
        self.client.get_lyrics.side_effect = [
            Lyrics("Hola", "[00:00.00]Hola"),
            Lyrics("Plain lyrics"),
        ]
        counts = self.run_backfill()
        self.assertEqual(counts["written_lrc"], 1)
        self.assertEqual(counts["written_txt"], 1)
        self.assertEqual(first.with_suffix(".lrc").read_text(), "[00:00.00]Hola\n")
        self.assertEqual(second.with_suffix(".txt").read_text(), "Plain lyrics\n")
        self.assertEqual(before, {p: self.signature(p) for p in before})
        self.assertEqual(
            {p.name for p in self.root.iterdir()},
            {"01.flac", "02.flac", "01.lrc", "02.txt"},
        )
        counts = self.run_backfill()
        self.assertEqual(counts["existing_sidecar"], 2)
        self.assertEqual(self.client.get_lyrics.await_count, 2)

    def test_dry_run_writes_nothing_and_preserves_audio(self):
        path = self.audio("01.flac")
        before = self.signature(path)
        counts = self.run_backfill(dry_run=True)
        self.assertEqual(counts["would_write_lrc"], 1)
        self.assertEqual(before, self.signature(path))
        self.assertEqual(list(self.root.iterdir()), [path])

    def test_existing_formats_case_variants_and_symlinks_are_preserved(self):
        for stem, suffix in [("01", ".txt"), ("02", ".LRC"), ("03", ".lrc")]:
            self.audio(stem + ".flac")
            sidecar = self.root / (stem + suffix)
            if stem == "03":
                sidecar.symlink_to(self.root / "missing")
            else:
                sidecar.write_text("Existing lyrics")
        counts = self.run_backfill()
        self.assertEqual(counts["existing_sidecar"], 3)
        self.client.get_lyrics.assert_not_awaited()
        self.assertEqual((self.root / "01.txt").read_text(), "Existing lyrics")
        self.assertTrue((self.root / "03.lrc").is_symlink())

    def test_missing_or_invalid_ids_and_symlinked_audio_are_not_queried(self):
        for i, identifier in enumerate([None, "album-123", "123?other=1"]):
            self.audio(str(i) + ".flac", identifier)
        (self.root / "link.flac").symlink_to(self.root / "0.flac")
        counts = self.run_backfill()
        self.assertEqual(counts["no_track_id"], 3)
        self.client.get_lyrics.assert_not_awaited()

    def test_missing_lyrics_creates_nothing_and_limit_counts_queries(self):
        self.audio("01.flac", None)
        self.audio("02.flac")
        self.audio("03.flac", "456")
        self.client.get_lyrics.return_value = None
        counts = self.run_backfill(limit=1)
        self.assertEqual(counts["queried"], 1)
        self.assertEqual(counts["unavailable"], 1)
        self.assertEqual(len(list(self.root.iterdir())), 3)

    def test_repeated_failures_stop_without_audio_changes(self):
        paths = [self.audio(f"{i}.flac") for i in range(6)]
        before = {p: self.signature(p) for p in paths}
        with (
            patch.object(
                backfill, "fetch", AsyncMock(side_effect=RuntimeError("API down"))
            ),
            self.assertRaisesRegex(RuntimeError, "five consecutive"),
        ):
            self.run_backfill()
        self.assertEqual(before, {p: self.signature(p) for p in paths})
        self.assertEqual(len(list(self.root.iterdir())), 6)


if __name__ == "__main__":
    unittest.main()
