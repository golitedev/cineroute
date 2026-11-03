#!/usr/bin/env python3
"""Download lyrics beside your music, in resumable folder batches."""
from __future__ import annotations

import argparse
import collections
import contextlib
import errno
import io
import json
import math
import os
from pathlib import Path
import shlex
import subprocess
import stat
import sys
import tarfile
import tempfile
import time
import uuid

ROOT = '/mnt/disk4/data/media/music'
PROVIDERS = ('lrclib', 'lyrics.ovh', 'lrcmux', 'kugou', 'netease', 'qqmusic', 'stixoi', 'genie', 'binilyrics', 'musixmatch')
VERIFIED_PROVIDERS = frozenset(('lrclib', 'kugou', 'netease', 'qqmusic', 'musixmatch'))
AUDIO = {'.flac', '.mp3', '.m4a', '.ogg', '.opus', '.wav', '.aiff', '.aif', '.alac', '.ape', '.wv'}
EXTENSIONS = {'plain': '.txt', 'lrc': '.lrc', 'elrc': '.elrc', 'ttml': '.ttml', 'srt': '.srt', 'lyricsfile': '.yaml', 'instrumental': '.txt'}
SIDECARS = set(EXTENSIONS.values()) | {'.yml'}
HERE = Path(__file__).resolve().parent


class Failure(Exception):
    pass


class EngineFailure(Failure):
    """The helper or its protocol failed, rather than one track's metadata."""


def scan(root):
    """One top-level folder including nested files; root-level audio shares the "." group."""
    folders = collections.defaultdict(list)
    def fail(error):
        raise error
    for directory, dirs, files in os.walk(root, followlinks=False, onerror=fail):
        dirs[:] = sorted(d for d in dirs if not d.startswith('.') and not (Path(directory) / d).is_symlink())
        for name in sorted(files):
            path = Path(directory) / name
            if path.suffix.lower() not in AUDIO or path.is_symlink():
                continue
            parts = path.relative_to(root).parts
            folders[parts[0] if len(parts) > 1 else '.'].append(path)
    return dict(sorted(folders.items(), key=lambda item: item[0].casefold()))


def sidecars(path):
    # Include dangling links to avoid replacing a user-managed sidecar link.
    return [path.with_suffix(ext) for ext in sorted(SIDECARS) if os.path.lexists(path.with_suffix(ext))]


def pending(path, args):
    return args.overwrite or not sidecars(path)


def choose(folders, args):
    selected = {}
    for name, paths in folders.items():
        if args.match and args.match.casefold() not in name.casefold():
            continue
        todo = [p for p in paths if pending(p, args)]
        if not todo:
            continue
        if args.limit is not None and len(selected) >= args.limit:
            break
        selected[name] = todo
    return selected


def write_sidecar(path, lyrics, overwrite=False):
    extension = EXTENSIONS[lyrics['kind']]
    text = lyrics['text'].strip()
    if not text:
        raise Failure('refusing to write empty lyrics')
    target = path.with_suffix(extension)
    if target.is_symlink():
        raise FileExistsError(f'refusing to replace sidecar symlink: {target}')
    fd, temp = tempfile.mkstemp(prefix='.lrcscript-', suffix='.tmp', dir=path.parent)
    try:
        with os.fdopen(fd, 'w', encoding='utf-8', newline='\n') as output:
            output.write(text + '\n')
            output.flush()
            os.fsync(output.fileno())
            os.fchmod(output.fileno(), 0o644)
        if overwrite:
            os.replace(temp, target)
        else:
            if sidecars(path):
                raise FileExistsError(f'sidecar already exists for {path.name}')
            # Atomic publish without truncation; the music file is never opened for writing.
            os.link(temp, target)
        return target
    finally:
        with contextlib.suppress(FileNotFoundError):
            os.unlink(temp)


