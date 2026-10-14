#!/bin/sh
# Install the setu managed policy on Linux. Run as root from your MDM's
# script runner or configuration management:
#
#   sh install-policy.sh < policy.json     # policy on stdin
#   sh install-policy.sh policy.json       # or a file
#
# The policy must be owned by root and not writable by others, or setu
# refuses to run (it fails closed rather than ignoring a policy).
set -eu
if [ "$(id -u)" -ne 0 ]; then
  echo "install-policy.sh: run as root" >&2
  exit 1
fi
umask 022
install -d -o root -g root -m 0755 /etc/setu
tmp=$(mktemp /etc/setu/.policy.XXXXXX)
trap 'rm -f "$tmp"' EXIT
if [ $# -gt 0 ]; then cat "$1" >"$tmp"; else cat >"$tmp"; fi
chown root:root "$tmp"
chmod 0644 "$tmp"
mv -f "$tmp" /etc/setu/policy.json
trap - EXIT
echo "installed /etc/setu/policy.json"
command -v setu >/dev/null 2>&1 && setu policy || true
