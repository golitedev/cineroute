#!/usr/bin/env bash
# One-off helper using the installed image, isolated from the running service.
set -euo pipefail
script_dir="$(cd -- "$(dirname -- "$0")" && pwd)"
music_root=/mnt/disk4/media/music-main
config_path=/srv/docker/cineroute/data/music/streamrip/config.toml
test -d "$music_root"
test -f "$config_path"
image_id="$(docker inspect cineroute --format '{{.Image}}')"
exec docker run --rm --init --name cineroute-lyrics-backfill \
  --read-only --cap-drop ALL --security-opt no-new-privileges \
  --user "$(id -u):$(id -g)" \
  --tmpfs /tmp:rw,nosuid,nodev,noexec,size=64m \
  --env HOME=/tmp --env PYTHONDONTWRITEBYTECODE=1 \
  --mount "type=bind,src=$music_root,dst=/music" \
  --mount "type=bind,src=$config_path,dst=/config/streamrip.toml,readonly" \
  --mount "type=bind,src=$script_dir/backfill-qobuz-lyrics.py,dst=/tool/backfill.py,readonly" \
  --entrypoint /opt/streamrip/bin/python "$image_id" \
  -u /tool/backfill.py --root /music --config /config/streamrip.toml "$@"
