# GoPress

A high-performance, single-binary WordPress alternative written in Go. It imports an existing
WordPress site and serves it with the same URLs and look. WordPress-like admin, SQLite embedded,
plugins as WebAssembly (Extism/wazero).

Status: early development. Plan and milestones: [PLAN.md](PLAN.md), devlog: [docs/devlog/](docs/devlog/README.md).

## Build & run

Requires Go 1.27.

```sh
make build          # static binary in bin/gopress
make test           # go vet + go test
./bin/gopress serve --addr 127.0.0.1:8080 --data ./local/data
```

## Import a WordPress site

```sh
./bin/gopress import-wp --dsn 'user:pass@tcp(host:3306)/wordpress' --uploads /path/to/wp-content/uploads --data ./data
./bin/gopress serve --addr :8080 --data ./data --trusted-proxies 172.16.0.0/12   # behind a reverse proxy
```

## Local test data

```sh
make testdata WP_HOST=root@example.org   # fresh DB dump, uploads, plugin/theme sources -> ./local (gitignored)
make devdb-up       # MariaDB 11.8 on 127.0.0.1:33066 seeded with the dump
make devdb-counts   # reference counts -> local/reference-counts.txt
make import         # import the dev database into ./local/data
make serve          # GoPress on http://127.0.0.1:8080
make wpref-up       # reference WordPress on http://127.0.0.1:8081
make parity SITE_HOST=example.org        # compare both page by page
make golden         # regenerate golden files for the render tests
```

`dev/fetch-testdata.sh` documents further settings (database container, paths). `./local` contains
personal data (comments, donor data) and must never be committed.

## License

GPL-2.0-or-later (`SPDX-License-Identifier: GPL-2.0-or-later`), see [LICENSE](LICENSE).
GoPress ports parts of WordPress (e.g. `wpautop`, `wptexturize`) and ships the Twenty Twelve
theme assets, both licensed GPLv2 or later.
