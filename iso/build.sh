#!/usr/bin/env bash
# Builds Racksail's installer ISO with Debian's live-build. It runs as root
# on Debian 13; make iso and the ISO workflow run it in a Debian container.
#
#   iso/build.sh --arch amd64 --binary bin/racksail-linux-amd64 --version 1.2.3
#   iso/build.sh --arch arm64 --variant firmware --binary bin/racksail-linux-arm64 --version 1.2.3
#
# The image goes to dist/racksail-<version>-<arch>.iso, or
# racksail-<version>-<arch>-firmware.iso for the variant with Debian's
# non-free firmware. It must stay under 2 GiB.
set -euo pipefail

usage() {
	cat <<'EOF'
Usage: iso/build.sh --arch ARCH --binary FILE --version VERSION [options]

  --arch ARCH          amd64 or arm64
  --variant VARIANT    standard (the default), or firmware to add Debian's
                       non-free firmware
  --binary FILE        the racksail binary for ARCH, built with the UI
  --version VERSION    the version the image is named and labelled with
  --out DIR            where the image goes; dist by default
  --work DIR           where live-build works; build/iso by default
  --mirror URL         Debian mirror to build from
  --security-mirror URL
                       Debian security mirror to build from
EOF
}

die() {
	echo "iso/build.sh: $*" >&2
	exit 1
}

arch= variant=standard binary= version= out=dist work=build/iso mirror= security_mirror=
while [ $# -gt 0 ]; do
	case $1 in
	--arch) arch=$2; shift 2 ;;
	--variant) variant=$2; shift 2 ;;
	--binary) binary=$2; shift 2 ;;
	--version) version=$2; shift 2 ;;
	--out) out=$2; shift 2 ;;
	--work) work=$2; shift 2 ;;
	--mirror) mirror=$2; shift 2 ;;
	--security-mirror) security_mirror=$2; shift 2 ;;
	-h | --help) usage; exit 0 ;;
	*) usage >&2; die "unknown option $1" ;;
	esac
done

