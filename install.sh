#!/bin/sh
# setu installer for Linux and macOS.
#
#   curl -fsSL https://dmdhrumilmistry.github.io/setu/install.sh | sh
#
# Downloads the latest release for this machine, checks it against the
# release's SHA256SUMS and the ed25519 signature of that file (when OpenSSL 3
# is available), makes sure it runs, and installs it to ~/.local/bin (or
# /usr/local/bin when run as root). No sudo needed, and because the binary
# stays writable by you, setu can update itself later.
#
# Settings (environment variables):
#   SETU_VERSION       install this release, e.g. v0.1.0 (default: latest)
#   SETU_INSTALL_DIR   install here instead
#   SETU_BASE_URL      a release mirror with GitHub's layout
#   SETU_REQUIRE_SIGNATURE=1   fail instead of warning when the signature cannot be checked
set -eu

REPO_URL="https://github.com/dmdhrumilmistry/setu"
BASE_URL="${SETU_BASE_URL:-$REPO_URL/releases}"
# The release signing key (internal/update.ReleaseKeys), as an Ed25519 SPKI.
PUBKEY_B64="4n/cWcdrCUpMYyqHsyEICBUzL+PkN2qWplBXIQPMnMo="

say() { printf '%s\n' "$*" >&2; }
die() { say "setu install: $*"; exit 1; }

case "$BASE_URL" in https://*) ;; *) die "SETU_BASE_URL must be an https:// URL" ;; esac

# Where this machine's binary comes from.
case "$(uname -s)" in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  MINGW* | MSYS* | CYGWIN*) die "on Windows, run in PowerShell: irm https://raw.githubusercontent.com/dmdhrumilmistry/setu/main/install.ps1 | iex" ;;
  *) die "no release for $(uname -s); build from source: $REPO_URL#install" ;;
esac
case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  arm64 | aarch64) arch=arm64 ;;
  *) die "no release for $(uname -m); build from source: $REPO_URL#install" ;;
esac
# Rosetta: prefer the native arm64 binary on Apple silicon.
if [ "$os" = darwin ] && [ "$arch" = amd64 ] && [ "$(sysctl -n sysctl.proc_translated 2>/dev/null || echo 0)" = 1 ]; then
  arch=arm64
fi

if command -v curl >/dev/null 2>&1; then
  fetch() { curl -fsSL --proto '=https' --tlsv1.2 --retry 3 -o "$2" "$1"; }
  latest() { curl -fsSL --proto '=https' --tlsv1.2 -o /dev/null -w '%{url_effective}' "$BASE_URL/latest"; }
elif command -v wget >/dev/null 2>&1; then
  fetch() { wget -q --https-only -O "$2" "$1"; }
  latest() { wget -q --https-only -O /dev/null --server-response "$BASE_URL/latest" 2>&1 | sed -n 's/^ *[Ll]ocation: *//p' | tail -n 1 | tr -d '\r'; }
else
  die "needs curl or wget"
fi

version="${SETU_VERSION:-}"
if [ -z "$version" ]; then
  version="$(latest)"
  version="${version##*/}"
fi
printf '%s' "$version" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$' || die "could not find the latest release (got '$version')"

asset="setu-$version-$os-$arch"
tmp="$(mktemp -d 2>/dev/null || mktemp -d -t setu)"
trap 'rm -rf "$tmp"' EXIT INT TERM

say "Downloading setu ${version} for ${os}/${arch}..."
fetch "$BASE_URL/download/$version/$asset" "$tmp/setu" || die "download failed: $BASE_URL/download/$version/$asset"
fetch "$BASE_URL/download/$version/SHA256SUMS" "$tmp/SHA256SUMS" || die "could not download SHA256SUMS"

# 1. The binary must match the release's checksum.
want="$(awk -v f="$asset" '$2 == f || $2 == "*" f { print $1 }' "$tmp/SHA256SUMS")"
[ -n "$want" ] || die "SHA256SUMS has no entry for $asset"
if command -v sha256sum >/dev/null 2>&1; then
  got="$(sha256sum "$tmp/setu" | awk '{print $1}')"
else
  got="$(shasum -a 256 "$tmp/setu" | awk '{print $1}')"
fi
[ "$got" = "$want" ] || die "checksum mismatch for $asset: the download is corrupt or was tampered with"

# 2. SHA256SUMS must be signed by the setu release key.
signed=no
if fetch "$BASE_URL/download/$version/SHA256SUMS.sig" "$tmp/SHA256SUMS.sig" 2>/dev/null &&
  command -v openssl >/dev/null 2>&1 && openssl version 2>/dev/null | grep -q '^OpenSSL [3-9]'; then
  { printf '\060\052\060\005\006\003\053\145\160\003\041\000'; printf '%s' "$PUBKEY_B64" | openssl base64 -d -A; } >"$tmp/key.der"
  openssl base64 -d -A <"$tmp/SHA256SUMS.sig" >"$tmp/sig.bin"
  if openssl pkeyutl -verify -pubin -inkey "$tmp/key.der" -keyform DER -rawin -in "$tmp/SHA256SUMS" -sigfile "$tmp/sig.bin" >/dev/null 2>&1; then
    signed=yes
  else
    die "the release signature does not verify: refusing to install"
  fi
fi
if [ "$signed" = no ]; then
  [ "${SETU_REQUIRE_SIGNATURE:-0}" = 1 ] && die "cannot check the release signature (needs OpenSSL 3)"
  say "note: signature not checked here (needs OpenSSL 3); the checksum was verified over https, and setu checks signatures on every update"
fi

# 3. It must run on this machine.
chmod 755 "$tmp/setu"
"$tmp/setu" version >/dev/null 2>&1 || die "the downloaded setu does not run on this machine"

# Install: into a temporary name in the target directory, then rename, so a
# running setu is never left half replaced.
if [ -n "${SETU_INSTALL_DIR:-}" ]; then
  dir="$SETU_INSTALL_DIR"
elif [ "$(id -u)" = 0 ]; then
  dir=/usr/local/bin
else
  dir="$HOME/.local/bin"
fi
mkdir -p "$dir" || die "cannot create $dir"
cp "$tmp/setu" "$dir/.setu.new" || die "cannot write to $dir (set SETU_INSTALL_DIR)"
mv -f "$dir/.setu.new" "$dir/setu"
if [ "$os" = darwin ]; then xattr -d com.apple.quarantine "$dir/setu" 2>/dev/null || true; fi

say ""
say "Installed $("$dir/setu" version) to $dir/setu"
case ":$PATH:" in
  *":$dir:"*) ;;
  *)
    case "${SHELL:-}" in
      */zsh) rc="$HOME/.zshrc" ;;
      */bash) rc="$HOME/.bashrc" ;;
      */fish) rc="" ;;
      *) rc="$HOME/.profile" ;;
    esac
    say ""
    if [ -n "$rc" ]; then
      say "$dir is not on your PATH yet. Add it:"
      say "  echo 'export PATH=\"$dir:\$PATH\"' >> $rc && export PATH=\"$dir:\$PATH\""
    else
      say "$dir is not on your PATH yet. Add it: fish_add_path $dir"
    fi
    ;;
esac
say ""
say "Next:"
say "  setu share -- claude              share Claude Code (or any command); scan the QR code with your phone"
say "  setu service add claude -- claude always-on access from your phone"
say "  setu doctor                       check your setup"
