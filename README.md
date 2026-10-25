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
at the first login GoPress asks for a one-time setup code (from an administrator or
`gopress user setup-code`), then for a passkey or an authenticator app (TOTP), and shows ten
recovery codes. Afterwards you sign in with password + second factor, or with a passkey alone.
Passkeys need the site to be reached by a host name (e.g. `http://localhost:8080`, or HTTPS in
production), not an IP address.

Imported WordPress users keep their password (it is rehashed with argon2id at the first login);
create their setup code on the server. Break-glass commands for whoever has access to the server:

```sh
./bin/gopress user list --data ./data
./bin/gopress user add --data ./data --login alice --email alice@example.org --role editor   # asks for the password, prints the setup code
./bin/gopress user setup-code --data ./data alice       # new setup code (72 h, single use)
./bin/gopress user set-password --data ./data alice      # also ends all sessions, revokes access tokens, lifts login locks
./bin/gopress user reset-2fa --data ./data alice         # set up two-factor authentication again (prints a setup code)
./bin/gopress user unlock --data ./data alice
./bin/gopress user revoke-sessions --data ./data alice
```

Administrators manage users in the admin as well (Users › Add User): an invitation link (mailed
and/or shown once) lets the new user choose a password and set up two-factor authentication.

### Email

Password resets, invitations, security notices and comment notifications are sent through an SMTP
server configured under Settings › Email (STARTTLS or TLS is required, except for a relay on
localhost; the password is stored encrypted with a key derived from `data/secret.key`). Until it
is configured, messages wait in the queue. `gopress mail queue` lists it, `gopress mail test --to
you@example.org` sends a test message. For development, `gopress serve --mail-sink DIR` writes
every message as an `.eml` file instead of sending it.

### Media

Uploads (images, PDF, archives, audio/video; never HTML, SVG or scripts) are checked against their
content. Location data and other metadata (EXIF/GPS, XMP, IPTC) are removed from photos without
re-encoding them, the EXIF orientation is applied, and the image sizes from Settings › Media are
created in pure Go. Images can also be pasted (Ctrl+V) or dropped into the editor.

```sh
./bin/gopress media regenerate --data ./data [--missing]   # after changing the image sizes
./bin/gopress media strip-metadata --data ./data [--apply]  # older uploads: list (or clean) files with metadata
```

### AI agents (MCP)

GoPress has an [MCP](https://modelcontextprotocol.io) server at `/mcp` (Streamable HTTP), so agents
such as Claude Code can read the site and write posts, e.g. release announcements. It is off by
default:

1. An administrator turns it on under Settings › Writing ("AI agents (MCP)"). The site must be
   served over HTTPS (or `http://localhost` for development).
2. Each author creates a personal access token under Profile › Access Tokens (after confirming
   their identity). The token is shown once, together with the client configuration.
3. Connect the agent, e.g. Claude Code:

   ```sh
   claude mcp add --transport http --scope user gopress https://example.org/mcp \
     --header "Authorization: Bearer gpat_…"
   ```

   or in a project's `.mcp.json`, with the token in an environment variable (never commit it):

   ```json
   { "mcpServers": { "gopress": { "type": "http", "url": "https://example.org/mcp",
       "headers": { "Authorization": "Bearer ${GOPRESS_TOKEN}" } } } }
   ```

Tools: `get_site_info`, `list_posts`, `get_post`, `create_post` and `update_post` (Markdown or HTML),
`list_terms`, `create_category`, `upload_media` (images and PDF as base64) and `list_media`.

A token acts as its user, limited to the scopes chosen when it was created:

| Scope | Allows |
|---|---|
| (always) | read published posts and pages and the user's own drafts |
| `content:write` | create posts and pages as drafts or for review, edit the user's own unpublished ones |
| `content:publish` | publish and schedule, edit the user's own published posts (off by default) |
| `media:upload` | upload images and PDF files, list the media library |
| `terms:manage` | create categories |

No scope allows editing other users' content, deleting anything, unfiltered HTML (agent content is
always filtered, even for administrators), comments, users or settings. Tokens expire after at
most a year, can be revoked at any time (administrators can revoke other users' tokens under
Users › Edit) and are revoked automatically by a password change or reset, a 2FA reset, a role change or "log out
everywhere". Every change made with a token is recorded in the activity log with the token's
name. Failed token checks are rate limited per network, calls per token.

### Plugins

Plugins are WebAssembly packages (`.zip`) that run in a sandbox. Administrators upload them under
Plugins › Upload Plugin, review what each one asks for (HTTPS hosts, routes, hooks, data, scripts on
public pages) and approve exactly that package when activating it. An update that asks for more
stays inactive until it is approved again. Plugins add shortcodes, widgets, settings pages, lists,
custom admin pages (in a sandboxed iframe), dashboard boxes and editor buttons. Their HTML is
filtered, and a plugin that crashes or hangs is paused instead of affecting the site.

Bundled plugins (activate them under Plugins, or with
`gopress plugin activate --import NAME…`; the import brings over their WordPress data):

- **Photo Slider & Lightbox:** galleries as a slider with a lightbox, `[fotogalerie id="…"]`.
- **PayPal Donations:** a donation button as a shortcode and a widget, optionally with a list of donations that PayPal confirmed.
- **Akismet Anti-spam:** checks new comments with Akismet (needs an API key).

Write plugins in Go with `pdk/go` (or any language with an Extism PDK) and build them with
`gopress plugin-pack DIR`; `plugins/hello` is the example (`make hello`). Plugins can render HTML
templates that themes can override, and test with `go test` against a fake host. See
[docs/plugins.md](docs/plugins.md).

### Live updates

Open admin pages get updates over Server-Sent Events. They see new comments awaiting moderation (a
notice and the count), a plugin being paused, the progress of an import, and notices from plugins.
The editor shows who else edits the same post and warns when someone saved a newer version. It also
has a live preview next to the editor.

## Deployment

```sh
make release        # tests, govulncheck, container image, image checks
docker run -d --name gopress --read-only --cap-drop ALL --security-opt no-new-privileges \
  -p 127.0.0.1:8080:8080 -v gopress:/data -v /srv/gopress/backups:/backups \
  -e GOPRESS_BASE_URL=https://example.org ghcr.io/theinvisible/gopress
docker exec gopress /gopress backup --out /backups --keep 14
```

The image is `FROM scratch`: it holds only the binary, the CA certificates and a non-root user, and
runs with a read-only root filesystem. Everything that changes lives in `/data`. `gopress backup`
works while the server runs; `gopress restore` restores a backup into an empty directory.

Behind a reverse proxy, pass `--trusted-proxies` (the proxy's subnet). Proxy headers from anyone
else are dropped. Staging copies run with `--noindex` and `--disable-outbound`.

- [docs/deploy.md](docs/deploy.md): configuration, the proxy, hardening, backups, updates.
- [docs/deploy/compose.example.yaml](docs/deploy/compose.example.yaml): a hardened service behind
  Traefik.
- [docs/deploy/runbook.md](docs/deploy/runbook.md): moving a WordPress site: staging, comparison with
  the live site, cutover, rollback.

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
