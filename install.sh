#!/bin/sh
# Installs a fileferry release binary.
#
#   curl -fsSL https://raw.githubusercontent.com/garethgeorge/fileferry/main/install.sh | sh
#   curl -fsSL https://raw.githubusercontent.com/garethgeorge/fileferry/main/install.sh | sh -s -- ferryserver
#
# Env overrides: BINARY (ferryupload|ferryserver), VERSION (e.g. v1.2.0,
# default: latest), INSTALL_DIR (default: /usr/local/bin, falling back to
# ~/.local/bin if not writable).
set -eu

repo="garethgeorge/fileferry"
binary="${1:-${BINARY:-ferryupload}}"
version="${VERSION:-latest}"

case "$binary" in
  ferryupload | ferryserver) ;;
  *)
    echo "install.sh: unknown binary '$binary' (expected ferryupload or ferryserver)" >&2
    exit 1
    ;;
esac

os=$(uname -s)
case "$os" in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  *)
    echo "install.sh: unsupported OS '$os' — download a release manually from" >&2
    echo "  https://github.com/$repo/releases" >&2
    exit 1
    ;;
esac

arch=$(uname -m)
case "$arch" in
  x86_64 | amd64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *)
    echo "install.sh: unsupported architecture '$arch' — download a release manually from" >&2
    echo "  https://github.com/$repo/releases" >&2
    exit 1
    ;;
esac

if [ "$version" = "latest" ]; then
  tag=$(curl -fsSL -o /dev/null -w '%{url_effective}' "https://github.com/$repo/releases/latest")
  tag=${tag##*/}
else
  case "$version" in
    v*) tag="$version" ;;
    *) tag="v$version" ;;
  esac
fi
if [ -z "$tag" ]; then
  echo "install.sh: could not resolve the latest release tag" >&2
  exit 1
fi
ver=${tag#v}

archive="${binary}_${ver}_${os}_${arch}.tar.gz"
base_url="https://github.com/$repo/releases/download/$tag"

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

echo "install.sh: downloading $binary $tag ($os/$arch)..." >&2
curl -fsSL "$base_url/$archive" -o "$tmpdir/$archive"

if curl -fsSL "$base_url/checksums.txt" -o "$tmpdir/checksums.txt" 2>/dev/null; then
  line=$(grep " $archive\$" "$tmpdir/checksums.txt" || true)
  if [ -n "$line" ]; then
    if command -v sha256sum >/dev/null 2>&1; then
      (cd "$tmpdir" && echo "$line" | sha256sum -c - >/dev/null) || {
        echo "install.sh: checksum verification failed for $archive" >&2
        exit 1
      }
    elif command -v shasum >/dev/null 2>&1; then
      (cd "$tmpdir" && echo "$line" | shasum -a 256 -c - >/dev/null) || {
        echo "install.sh: checksum verification failed for $archive" >&2
        exit 1
      }
    else
      echo "install.sh: warning: no sha256sum/shasum found, skipping checksum verification" >&2
    fi
  fi
else
  echo "install.sh: warning: could not fetch checksums.txt, skipping checksum verification" >&2
fi

tar -xzf "$tmpdir/$archive" -C "$tmpdir" "$binary"

install_dir="${INSTALL_DIR:-/usr/local/bin}"
if [ ! -d "$install_dir" ] || [ ! -w "$install_dir" ]; then
  install_dir="$HOME/.local/bin"
  mkdir -p "$install_dir"
fi

mv "$tmpdir/$binary" "$install_dir/$binary"
chmod +x "$install_dir/$binary"

echo "install.sh: installed $binary $ver to $install_dir/$binary" >&2
case ":$PATH:" in
  *":$install_dir:"*) ;;
  *) echo "install.sh: warning: $install_dir is not on your PATH" >&2 ;;
esac

"$install_dir/$binary" --version 2>/dev/null || true
