# alass (vendored for CineRoute)

This directory contains the upstream `alass` workspace, vendored so CineRoute
can build a pinned `alass-cli` binary into its container image and invoke it as
a subprocess for subtitle synchronization.

## Provenance

| | |
| --- | --- |
| Upstream | https://github.com/kaegi/alass |
| Pinned commit | `874f02d9577182752a0f969b6d6b98fd65bdf1fc` ("Auto detect subtitle encoding") |
| Version | `alass-cli` / `alass-core` 2.0.0 |
| License | GPL-3.0 (see `LICENSE`) |
| Vendored | `Cargo.toml`, `Cargo.lock`, `alass-cli/`, `alass-core/`, `LICENSE`, `README.md` |
| Removed | `.git`, `documentation/` (thesis PDFs), `statistics-helpers/`, `Makefile`, `MakefileWindows64`, `rustfmt.toml` — all irrelevant to building `alass-cli` |

No source changes have been made to the vendored code. If a change is ever
needed (for example a Rust toolchain fix), record it in the "Local patches"
section below and keep the patch minimal and clearly marked.

## Why it is a subprocess and not a library

`alass` is GPL-3.0. CineRoute runs the unmodified `alass-cli` program as a
separate process, which keeps CineRoute's own code outside the GPL's derivative
work scope. Do not link `alass-core` into the Go binary.

`alass-cli` only shells out to `ffmpeg`/`ffprobe` when the *reference* file is a
video. CineRoute always passes a normalized `.srt` reference, so `alass` itself
never needs ffmpeg or ffprobe; the image ships them because CineRoute uses them
to probe and extract subtitle streams.

## Build

```sh
cargo build --release --locked --bin alass-cli --manifest-path third_party/alass/alass-cli/Cargo.toml
```

The Docker build runs this in a `rust:1-alpine` stage (musl ⇒ static binary) for
the target platform and copies the resulting binary to `/usr/local/bin/alass`.
A C compiler is required because of the `webrtc-vad` C module.

## How CineRoute calls it

```sh
alass --encoding-ref UTF-8 --encoding-inc UTF-8 [--no-split] [--split-penalty N] \
      <reference.srt> <downloaded.srt> <output.srt>
```

stdout (which carries the progress, the guessed framerate ratio and the
"shifted block of N subtitles ... by ..." lines) is captured and parsed for the
attempt log and timing report.

## Updating

1. Fetch the desired upstream commit.
2. Replace `alass-cli/`, `alass-core/`, `Cargo.toml`, `Cargo.lock`, `LICENSE` and
   `README.md`, updating the pinned commit above.
3. Rebuild to confirm, then run CineRoute's subtitle tests.

## Local patches

None.
