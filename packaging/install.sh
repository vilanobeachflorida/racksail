#!/bin/sh
# Turn a fresh Debian 13 server into a Racksail server.
#
# Download the script, read it, then run it as root:
#
#   curl -fsSLO https://github.com/vilanobeachflorida/racksail/releases/latest/download/install.sh
#   sudo sh install.sh
#
# It checks that the machine runs Debian 13 on amd64 or arm64, downloads the
# release for it, verifies the release's Sigstore signatures and SHA-256
# checksum, installs the racksail binary and hands over to
# "racksail install --convert", which sets the server up. Nothing it
# downloads runs before it has been verified.

set -eu

REPOSITORY=vilanobeachflorida/racksail

# cosign verifies the signatures. A cosign already on the PATH is used as it
# is; otherwise this release is downloaded, from GitHub or the mirror, and
# checked against these checksums before it runs.
# renovate: datasource=github-releases depName=sigstore/cosign
COSIGN_VERSION=v3.1.3
COSIGN_SHA256_AMD64=4629c757b7618056f8ddd7e2625ae9fdd94c0372a65049520bc7d9df9efc7f71
COSIGN_SHA256_ARM64=c5d324e091826b0d7a78eb16fef316450b4eb9aaec045611c08ba06f5e73220a

# Sigstore's trusted root holds the keys the signatures are checked against.
# cosign fetches the current one from Sigstore; through a mirror, the script
# uses this one instead, which the mirror serves under its checksum, as
# https://tuf-repo-cdn.sigstore.dev/targets/<checksum>.trusted_root.json
# does, so installing needs nothing from GitHub or Sigstore. CI keeps it
# current with go run ./packaging/release check-pins.
SIGSTORE_ROOT_SHA256=6494e21ea73fa7ee769f85f57d5a3e6a08725eae1e38c755fc3517c9e6bc0b66

# Releases are signed by the release workflow, from a version tag or, for
# nightly builds, from main.
WORKFLOW="https://github.com/${REPOSITORY}/.github/workflows/release.yml"
OIDC_ISSUER=https://token.actions.githubusercontent.com

# The binary's path on the server, which its services run.
BINARY=/usr/local/bin/racksail

# RACKSAIL_ROOT puts everything under another directory, for tests and for
# preparing images.
ROOT=${RACKSAIL_ROOT:-}

usage() {
	cat <<'EOF'
Usage: sh install.sh [options]

Turns this Debian 13 server into a Racksail server: downloads the release,
verifies its signatures and checksum, installs /usr/local/bin/racksail and
runs "racksail install --convert".

Options:
  --version VERSION   install this version instead of the newest on the channel
  --channel CHANNEL   stable (the default), beta or nightly
  --hostname NAME     set the server's hostname, such as panel.example.com
  --mirror URL        download releases, cosign and Sigstore's trusted root
                      from a mirror instead of GitHub; it needs --version
  --dry-run           show what converting the server would do, and stop
  --reinstall         replace a racksail binary that is already installed,
                      and restart Racksail's services
  --yes               don't ask before converting the server
  --help              show this help
EOF
}

say() {
	printf '%s\n' "$*" >&2
}

die() {
	say "install.sh: $*"
	exit 1
}

VERSION=
CHANNEL=stable
HOSTNAME_ANSWER=
MIRROR=
DRY_RUN=
REINSTALL=
YES=

while [ $# -gt 0 ]; do
	case $1 in
	--version)
		[ $# -ge 2 ] || die "--version needs a value"
		VERSION=$2
		shift 2
		;;
	--channel)
		[ $# -ge 2 ] || die "--channel needs a value"
		CHANNEL=$2
		shift 2
		;;
	--hostname)
		[ $# -ge 2 ] || die "--hostname needs a value"
		HOSTNAME_ANSWER=$2
		shift 2
		;;
	--mirror)
		[ $# -ge 2 ] || die "--mirror needs a value"
		MIRROR=${2%/}
		shift 2
		;;
	--dry-run)
		DRY_RUN=1
		shift
		;;
	--reinstall)
		REINSTALL=1
		shift
		;;
	--yes | -y)
		YES=1
		shift
		;;
	--help | -h)
		usage
		exit 0
		;;
	*)
		usage >&2
		die "unknown option $1"
		;;
	esac
