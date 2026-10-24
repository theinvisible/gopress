#!/usr/bin/env bash
# (Re)starts a local GoPress on 127.0.0.1:8080 with ./local/data in the
# background; the pid is kept in local/serve.pid, the log in local/serve.log.
set -euo pipefail
cd "$(dirname "$0")/.."
if [[ -f local/serve.pid ]] && kill "$(cat local/serve.pid)" 2>/dev/null; then
	sleep 0.5
fi
[[ "${1:-}" == "stop" ]] && exit 0
go build -o bin/gopress ./cmd/gopress
nohup ./bin/gopress serve --addr 127.0.0.1:8080 --base-url http://127.0.0.1:8080 --data ./local/data >local/serve.log 2>&1 &
echo $! >local/serve.pid
for _ in $(seq 50); do curl -fs -o /dev/null http://127.0.0.1:8080/healthz && exit 0; sleep 0.1; done
echo "server did not start" >&2; cat local/serve.log >&2; exit 1
