# Bundled lrcscript

The Python runner and native Rust provider engine are vendored from the sibling
`lrcscript` repository at the commit in `REVISION`. To update after reviewing and
committing changes there, run `tools/update-lrcscript.sh ../lrcscript`.

CineRoute runs this as a separate subprocess, scoped to one imported release.
No SSH, Navidrome plugin host, account cookies, analytics, lyrics cache, or audio
rewrites are involved. Runtime Python is already included for streamrip. Docker
builds the helper for the image's Alpine/musl CPU architecture.

The engine includes MIT-licensed code adapted from J0R6IT0/navidrome-lyrics-plugin
and Myzel394/navidrome-musixmatch-plugin; see LICENSE, NOTICE and licenses/.
The local LRCGET checkout was reviewed for design ideas (request pacing,
retry handling and metadata reads); no GPL desktop application code is copied.