class Engine:
    def __init__(self, binary):
        self.process = subprocess.Popen([str(binary)], stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                        text=True, encoding='utf-8', bufsize=1)

    def request(self, request):
        try:
            self.process.stdin.write(json.dumps(request, ensure_ascii=False) + '\n')
            self.process.stdin.flush()
            line = self.process.stdout.readline()
            if not line:
                raise EngineFailure('lyrics engine exited unexpectedly')
            reply = json.loads(line)
            if not isinstance(reply, dict):
                raise EngineFailure('lyrics engine returned an invalid response')
            if 'error' in reply:
                raise (EngineFailure if reply.get('fatal') else Failure)(reply['error'])
            return reply
        except (OSError, ValueError) as error:
            raise EngineFailure(f'lyrics engine communication failed: {error}') from None

    def lookup(self, request):
        return self.request(request)

    def metadata(self, path):
        reply = self.request({'metadata_path': str(path)})
        if not isinstance(reply.get('track'), dict):
            raise EngineFailure('lyrics engine returned invalid metadata')
        return reply['track']

    def close(self):
        with contextlib.suppress(OSError):
            self.process.stdin.close()
        if self.process.poll() is None:
            self.process.terminate()
        try:
            self.process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            self.process.kill()
            self.process.wait()
        self.process.stdout.close()


class Report:
    """Append-only audit output. Never read to select or skip tracks."""
    def __init__(self, path):
        self.file = None
        self.run_id = uuid.uuid4().hex
        if path is None:
            return
        path = path.expanduser().absolute()
        if path.suffix.lower() not in {'.jsonl', '.ndjson', '.json'}:
            raise Failure('report must use a .jsonl, .ndjson, or .json filename')
        if path.is_symlink():
            raise Failure('report must be a regular file, not a symlink')
        path.parent.mkdir(parents=True, exist_ok=True)
        fd = os.open(path, os.O_RDWR | os.O_APPEND | os.O_CREAT | os.O_NOFOLLOW, 0o644)
        try:
            if not stat.S_ISREG(os.fstat(fd).st_mode):
                raise Failure('report must be a regular file')
            with os.fdopen(os.dup(fd), 'r', encoding='utf-8') as existing:
                first = existing.readline()
                if first:
                    try:
                        record = json.loads(first)
                    except ValueError:
                        raise Failure('existing report is not lrcscript JSON Lines; choose a new report path') from None
                    if not isinstance(record, dict) or record.get('tool') != 'lrcscript':
                        raise Failure('refusing to append to an unrelated existing file')
            size = os.fstat(fd).st_size
            if size:
                os.lseek(fd, size - 1, os.SEEK_SET)
                ends_newline = os.read(fd, 1) == b'\n'
                end = size - int(ends_newline)
                start, chunks = end, []
                while start:
                    offset = max(0, start - 4096)
                    os.lseek(fd, offset, os.SEEK_SET)
                    chunk = os.read(fd, start - offset)
                    boundary = chunk.rfind(b'\n')
                    chunks.insert(0, chunk[boundary + 1:])
                    start = offset + boundary + 1 if boundary >= 0 else offset
                    if boundary >= 0:
                        break
                try:
                    json.loads(b''.join(chunks))
                except ValueError:
                    # Only an unfinished trailing record can be lost on abrupt termination.
                    os.ftruncate(fd, start)
                    os.fsync(fd)
                else:
                    if not ends_newline:
                        os.write(fd, b'\n')
            self.file = os.fdopen(fd, 'a', encoding='utf-8')
        except BaseException:
            os.close(fd)
            raise

    def write(self, event, **data):
        if self.file:
            record = dict(tool='lrcscript', run_id=self.run_id, event=event, time=time.time(), **data)
            self.file.write(json.dumps(record, ensure_ascii=False) + '\n')
            self.file.flush()
            os.fsync(self.file.fileno())

    def close(self):
        if self.file:
            self.file.close()


