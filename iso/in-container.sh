#!/usr/bin/env bash
# Runs iso/build.sh in a Debian 13 container, so the ISO builds on any
# machine with Docker or Podman. It takes build.sh's options. The container
# is privileged because live-build mounts file systems and changes root into
# the system it builds; it runs for the image's architecture, emulated when
# that isn't the machine's own.
#
#   iso/in-container.sh --arch amd64 --binary bin/racksail-linux-amd64 --version 1.2.3
set -euo pipefail

repo=$(cd "$(dirname "$0")/.." && pwd)
image=${ISO_BUILDER_IMAGE:-docker.io/library/debian:trixie}
engine=${CONTAINER_ENGINE:-}
if [ -z "$engine" ]; then
	for candidate in docker podman; do
		if command -v "$candidate" >/dev/null; then
			engine=$candidate
			break
		fi
	done
fi
[ -n "$engine" ] || {
	echo "iso/in-container.sh: building the ISO needs Docker or Podman, or Debian 13 to run iso/build.sh directly" >&2
	exit 1
}

arch=
args=("$@")
while [ $# -gt 0 ]; do
	if [ "$1" = --arch ] && [ $# -gt 1 ]; then
		arch=$2
	fi
	shift
done
[ -n "$arch" ] || {
	echo "iso/in-container.sh: --arch is required" >&2
	exit 1
}

# The quoted script runs inside the container, with the options as its
# arguments.
# shellcheck disable=SC2016
exec "$engine" run --rm --privileged --platform "linux/$arch" \
	--volume "$repo:/src" --workdir /src \
	--env SOURCE_DATE_EPOCH="${SOURCE_DATE_EPOCH:-}" \
	--env HOST_UID="$(id -u)" --env HOST_GID="$(id -g)" \
	"$image" bash -c '
		set -euo pipefail
		export DEBIAN_FRONTEND=noninteractive
		apt-get update -qq
		apt-get install -qq -y --no-install-recommends \
			live-build librsvg2-bin grub-common fonts-inter wget ca-certificates git xz-utils >/dev/null
		exec iso/build.sh "$@"
	' bash "${args[@]}"
