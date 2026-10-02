#!/usr/bin/env bash
# CLI smoke test on a real OS (used by CI on Linux, macOS and Windows):
#   setu share -d  -> background share of a one-line echo command
#   setu ps / link -> registry works
#   setu join      -> types a line through the real PTY / ConPTY, expects echo
#   session ends   -> registry cleaned up
set -euo pipefail
BIN=${1:-./setu}
export SETU_PASSWORD=smoke-test-pass
case "$(uname -s)" in
  MINGW*|MSYS*|CYGWIN*)
    CMD=(powershell.exe -NoLogo -NoProfile -Command "Write-Output READY; \$l = [Console]::ReadLine(); Write-Output ('got:' + \$l)") ;;
  *)
    CMD=(sh -c 'echo READY; read l; echo "got:$l"') ;;
esac

"$BIN" version
"$BIN" share -d --web-url "" -- "${CMD[@]}"
"$BIN" ps
ID=$("$BIN" ps | awk 'NR==2 {print $1}')
test -n "$ID"
LINK=$("$BIN" link "$ID" | grep -m1 'setu join' | sed "s/.*setu join '\(.*\)'/\1/")

# Join in the background with a watchdog so a regression fails fast with
# logs instead of hanging CI (macOS has no `timeout`).
OUTF=$(mktemp)
( (sleep 10; echo smoke) | "$BIN" join --timeout 90s "$LINK" >"$OUTF" 2>&1 ) &
JOIN=$!
for _ in $(seq 1 120); do
  kill -0 "$JOIN" 2>/dev/null || break
  sleep 1
done
if kill -0 "$JOIN" 2>/dev/null; then
  kill "$JOIN" 2>/dev/null || true
  cat "$OUTF"
  echo "setu join did not finish within 120s; host log:" >&2
  "$BIN" logs "$ID" >&2 || true
  "$BIN" stop "$ID" || true
  exit 1
fi
cat "$OUTF"
grep -q "got:smoke" "$OUTF"

# The command exited, so the background share must have ended and cleaned up.
for _ in $(seq 1 20); do
  "$BIN" ps | grep -q "$ID" || { echo "smoke test passed"; exit 0; }
  sleep 1
done
echo "background session $ID still listed after its command exited" >&2
"$BIN" logs "$ID" >&2 || true
"$BIN" stop "$ID" || true
exit 1
