#!/usr/bin/env bash
# Runs the admin end-to-end test against a throwaway copy of the local
# database: starts GoPress on localhost:8090 (passkeys need a host name),
# creates the test users and runs e2e.mjs with the local Chrome.
set -euo pipefail
cd "$(dirname "$0")/../.."
SRC=${1:-local/data}
WORK=$(mktemp -d -t gopress-e2e-XXXXXX)
trap 'kill "$(cat "$WORK/pid" 2>/dev/null)" 2>/dev/null || true; rm -rf "$WORK"' EXIT

go build -o "$WORK/gopress" ./cmd/gopress
mkdir -p "$WORK/data"
sqlite_copy() { cp "$SRC/gopress.db" "$WORK/data/gopress.db"; [ -f "$SRC/gopress.db-wal" ] && cp "$SRC/gopress.db-wal" "$WORK/data/gopress.db-wal" || true; }
sqlite_copy
# A copy of the uploads: new uploads and deletions stay in the throwaway
# data directory.
cp -r "$(realpath "$SRC/uploads")" "$WORK/data/uploads"

export E2E_PASSWORD='e2e correct horse battery'
out=$(printf '%s\n' "$E2E_PASSWORD" | "$WORK/gopress" user add --data "$WORK/data" --login e2e --email e2e@example.org --display-name "E2E Tester")
export E2E_SETUP_CODE=${out##*: }
printf '%s\n' "$E2E_PASSWORD" | "$WORK/gopress" user add --data "$WORK/data" --login victim --email victim@example.org >/dev/null

mkdir -p "$WORK/mail"
export E2E_MAIL_DIR="$WORK/mail"
"$WORK/gopress" serve --addr 127.0.0.1:8090 --base-url http://localhost:8090 --data "$WORK/data" --mail-sink "$WORK/mail" >"$WORK/serve.log" 2>&1 &
echo $! >"$WORK/pid"
for _ in $(seq 50); do curl -fs -o /dev/null http://127.0.0.1:8090/healthz && break; sleep 0.1; done

cd dev/e2e
[ -d node_modules ] || npm ci --no-audit --no-fund
GP_BASE=http://localhost:8090 node e2e.mjs || { echo "--- server log"; tail -40 "$WORK/serve.log"; exit 1; }
