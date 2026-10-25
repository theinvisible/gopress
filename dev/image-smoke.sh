#!/usr/bin/env bash
# Runs the container image the way production should: read-only root
# filesystem, no capabilities, no privilege escalation, non-root, a fresh
# volume for /data. Checks that it starts, keeps its files private, can
# activate a bundled plugin (the WASM cache lives in /data) and reports
# healthy. Usage: dev/image-smoke.sh IMAGE
set -euo pipefail
IMAGE="${1:?usage: dev/image-smoke.sh IMAGE}"
# Only for looking into the volume; the same pinned image as the build.
HELPER="golang:1.27.3-alpine@sha256:b8c9b82affbbb82a020bbcf998dad2ba9516944ac7e883524642a481a9505e9c"
NAME="gopress-smoke-$$"
VOL="gopress-smoke-$$"
HARDEN=(--read-only --cap-drop ALL --security-opt no-new-privileges)

cleanup() { docker rm -f "$NAME" >/dev/null 2>&1 || true; docker volume rm "$VOL" >/dev/null 2>&1 || true; }
trap cleanup EXIT
fail() { echo "image-smoke: $*" >&2; docker logs "$NAME" 2>&1 | tail -20 >&2 || true; exit 1; }

start() {
	docker run -d --name "$NAME" "${HARDEN[@]}" -v "$VOL:/data" -p 127.0.0.1::8080 "$IMAGE" >/dev/null
	port=$(docker port "$NAME" 8080/tcp | head -1 | sed 's/.*://')
	for _ in $(seq 100); do
		curl -fs -o /dev/null "http://127.0.0.1:$port/healthz" && return 0
		sleep 0.1
	done
	fail "server did not answer on /healthz"
}

docker volume create "$VOL" >/dev/null
start
[[ "$(docker inspect -f '{{.Config.User}}' "$NAME")" == "65532:65532" ]] || fail "image does not run as 65532"
docker exec "$NAME" /gopress healthcheck >/dev/null || fail "healthcheck subcommand failed"
curl -fs "http://127.0.0.1:$port/" -o /dev/null || fail "home page failed"

perms=$(docker run --rm -v "$VOL:/data:ro" "$HELPER" stat -c '%n %a %u' /data /data/secret.key /data/gopress.db)
echo "$perms" | grep -qx '/data 750 65532' || fail "data dir: $perms"
echo "$perms" | grep -qx '/data/secret.key 600 65532' || fail "secret.key: $perms"
echo "$perms" | grep -qx '/data/gopress.db 640 65532' || fail "gopress.db: $perms"

# The CLI shares the image and the volume; plugins compile into /data/cache.
docker stop -t 10 "$NAME" >/dev/null
docker logs "$NAME" 2>&1 | grep -q 'level=ERROR' && fail "errors in the log"
docker rm "$NAME" >/dev/null
docker run --rm "${HARDEN[@]}" -v "$VOL:/data" "$IMAGE" plugin activate foto-slider-lightbox >/dev/null || fail "plugin activate"
start
docker run --rm -v "$VOL:/data:ro" "$HELPER" sh -c 'ls /data/cache/wasm | grep -q .' || fail "no WASM cache in /data"
docker run --rm "${HARDEN[@]}" -v "$VOL:/data" "$IMAGE" plugin list | grep -q 'foto-slider-lightbox.*active' || fail "plugin not active"
docker logs "$NAME" 2>&1 | grep -q 'level=ERROR' && fail "errors in the log"
echo "image-smoke: ok ($IMAGE)"
