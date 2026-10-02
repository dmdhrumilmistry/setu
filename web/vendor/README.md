# Vendored browser dependencies

These files are committed so the static page never loads third-party code at
runtime (no CDN = no CDN compromise). Regenerate with `web/vendor/update.sh`
and review the diff + `SHA256SUMS` before committing.

| File | Source |
| --- | --- |
| `xterm.mjs`, `xterm.css` | `@xterm/xterm@6.0.0` |
| `addon-fit.mjs` | `@xterm/addon-fit@0.11.0` |
| `noble-secp256k1.mjs` | `@noble/curves@2.0.1` (`schnorr` export only, bundled with esbuild) |
