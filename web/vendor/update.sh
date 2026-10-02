#!/usr/bin/env bash
# Rebuild vendored browser dependencies. Requires node + npm.
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
cd "$tmp"
npm init -y >/dev/null
npm i --no-audit --no-fund @noble/curves@2.0.1 @xterm/xterm@6.0.0 @xterm/addon-fit@0.11.0 esbuild@0.25 >/dev/null
echo "export { schnorr } from '@noble/curves/secp256k1.js';" > entry.js
npx esbuild entry.js --bundle --format=esm --minify --outfile="$here/noble-secp256k1.mjs"
cp node_modules/@xterm/xterm/lib/xterm.mjs node_modules/@xterm/xterm/css/xterm.css "$here/"
cp node_modules/@xterm/addon-fit/lib/addon-fit.mjs "$here/"
sed -i.bak '/sourceMappingURL/d' "$here/xterm.mjs" "$here/addon-fit.mjs" && rm -f "$here"/*.bak
cd "$here" && sha256sum xterm.mjs xterm.css addon-fit.mjs noble-secp256k1.mjs > SHA256SUMS
echo "vendored files updated; review: git diff -- web/vendor"