def lookup_with_backoff(engine, request, remaining_wait):
    """Wait only when every usable provider is temporarily unavailable."""
    waited = 0.0
    while True:
        reply = engine.lookup(request)
        retry_after = reply.get('retry_after')
        if reply.get('lyrics') or not reply.get('unavailable') or not retry_after:
            return reply, waited
        seconds = max(1.0, float(retry_after))
        if seconds > remaining_wait - waited:
            return reply, waited
        print(f'  All providers unavailable; retrying this track in {seconds:g}s', flush=True)
        # Keep long waits visible and interruptible.
        left = seconds
        while left > 0:
            chunk = min(left, 30.0)
            time.sleep(chunk)
            waited += chunk
            left -= chunk


def positive(value):
    result = float(value)
    if not math.isfinite(result) or result <= 0:
        raise argparse.ArgumentTypeError('must be finite and greater than zero')
    return result


def nonnegative(value):
    result = int(value)
    if result < 0:
        raise argparse.ArgumentTypeError('must be zero or greater')
    return result


def provider_list(value):
    result = list(dict.fromkeys(p.strip() for p in value.split(',') if p.strip()))
    if not result or any(p not in PROVIDERS for p in result):
        raise argparse.ArgumentTypeError('choose from: ' + ','.join(PROVIDERS))
    return result


def parser():
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.ArgumentDefaultsHelpFormatter)
    p.add_argument('command', choices=['scan', 'plan', 'check', 'download'], help='inventory, preview, check one track without saving, or download')
    p.add_argument('--root', type=Path, default=Path(ROOT), help='music root on the execution machine')
    p.add_argument('--ssh', metavar='HOST', help='run on SSH host, e.g. nas, using a temporary copy of this tool')
    p.add_argument('--limit', type=nonnegative, help='maximum pending folders')
    p.add_argument('--all', action='store_true', help='explicitly download every pending folder')
    p.add_argument('--match', default='', help='case-insensitive substring of folder name')
    p.add_argument('--providers', type=provider_list, default=list(PROVIDERS), help='comma-separated provider order; all account-free providers enabled')
    p.add_argument('--mode', choices=['sync', 'priority', 'type'], default='type', help='prefer best synchronization, first result, or LRC-first format order')
    p.add_argument('--verified-only', action='store_true', help='use only providers that validate returned artist/title identity')
    p.add_argument('--synced-only', action='store_true', help='disable plain .txt results (instrumental markers may still be returned)')
    p.add_argument('--require-album-match', action='store_true', help='require album matching where supported by the provider')
    p.add_argument('--strip-section-labels', action='store_true', help='use upstream cleanup for [Verse], [Chorus], etc.')
    p.add_argument('--overwrite', action='store_true', help='allow replacing existing sidecars')
    p.add_argument('--duration-tolerance', type=positive, default=3.0, help='allowed duration mismatch in seconds')
    p.add_argument('--delay', type=positive, default=1.0, help='minimum seconds between API requests')
    p.add_argument('--timeout', type=nonnegative, default=15, help='timeout per HTTP request in seconds (must be >0)')
    p.add_argument('--retries', type=nonnegative, default=1, help='additional attempts for network errors and HTTP 5xx')
    p.add_argument('--outage-wait', type=nonnegative, default=300, help='total seconds allowed waiting for provider outages per run; zero disables waiting')
    p.add_argument('--report', type=Path, help='append-only JSON Lines audit report on the execution machine')
    p.add_argument('--engine', type=Path, default=HERE / 'bin/lyrics-fetch', help='native helper built by ./build.sh')
    for name in ('lrclib', 'lrcmux', 'lyrics-ovh', 'musixmatch'):
        p.add_argument('--' + name + '-url', help='optional custom provider base URL')
    return p