done

case $CHANNEL in
stable | beta | nightly) ;;
*) die "the channel is stable, beta or nightly, not $CHANNEL" ;;
esac
case $MIRROR in
'' | https://*) ;;
*) die "the mirror must be an https:// URL" ;;
esac
if [ -n "$HOSTNAME_ANSWER" ] &&
	! printf '%s\n' "$HOSTNAME_ANSWER" | grep -Eq '^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)*$'; then
	die "$HOSTNAME_ANSWER isn't a hostname such as panel.example.com; use lowercase letters, digits, hyphens and dots"
fi
if [ -n "$VERSION" ]; then
	VERSION=${VERSION#v}
	if ! printf '%s\n' "$VERSION" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$'; then
		die "$VERSION isn't a version such as 1.2.3"
	fi
fi

# Checks before anything is downloaded.

[ "$(id -u)" = 0 ] || die "run this script as root, for example with sudo sh install.sh"

OS_RELEASE=$ROOT/etc/os-release
[ -r "$OS_RELEASE" ] || die "$OS_RELEASE is missing, so this doesn't look like Debian 13"
# shellcheck source=/dev/null
OS_ID=$(. "$OS_RELEASE" && printf '%s' "${ID:-}")
# shellcheck source=/dev/null
OS_VERSION=$(. "$OS_RELEASE" && printf '%s' "${VERSION_ID:-}")
# shellcheck source=/dev/null
OS_NAME=$(. "$OS_RELEASE" && printf '%s' "${PRETTY_NAME:-unknown}")
if [ "$OS_ID" != debian ] || [ "$OS_VERSION" != 13 ]; then
	die "Racksail needs Debian 13 (trixie), and this is $OS_NAME"
fi

case $(uname -m) in
x86_64 | amd64) ARCH=amd64 ;;
aarch64 | arm64) ARCH=arm64 ;;
*) die "Racksail runs on amd64 and arm64, not $(uname -m)" ;;
esac

if [ -e "$ROOT/usr/bin/racksail" ]; then
	die "Racksail is installed from its Debian package, which apt keeps up to date; this script is for servers without it"
fi
if [ -e "$ROOT$BINARY" ] && [ -z "$REINSTALL" ]; then
	die "Racksail is already installed at $BINARY, and updates itself from the panel; use --reinstall to replace it"
fi

if command -v curl >/dev/null 2>&1; then
	DOWNLOADER=curl
elif command -v wget >/dev/null 2>&1; then
	DOWNLOADER=wget
else
	die "downloading needs curl or wget; install one with apt install curl"
fi

# fetch URL FILE downloads over HTTPS only, redirects included.
fetch() {
	if [ "$DOWNLOADER" = curl ]; then
		curl --fail --silent --show-error --location --proto '=https' --tlsv1.2 --retry 3 --output "$2" "$1" ||
			die "couldn't download $1"
	else
		wget --quiet --https-only --tries=3 --output-document="$2" "$1" ||
			die "couldn't download $1"
	fi
}

sha256() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | cut -d ' ' -f 1
	else
		shasum -a 256 "$1" | cut -d ' ' -f 1
	fi
}