case $arch in
amd64) elf_machine=62 ;;
arm64) elf_machine=183 ;;
*) die "--arch is amd64 or arm64" ;;
esac
case $variant in
standard | firmware) ;;
*) die "--variant is standard or firmware" ;;
esac
[[ $version =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] || die "--version must be a version such as 1.2.3"
[ -f "$binary" ] || die "--binary must name the racksail binary for $arch"
# The binary's ELF header names its machine at offset 18.
[ "$(od -An -tu2 -j18 -N2 "$binary" | tr -d ' ')" = "$elf_machine" ] || die "$binary isn't a $arch binary"

[ "$(id -u)" = 0 ] || die "live-build needs root; run this as root, or make iso to build in a container"
for tool in lb rsvg-convert grub-mkfont; do
	command -v "$tool" >/dev/null || die "$tool is missing; install live-build, librsvg2-bin and grub-common"
done
fonts=/usr/share/fonts/opentype/inter
[ -f "$fonts/Inter-Regular.otf" ] || die "the Inter font is missing; install fonts-inter"

repo=$(cd "$(dirname "$0")/.." && pwd)
binary=$(realpath "$binary")
out=$(realpath -m "$out")
dir=$(realpath -m "$work")/$arch-$variant
branding=$repo/iso/branding

# svg renders an SVG given as text into a PNG.
svg() {
	printf '<svg xmlns="http://www.w3.org/2000/svg" width="%s" height="%s">%s</svg>' "$1" "$2" "$3" | rsvg-convert --output "$4"
}

render_grub_theme() {
	local dest=$1 title subtitle
	mkdir -p "$dest"
	title="Racksail $version"
	subtitle="Installer for $arch"
	[ "$variant" = standard ] || subtitle="$subtitle, with non-free firmware"
	sed -e "s|@TITLE@|$title|" -e "s|@SUBTITLE@|$subtitle|" "$branding/grub/theme.txt" >"$dest/theme.txt"
	rsvg-convert --width 1920 --height 1080 --output "$dest/background.png" "$branding/grub/background.svg"

	# The chosen entry's box, in the nine pieces GRUB stretches to fit an
	# entry: rounded corners, edges and the middle, in brand blue.
	local blue='fill="#007CFD"'
	svg 6 6 "<path d=\"M6 6L0 6A6 6 0 0 1 6 0Z\" $blue/>" "$dest/select_nw.png"
	svg 6 6 "<path d=\"M0 6L0 0A6 6 0 0 1 6 6Z\" $blue/>" "$dest/select_ne.png"
	svg 6 6 "<path d=\"M0 0L6 0A6 6 0 0 1 0 6Z\" $blue/>" "$dest/select_se.png"
	svg 6 6 "<path d=\"M6 0L6 6A6 6 0 0 1 0 0Z\" $blue/>" "$dest/select_sw.png"
	svg 1 6 "<rect width=\"1\" height=\"6\" $blue/>" "$dest/select_n.png"
	svg 1 6 "<rect width=\"1\" height=\"6\" $blue/>" "$dest/select_s.png"
	svg 6 1 "<rect width=\"6\" height=\"1\" $blue/>" "$dest/select_e.png"
	svg 6 1 "<rect width=\"6\" height=\"1\" $blue/>" "$dest/select_w.png"
	svg 1 1 "<rect width=\"1\" height=\"1\" $blue/>" "$dest/select_c.png"

	# The fonts theme.txt names, from the Inter the panel uses.
	grub-mkfont --size=14 --output="$dest/inter-regular-14.pf2" "$fonts/Inter-Regular.otf"
	grub-mkfont --size=16 --output="$dest/inter-regular-16.pf2" "$fonts/Inter-Regular.otf"
	grub-mkfont --size=18 --output="$dest/inter-regular-18.pf2" "$fonts/Inter-Regular.otf"
	grub-mkfont --size=18 --output="$dest/inter-bold-18.pf2" "$fonts/Inter-Bold.otf"
	grub-mkfont --size=24 --output="$dest/inter-bold-24.pf2" "$fonts/Inter-Bold.otf"
}

render_plymouth_theme() {
	local dest=$1
	mkdir -p "$dest"
	install -m 0644 "$branding/plymouth/racksail.plymouth" "$branding/plymouth/racksail.script" "$dest/"
	rsvg-convert --width 200 --output "$dest/logo.png" "$branding/plymouth/logo.svg"
	svg 320 6 '<rect width="320" height="6" rx="3" fill="#22344C"/>' "$dest/progress-track.png"
	svg 320 6 '<rect width="320" height="6" rx="3" fill="#3D9BFF"/>' "$dest/progress-bar.png"
}

echo "Building the $variant installer image for $arch in $dir"
rm -rf "$dir"
mkdir -p "$dir"
cp -a "$repo/iso/auto" "$repo/iso/config" "$dir/"
files=$dir/config/includes.chroot_after_packages

install -D -m 0755 "$binary" "$files/usr/local/bin/racksail"
# racksail install reads this to know it runs from the installer image.
install -d "$files/etc/racksail"
cat >"$files/etc/racksail/installer-release" <<EOF
NAME="Racksail installer"
VERSION="$version"
VARIANT_ID=$variant
ARCHITECTURE=$arch
EOF
install -m 0644 "$branding/console/console.vtrgb" "$files/etc/racksail/console.vtrgb"
install -m 0644 "$branding/console/issue" "$files/etc/issue"
render_grub_theme "$dir/config/bootloaders/grub-pc/racksail"
render_plymouth_theme "$files/usr/share/plymouth/themes/racksail"

# Everything in the image carries the commit's time, so building a commit
# twice gives the same image.
if [ -z "${SOURCE_DATE_EPOCH:-}" ]; then
	SOURCE_DATE_EPOCH=$(git -c safe.directory="$repo" -C "$repo" log -1 --format=%ct 2>/dev/null || date +%s)
fi
export SOURCE_DATE_EPOCH

export RACKSAIL_ARCH=$arch RACKSAIL_VARIANT=$variant
[ -z "$mirror" ] || export RACKSAIL_MIRROR=$mirror
[ -z "$security_mirror" ] || export RACKSAIL_SECURITY_MIRROR=$security_mirror

log=$dir.log
cd "$dir"
lb config
lb build 2>&1 | tee "$log"

image=$dir/racksail-$arch.hybrid.iso
[ -f "$image" ] || die "live-build made no image; see $log"
size=$(stat -c %s "$image")
limit=$((2 * 1024 * 1024 * 1024))
[ "$size" -lt "$limit" ] || die "the image is $((size / 1024 / 1024)) MiB, over the 2 GiB limit"

name=racksail-$version-$arch
[ "$variant" = standard ] || name=$name-firmware
mkdir -p "$out"
mv "$image" "$out/$name.iso"
# The system the image was made from goes; the package cache stays for the
# next build.
lb clean
# A container builds as root; give what it made to whoever started it.
if [ -n "${HOST_UID:-}" ]; then
	chown -R "$HOST_UID:${HOST_GID:-$HOST_UID}" "$out" "$dir" "$log"
fi
echo "$out/$name.iso: $((size / 1024 / 1024)) MiB"