def remote(args, argv):
    if args.ssh.startswith('-') or any(c not in 'abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_.@:-' for c in args.ssh):
        raise Failure('use an SSH config alias such as nas')
    forwarded, skip = [], False
    for value in argv:
        if skip:
            skip = False
        elif value in ('--ssh', '--engine'):
            skip = True
        elif not value.startswith(('--ssh=', '--engine=')):
            forwarded.append(value)
    if args.command in ('check', 'download') and not args.engine.exists():
        raise Failure('build the engine first: ./build.sh')
    buffer = io.BytesIO()
    with tarfile.open(fileobj=buffer, mode='w:gz') as archive:
        archive.add(HERE / 'lrcscript.py', arcname='lrcscript.py')
        if args.command in ('check', 'download'):
            archive.add(args.engine, arcname='bin/lyrics-fetch')
    # The archive contains only our two fixed filenames. No shell interpolation of paths.
    bootstrap = '''import sys,tarfile,tempfile,subprocess,os
with tempfile.TemporaryDirectory(prefix="lrcscript-") as d:
 with tarfile.open(fileobj=sys.stdin.buffer,mode="r|gz") as t:
  for m in t:
   if m.name not in ("lrcscript.py","bin/lyrics-fetch") or not m.isfile(): raise ValueError("unexpected archive entry")
   p=os.path.join(d,m.name); os.makedirs(os.path.dirname(p),exist_ok=True)
   with open(p,"wb") as f: f.write(t.extractfile(m).read())
   os.chmod(p,0o755)
 rc=subprocess.call([sys.executable,os.path.join(d,"lrcscript.py"),*sys.argv[1:]],stdin=subprocess.DEVNULL)
sys.exit(rc)
'''
    command = shlex.join(['python3', '-c', bootstrap] + forwarded)
    return subprocess.run(['ssh', '-o', 'BatchMode=yes', '-o', 'ConnectTimeout=10', args.ssh, command], input=buffer.getvalue()).returncode


