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

    def test_only_timed_lyrics_get_sidecars_and_audio_is_identical(self):
        first = self.audio("01.flac")
        second = self.audio("02.flac", "456")
        before = {p: self.signature(p) for p in [first, second]}
        self.client.get_lyrics.side_effect = [
            Lyrics("Hola", "[00:00.00]Hola"),
            Lyrics("Plain lyrics"),
        ]
        counts = self.run_backfill()
        self.assertEqual(counts["written_lrc"], 1)
        self.assertEqual(counts["no_timestamps"], 1)
        self.assertEqual(counts["written_txt"], 0)
        self.assertEqual(first.with_suffix(".lrc").read_text(), "[00:00.00]Hola\n")
        self.assertFalse(second.with_suffix(".txt").exists())
        self.assertFalse(second.with_suffix(".lrc").exists())
        self.assertEqual(before, {p: self.signature(p) for p in before})
        self.assertEqual(
            {p.name for p in self.root.iterdir()},
            {"01.flac", "02.flac", "01.lrc"},
        )
        self.client.get_lyrics.side_effect = None
        self.client.get_lyrics.return_value = Lyrics("Plain lyrics")
        counts = self.run_backfill()
        self.assertEqual(counts["existing_sidecar"], 1)
        self.assertEqual(counts["no_timestamps"], 1)
        self.assertEqual(self.client.get_lyrics.await_count, 3)

    def test_dry_run_writes_nothing_and_preserves_audio(self):
        path = self.audio("01.flac")
        before = self.signature(path)
        counts = self.run_backfill(dry_run=True)
        self.assertEqual(counts["would_write_lrc"], 1)
        self.assertEqual(before, self.signature(path))
        self.assertEqual(list(self.root.iterdir()), [path])

    def test_dry_run_skips_plain_lyrics_without_promising_a_sidecar(self):
        path = self.audio("01.flac")
        self.client.get_lyrics.return_value = Lyrics("Plain lyrics")
        counts = self.run_backfill(dry_run=True)
        self.assertEqual(counts["no_timestamps"], 1)
        self.assertEqual(counts["would_write_lrc"], 0)
        self.assertEqual(list(self.root.iterdir()), [path])

    def test_only_lrc_blocks_new_output_and_existing_files_are_preserved(self):
        for stem, suffix in [("01", ".txt"), ("02", ".LRC"), ("03", ".lrc")]:
            self.audio(stem + ".flac")
            sidecar = self.root / (stem + suffix)
            if stem == "03":
                sidecar.symlink_to(self.root / "missing")
            else:
                sidecar.write_text("Existing lyrics")
        counts = self.run_backfill()
        self.assertEqual(counts["existing_sidecar"], 2)
        self.assertEqual(counts["written_lrc"], 1)
        self.client.get_lyrics.assert_awaited_once()
        self.assertTrue((self.root / "01.lrc").exists())
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

    def test_known_qobuz_catalog_error_is_confirmed_without_retries(self):
        client = SimpleNamespace(
            get_lyrics=AsyncMock(
                side_effect=RuntimeError("Qobuz lyrics lookup returned HTTP 400")
            ),
            secret="test-secret",
            config=SimpleNamespace(
                session=SimpleNamespace(qobuz=SimpleNamespace(app_id="app"))
            ),
            _api_request=AsyncMock(
                return_value=(
                    400,
                    {
                        "code": 0,
                        "message": "You must pass at least one parameter when using an IN() condition.",
                    },
                )
            ),
        )
        with self.assertRaises(backfill.QobuzCatalogLyricsError):
            asyncio.run(backfill.fetch(client, "822602"))
        client.get_lyrics.assert_awaited_once_with("822602")
        client._api_request.assert_awaited_once()
        endpoint, params = client._api_request.call_args.args
        self.assertEqual(endpoint, "track/lyricsUrl")
        self.assertEqual(params["track_id"], "822602")

    def test_other_bad_requests_still_retry_and_fail(self):
        client = SimpleNamespace(
            get_lyrics=AsyncMock(
                side_effect=RuntimeError("Qobuz lyrics lookup returned HTTP 400")
            )
        )
        with (
            patch.object(
                backfill, "known_catalog_error", AsyncMock(return_value=False)
            ),
            patch.object(backfill.asyncio, "sleep", AsyncMock()),
            self.assertRaisesRegex(RuntimeError, "HTTP 400"),
        ):
            asyncio.run(backfill.fetch(client, "123"))
        self.assertEqual(client.get_lyrics.await_count, 3)

    def test_catalog_failures_do_not_stop_an_album_or_write_files(self):
        paths = [self.audio(f"{i}.flac") for i in range(6)]
        before = {p: self.signature(p) for p in paths}
        with patch.object(
            backfill, "fetch", AsyncMock(side_effect=backfill.QobuzCatalogLyricsError)
        ):
            counts = self.run_backfill()
        self.assertEqual(counts["catalog_errors"], 6)
        self.assertEqual(counts["errors"], 0)
        self.assertEqual(before, {p: self.signature(p) for p in paths})
        self.assertEqual(len(list(self.root.iterdir())), 6)

    def test_file_disappearing_during_scan_is_skipped(self):
        missing = self.audio("01.flac")
        good = self.audio("02.flac")
        missing.unlink()
        with patch.object(backfill, "audio_files", return_value=iter([missing, good])):
            counts = self.run_backfill()
        self.assertEqual(counts["disappeared"], 1)
        self.assertEqual(counts["written_lrc"], 1)
        self.assertTrue(good.with_suffix(".lrc").exists())

    def test_directory_disappearing_during_walk_is_skipped(self):
        path = self.audio("01.flac")

        def walker(root, followlinks, onerror):
            onerror(FileNotFoundError(2, "Missing directory", str(root / "gone")))
            yield str(root), [], [path.name]

        with patch.object(backfill.os, "walk", side_effect=walker):
            self.assertEqual(list(backfill.audio_files(self.root)), [path])
        self.assertFalse(backfill.has_sidecar(self.root / "gone" / "track.flac"))

    def test_missing_sidecar_directory_is_not_an_api_failure(self):
        self.audio("01.flac")
        with patch.object(backfill, "write_sidecar", side_effect=FileNotFoundError()):
            counts = self.run_backfill()
        self.assertEqual(counts["disappeared"], 1)
        self.assertEqual(counts["errors"], 0)
        self.assertEqual(len(list(self.root.iterdir())), 1)


if __name__ == "__main__":
    unittest.main()