# newest prints the highest of the versions on its input, by semantic
# versioning: 1.3.0 is newer than 1.3.0-beta.2, which is newer than
# 1.3.0-beta.1.
newest() {
	awk '
	function numeric(s) { return s ~ /^[0-9]+$/ }
	function cmpid(x, y) {
		if (numeric(x) && numeric(y)) { x += 0; y += 0; return x < y ? -1 : x > y }
		if (numeric(x)) return -1
		if (numeric(y)) return 1
		return x < y ? -1 : x > y
	}
	function cmp(a, b,    i, r, ca, cb, pa, pb, xa, xb, na, nb) {
		sub(/^v/, "", a); sub(/^v/, "", b)
		ca = a; pa = ""; i = index(a, "-"); if (i) { ca = substr(a, 1, i - 1); pa = substr(a, i + 1) }
		cb = b; pb = ""; i = index(b, "-"); if (i) { cb = substr(b, 1, i - 1); pb = substr(b, i + 1) }
		split(ca, xa, "."); split(cb, xb, ".")
		for (i = 1; i <= 3; i++) { r = cmpid(xa[i], xb[i]); if (r) return r }
		if (pa == "" && pb == "") return 0
		if (pa == "") return 1
		if (pb == "") return -1
		na = split(pa, xa, "."); nb = split(pb, xb, ".")
		for (i = 1; i <= na && i <= nb; i++) { r = cmpid(xa[i], xb[i]); if (r) return r }
		return na < nb ? -1 : na > nb
	}
	{ if (best == "" || cmp($0, best) > 0) best = $0 }
	END { print best }
	'
}

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT
trap 'exit 130' INT TERM

if [ -z "$VERSION" ]; then
	[ -z "$MIRROR" ] || die "--mirror needs --version, since a mirror can't list releases"
	fetch "https://api.github.com/repos/$REPOSITORY/releases?per_page=100" "$TMP/releases.json"
	# A server on a channel takes releases from that channel and the ones
	# above it: nightly takes everything, beta takes beta and stable.
	case $CHANNEL in
	stable) ACCEPT='^v[0-9]+\.[0-9]+\.[0-9]+$' ;;
	beta) ACCEPT='^v[0-9]+\.[0-9]+\.[0-9]+(-(beta|rc)\.[0-9]+)?$' ;;
	nightly) ACCEPT='^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$' ;;
	esac
	VERSION=$(grep -o '"tag_name": *"[^"]*"' "$TMP/releases.json" | sed 's/.*"\([^"]*\)"$/\1/' |
		grep -E "$ACCEPT" | newest | sed 's/^v//') || true
	[ -n "$VERSION" ] || die "no $CHANNEL release has been published yet"
fi
TAG=v$VERSION

case $VERSION in
*nightly*) IDENTITY="$WORKFLOW@refs/heads/main" ;;
*) IDENTITY="$WORKFLOW@refs/tags/$TAG" ;;
esac

# A mirror serves each release's files under its tag, cosign's releases
# under cosign/ and Sigstore's trusted roots under sigstore/, named as
# Sigstore names them.
if [ -n "$MIRROR" ]; then
	BASE=$MIRROR/$TAG
	COSIGN_BASE=$MIRROR/cosign/$COSIGN_VERSION
else
	BASE=https://github.com/$REPOSITORY/releases/download/$TAG
	COSIGN_BASE=https://github.com/sigstore/cosign/releases/download/$COSIGN_VERSION
fi

ASSET=racksail-linux-$ARCH
say "Downloading Racksail $VERSION for $ARCH"
fetch "$BASE/$ASSET" "$TMP/$ASSET"
fetch "$BASE/$ASSET.sigstore.json" "$TMP/$ASSET.sigstore.json"
fetch "$BASE/SHA256SUMS" "$TMP/SHA256SUMS"
fetch "$BASE/SHA256SUMS.sigstore.json" "$TMP/SHA256SUMS.sigstore.json"

# A noexec file system under the temporary directory stops anything
# downloaded from running, which would otherwise look like a bad download.
noexec() {
	die "can't run the $1 downloaded into $TMP; if its file system is mounted noexec, set TMPDIR to a directory programs can run from"
}

if command -v cosign >/dev/null 2>&1; then
	COSIGN=cosign
else
	case $ARCH in
	amd64) COSIGN_SHA256=$COSIGN_SHA256_AMD64 ;;
	arm64) COSIGN_SHA256=$COSIGN_SHA256_ARM64 ;;
	esac
	fetch "$COSIGN_BASE/cosign-linux-$ARCH" "$TMP/cosign"
	[ "$(sha256 "$TMP/cosign")" = "$COSIGN_SHA256" ] ||
		die "the cosign download doesn't match the checksum in this script, so nothing was installed"
	chmod 0755 "$TMP/cosign"
	"$TMP/cosign" version >/dev/null 2>&1 || noexec cosign
	COSIGN=$TMP/cosign