def run(args):
    root = args.root.expanduser().resolve()
    if not root.is_dir():
        raise Failure(f'library directory does not exist: {root}')
    if args.command in ('check', 'download'):
        args.engine = args.engine.expanduser().resolve()
        if not args.engine.is_file():
            raise Failure('build the engine first: ./build.sh')
    params = {}
    for provider, value in [('lrclib', args.lrclib_url), ('lrcmux', args.lrcmux_url), ('lyrics.ovh', args.lyrics_ovh_url), ('musixmatch', args.musixmatch_url)]:
        if value:
            params[provider] = {'baseUrl': value.rstrip('/')}
    if args.verified_only:
        args.providers = [p for p in args.providers if p in VERIFIED_PROVIDERS]
        if not args.providers:
            raise Failure('--verified-only leaves no providers with artist/title validation')
    settings = dict(providers=args.providers, mode=args.mode, synced_only=args.synced_only,
                    strip_section_labels=args.strip_section_labels, require_album_match=args.require_album_match,
                    tolerance=args.duration_tolerance, params=params)
    folders = scan(root)
    print(f'Library: {root}\n{len(folders)} folders, {sum(map(len, folders.values()))} tracks', flush=True)
    if args.command == 'scan':
        displayed = 0
        for name, paths in folders.items():
            if args.match.casefold() not in name.casefold():
                continue
            if args.limit is not None and displayed >= args.limit:
                break
            print(f'{len(paths):3} tracks  {name}')
            displayed += 1
        return 0
    chosen = choose(folders, args)
    print(f'Selected {len(chosen)} folders, {sum(map(len, chosen.values()))} pending tracks', flush=True)
    print('Providers: ' + ', '.join(args.providers) + f'; mode={args.mode}', flush=True)
    if args.command == 'plan':
        for name, paths in chosen.items():
            print(f'\nFOLDER {name} ({len(paths)}/{len(folders[name])} pending)')
            for path in paths:
                print('  ' + str(path.relative_to(root)))
        return 0
    counts = collections.Counter()
    exit_code, fatal, interrupted = 0, False, False
    remaining_wait = float(args.outage_wait)
    selected_tracks = sum(map(len, chosen.values()))
    completed = 0
    with contextlib.closing(Report(args.report)) as report:
        report.write('start', root=str(root), settings=settings, selected_folders=len(chosen), selected_tracks=selected_tracks)
        try:
            with contextlib.closing(Engine(args.engine)) as engine:
                for name, paths in chosen.items():
                    print(f'\n[folder] {name}', flush=True)
                    for path in paths:
                        status, detail, provider, format = '', '', None, None
                        attempts = []
                        try:
                            # A second run may have created a sidecar after initial selection.
                            if not pending(path, args):
                                raise FileExistsError('sidecar appeared after selection')
                            track = engine.metadata(path)
                            print(f'  Lookup: {track["artist"]} — {track["title"]}', flush=True)
                            request = dict(settings, track=track, delay=args.delay, timeout=args.timeout, retries=args.retries)
                            reply, waited = lookup_with_backoff(engine, request, remaining_wait)
                            remaining_wait = max(0, remaining_wait - waited)
                            attempts = reply['attempts']
                            for attempt in attempts:
                                print(f'  {attempt["provider"]}: {attempt["status"]}' +
                                      (f' ({attempt["error"]})' if attempt.get('error') else ''), flush=True)
                            lyrics = reply.get('lyrics')
                            if lyrics:
                                provider, format = lyrics['provider'], lyrics['kind']
                                if args.command == 'download':
                                    detail = str(write_sidecar(path, lyrics, args.overwrite).relative_to(root))
                                    status = 'saved'
                                else:
                                    status, detail = 'found', f'{format}, {len(lyrics["text"].splitlines())} lines; no file written'
                            elif reply.get('unavailable') or any(a.get('retry_after') for a in attempts):
                                status, detail = 'deferred', 'one or more providers temporarily unavailable; retry on a later run'
                                exit_code = 1
                            else:
                                status, detail = 'missing', 'no verified lyrics found'
                        except EngineFailure as error:
                            status, detail, fatal = 'error', str(error), True
                            exit_code = 1
                        except FileExistsError as error:
                            status, detail = 'skipped', str(error)
                        except OSError as error:
                            status, detail = 'error', str(error)
                            fatal |= error.errno in (errno.ENOSPC, errno.EDQUOT, errno.EROFS)
                            exit_code = 1
                        except (Failure, KeyError, ValueError, TypeError) as error:
                            status, detail = 'error', str(error)
                            exit_code = 1
                        completed += 1
                        counts[status] += 1
                        # Persist before printing, so disconnects do not lose completed results.
                        report.write('track', path=str(path.relative_to(root)), folder=name, status=status,
                                     provider=provider, format=format, detail=detail, attempts=attempts)
                        print(f'  {status.upper()}: {path.name}: {detail}', flush=True)
                        if fatal or args.command == 'check':
                            break
                    if fatal or args.command == 'check':
                        break
        except KeyboardInterrupt:
            interrupted, exit_code = True, 130
            print('\nInterrupted; completed sidecars and report entries are retained.', flush=True)
        except OSError:
            # A failed report write must stop further writes to the library.
            fatal, exit_code = True, 1
            raise
        finally:
            report.write('end', exit_code=exit_code, counts=dict(counts), attempted_tracks=completed,
                         selected_tracks=selected_tracks, pass_complete=not fatal and not interrupted and completed == selected_tracks)
            print('\nResults: ' + ', '.join(f'{k}={v}' for k, v in sorted(counts.items())) +
                  f'; attempted {completed}/{selected_tracks}', flush=True)
    return exit_code


def main(argv=None):
    argv = list(sys.argv[1:] if argv is None else argv)
    p = parser()
    args = p.parse_args(argv)
    if args.timeout == 0:
        p.error('--timeout must be greater than zero')
    if args.command == 'download' and not (args.all or args.limit is not None):
        p.error('choose --limit or --all explicitly')
    if args.command in ('plan', 'check') and args.limit is None and not args.all:
        args.limit = 5 if args.command == 'plan' else 1
    try:
        return remote(args, argv) if args.ssh else run(args)
    except KeyboardInterrupt:
        return 130
    except (Failure, OSError) as error:
        print(f'Error: {error}', file=sys.stderr)
        return 1


if __name__ == '__main__':
    sys.exit(main())
