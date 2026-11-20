#!/usr/bin/env bash
# Check the installed revision in a registry image without starting a container.
set -euo pipefail
if [[ $# != 3 ]]; then
  echo "Usage: $0 IMAGE@DIGEST PLATFORM STREAMRIP_COMMIT" >&2
  exit 2
fi
image="$1"
platform="$2"
expected="$3"
[[ "$image" =~ @sha256:[0-9a-f]{64}$ && "$expected" =~ ^[0-9a-f]{40}$ ]]

work="$(mktemp -d)"
container=""
cleanup() {
  if [[ -n "$container" ]]; then
    docker rm "$container" >/dev/null || true
  fi
  rm -rf -- "$work"
}
trap cleanup EXIT

container="$(docker create --pull=always --platform "$platform" "$image")"
docker cp "$container:/opt/streamrip/lib/." "$work/"
shopt -s nullglob
metadata=("$work"/python*/site-packages/streamrip-*.dist-info/direct_url.json)
if (( ${#metadata[@]} != 1 )); then
  echo "$platform: expected one Streamrip installation, found ${#metadata[@]}" >&2
  exit 1
fi
actual="$(jq -er '.vcs_info.commit_id' "${metadata[0]}")"
if [[ "$actual" != "$expected" ]]; then
  echo "$platform: image contains Streamrip $actual, expected $expected" >&2
  exit 1
fi
echo "$platform: verified Streamrip $actual in $image"
