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

## Admin

The admin is at `/admin/`, the login at `/login`. Every account uses two-factor authentication:
at the first login GoPress asks for a passkey or an authenticator app (TOTP) and shows ten
recovery codes. Afterwards you sign in with password + second factor, or with a passkey alone.
Passkeys need the site to be reached by a host name (e.g. `http://localhost:8080`, or HTTPS in
production), not an IP address.

Imported WordPress users keep their password (it is rehashed with argon2id at the first login).
Break-glass commands for whoever has access to the server:

```sh
./bin/gopress user list --data ./data
./bin/gopress user add --data ./data --login alice --email alice@example.org --role editor   # asks for the password
./bin/gopress user set-password --data ./data alice      # also ends all sessions
./bin/gopress user reset-2fa --data ./data alice         # set up two-factor authentication again
./bin/gopress user revoke-sessions --data ./data alice
```

## Local test data

```sh
make testdata WP_HOST=root@example.org   # fresh DB dump, uploads, plugin/theme sources -> ./local (gitignored)
make devdb-up       # MariaDB 11.8 on 127.0.0.1:33066 seeded with the dump
make devdb-counts   # reference counts -> local/reference-counts.txt
make import         # import the dev database into ./local/data
make serve          # GoPress on http://127.0.0.1:8080
make wpref-up       # reference WordPress on http://127.0.0.1:8081
make parity SITE_HOST=example.org        # compare both page by page (extra paths: local/parity-urls.txt)
make golden         # regenerate golden files for the render tests
make roundtrip      # editor round trip over all posts: no content loss, same rendering
make e2e            # admin end-to-end tests with the local Chrome (incl. passkeys)
```

The post editor (`web/editor`, TipTap) is bundled into `web/admin/static/editor` and committed, so
building GoPress needs no Node; `make editor` rebuilds it (Node 22). `make admin-assets` copies the
WordPress admin stylesheets from a WordPress checkout (default `local/wp-core`).

`dev/fetch-testdata.sh` documents further settings (database container, paths). `./local` contains
personal data (comments, donor data) and must never be committed.

## License

GPL-2.0-or-later (`SPDX-License-Identifier: GPL-2.0-or-later`), see [LICENSE](LICENSE).
GoPress ports parts of WordPress (e.g. `wpautop`, `wptexturize`) and ships the Twenty Twelve
theme assets and the WordPress admin stylesheets and Dashicons, all licensed GPLv2 or later. The
editor bundles MIT-licensed libraries (TipTap, ProseMirror, …), see
`web/admin/static/editor/THIRD-PARTY-LICENSES.txt`. The common-password list in `internal/auth`
is derived from SecLists (MIT).