fi

# The positional parameters were all read above; they now hold what cosign
# checks the signatures against.
set -- --certificate-identity "$IDENTITY" --certificate-oidc-issuer "$OIDC_ISSUER"
if [ -n "$MIRROR" ]; then
	fetch "$MIRROR/sigstore/$SIGSTORE_ROOT_SHA256.trusted_root.json" "$TMP/trusted_root.json"
	[ "$(sha256 "$TMP/trusted_root.json")" = "$SIGSTORE_ROOT_SHA256" ] ||
		die "the mirror's Sigstore trusted root doesn't match the checksum in this script, so nothing was installed"
	set -- "$@" --trusted-root "$TMP/trusted_root.json"
fi

say "Verifying the signatures"
for file in "$ASSET" SHA256SUMS; do
	if ! "$COSIGN" verify-blob --bundle "$TMP/$file.sigstore.json" "$@" "$TMP/$file" >"$TMP/cosign.log" 2>&1; then
		sed 's/^/  /' "$TMP/cosign.log" >&2
		die "cosign couldn't verify that $file is signed by Racksail's release workflow for $TAG (its output is above), so nothing was installed"
	fi
done

say "Verifying the checksum"
EXPECTED=$(awk -v f="$ASSET" '$2 == f || $2 == "*" f { print $1 }' "$TMP/SHA256SUMS")
[ -n "$EXPECTED" ] || die "SHA256SUMS doesn't list $ASSET, so nothing was installed"
[ "$(sha256 "$TMP/$ASSET")" = "$EXPECTED" ] ||
	die "$ASSET doesn't match its checksum, so nothing was installed"
chmod 0755 "$TMP/$ASSET"

# The version is built into the signed binary, so this also catches a
# mirror serving another release under this one's name.
if ! "$TMP/$ASSET" version --json >"$TMP/version.json" 2>&1; then
	sed 's/^/  /' "$TMP/version.json" >&2
	noexec "racksail binary"
fi
REPORTED=$(sed -n 's/.*"version": *"\([^"]*\)".*/\1/p' "$TMP/version.json" | head -n 1)
[ "$REPORTED" = "$VERSION" ] || die "the binary reports version $REPORTED, not $VERSION, so nothing was installed"

{
	echo "format: 1"
	echo "mode: convert"
	echo "channel: $CHANNEL"
	[ -z "$HOSTNAME_ANSWER" ] || echo "hostname: $HOSTNAME_ANSWER"
} >"$TMP/answers.yaml"

if [ -n "$DRY_RUN" ]; then
	"$TMP/$ASSET" install --convert --binary "$BINARY" --answers "$TMP/answers.yaml" --dry-run
	exit 0
fi

if [ -z "$YES" ]; then
	say ""
	say "Racksail $VERSION will be installed as $BINARY and set up this server:"
	say "its own users, directories and services, with the panel on port 8443."
	if [ ! -t 0 ]; then
		die "there is no terminal to ask on; run it again with --yes to go ahead"
	fi
	printf 'Go ahead? [y/N] ' >&2
	read -r answer
	case $answer in
	y | Y | yes | Yes | YES) ;;
	*) die "nothing was changed" ;;
	esac
fi

mkdir -p "$ROOT${BINARY%/*}"
install -m 0755 "$TMP/$ASSET" "$ROOT$BINARY.new"
mv -f "$ROOT$BINARY.new" "$ROOT$BINARY"

"$ROOT$BINARY" install --convert --binary "$BINARY" --answers "$TMP/answers.yaml"

# Services that were already running still run the binary they started
# with; restarting them puts the new one to work.
if [ -n "$REINSTALL" ] && [ -d "$ROOT/run/systemd/system" ]; then
	say "Restarting Racksail's services"
	systemctl try-restart racksail-agent.service racksail-panel.service racksail-edge.service
fi

ADDRESS=$(hostname -I 2>/dev/null | awk '{ print $1 }') || true
say ""
say "Racksail $VERSION is installed. To finish setting it up, open"
say "  https://${ADDRESS:-this-server}:8443"
say "in a browser."
