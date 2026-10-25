# 2026-10-25 – M5 deployment

## Done
- **Image** (`Dockerfile`):
  - Static build on `golang:1.27.3-alpine`, pinned by digest.
  - Final image `FROM scratch`: the binary, the CA bundle and `passwd`/`group` for uid 65532; `/data` 0750.
  - `.dockerignore` is an allowlist: `local/` never enters the build context.
  - `gopress healthcheck` for `HEALTHCHECK` (scratch has no curl).
  - **Make targets:**
    - `make image-check` pins the exact file list.
    - `make image-smoke` runs the image read-only, without capabilities and with `no-new-privileges`, then checks permissions and a plugin activation into the volume.
    - `make release` runs everything; `make image-push` only pushes a clean tree.
- **`gopress backup` / `gopress restore`:**
  - The snapshot is `VACUUM INTO`, so it works while serving, and gets an integrity check.
  - One tar.gz (0600) with the manifest, secret, database, uploads, uploaded plugins and theme overrides.
  - Retention only touches its own archive names.
  - Restore goes only into an empty directory and checks paths, types, limits, format and schema.
  - `db.Open` refuses a database whose schema is newer than the binary.
- **Serve:**
  - Proxy headers only from `--trusted-proxies`. Before, any client could claim `X-Forwarded-Proto: https`.
  - `--noindex` and `--disable-outbound` for staging; all flags also as environment variables.
  - Startup warnings: root, a world-readable data directory, no https, https without trusted proxies.
  - umask 027.
- **Docs:**
  - `docs/deploy.md`: configuration, Traefik, hardening, resources, backups, updates.
  - `docs/deploy/compose.example.yaml`.
  - `docs/deploy/runbook.md`: staging, comparison, acceptance, cutover with a content freeze, rollback.
- **Comparison with the live site:**
  - `dev/parity` can crawl a live site politely (`-concurrency`, `-delay`, its own User-Agent) and compares content types.
  - `dev/visual` takes screenshots at 1280 and 375 px and computes the pixel diff in Chrome (no new dependencies).
  - A fresh import, served like staging, matches the live site in 162/162 URLs. The visual comparison of home, a post with comments, the gallery page, the donate page, a category, the search and the 404 differs by at most 0.12 % of the pixels (text anti-aliasing).
- **Differences from the live site found and fixed on the way:**
  - **Feed URLs:** `/feed/rss2/` (and the comment, category and `/rss2/` variants) redirect to the default feed URL like WordPress' canonical redirect. The home page's Atom/RDF redirect went to `//feed/`.
  - **Excerpts of block content:** WordPress builds them without lists of list items, code, images and similar blocks (`excerpt_remove_blocks`). The importer now stores that source (post meta bound to a hash of the content). Search results and feed descriptions use it while the post is unchanged. All 24 block cases of the golden data match.
  - **Gallery images:** the slides are limited to the theme's content width, as `wp_get_attachment_image( 'large' )` does: 625×391 instead of 1024×640, with `sizes="auto, …"`. Themes declare the width in `theme.json`; plugins get it from `site_info`. Photo Slider & Lightbox is now 2.0.1.
  - **Twenty Twelve's `navigation.js`** used jQuery, which GoPress does not load. It threw on every page, and submenus did not open on keyboard focus or on the first tap. It is ported to plain JavaScript.
- **Load** in the hardened container with a 768 MiB limit:
  - Cached pages: about 130,000 requests/s (p99 about 2 ms), no errors.
  - Memory: about 490 MiB after the run. The live heap is about 170 MB, 100 MB of it the WASM plugin instances; with Go's default GC goal and `GOMEMLIMIT=600MiB` this fits.
- **Checks:**
  - `go test ./...` and `-race` for the new packages.
  - E2E 31/31; parity 162/162 against the local reference and against the live site.
  - `make plugins-check`, `make image-check`, `make image-smoke`.
  - govulncheck: nothing that GoPress calls.

## Decisions
- **The site owner writes the compose files and runs the server steps.** GoPress delivers the image, an example and a runbook; the repository stays generic. The live server was only read for planning: Traefik setup, networks and paths.
- **Images go to a private registry (ghcr.io)**, pushed by hand after `make release`. The commits are not pushed.
- **Go-live with a fresh import at the cutover.** WordPress is stopped first (content freeze, so no comment or donation is lost) and never deleted. The imported admin sets up the second factor with a CLI setup code.
- **Staging switches in the binary, not only in the proxy:** with `--disable-outbound`, a copy of the live data cannot use the live Akismet key, PayPal or the mail server by mistake. `--noindex` keeps pages and sitemaps unchanged, so staging can be compared with the live site.
- **The backup contains `secret.key`:** without it the 2FA secrets and the SMTP password cannot be read. The archive is 0600; the docs say to keep backups as private as the server.
- **No restore over existing data:** restoring into a fresh directory and switching is safer and works on a mount point.

## Security review
An independent review agent checked:
- the image and build;
- backup and restore (archive attacks, links, permissions, the secret);
- proxy headers;
- the staging switches;
- the excerpt meta;
- the gallery change;
- the docs and the runbook.

It found no Critical or High issues. Every finding was fixed, with regression tests where it is code (`TestReviewFindings` in backup, `TestEnvBool`, `TestServeRefusesBadSwitches`, `TestImportDSNNotPrinted`, `TestForwardedFromTrustedProxy`, `TestDisabled` in mail).

| Finding | Severity | Fix |
|---|---|---|
| Staging switches with values like `yes`/`on`/typos were silently off, so a staging copy could mail people and call Akismet/PayPal | Medium | Unknown values stop the server; yes/no/on/off accepted; state logged at every start |
| `GOPRESS_IMPORT_DSN` (with the password) was the flag default and printed in usage messages | Low | Read after parsing |
| The "backup outside the data directory" check compared path strings (links, bind mounts) | Low | Compared by file identity up the tree |
| CLI commands ignored `--disable-outbound`; staging mail stayed queued and could go out after a restart without the switch | Low | CLI follows `GOPRESS_DISABLE_OUTBOUND`; mail is marked failed while disabled |
| `make image-push` let untracked files into a "clean" image and pushed whatever `:latest` was | Low | Refuses untracked files, rebuilds and checks, tags `:latest` from the checked version |
| From a trusted proxy, `X-Real-Ip` and the first `X-Forwarded-For` line won (a proxy adding its own line let clients choose their address); the docs trusted a whole shared network | Low | Last `X-Forwarded-For` entry over all lines, last `X-Forwarded-Proto`; docs: a network only Traefik and GoPress join |
| Runbook: staging access optional, import with the app's DB user, passwords in `mariadb` argv | Low | IP allow list required; read-only import user; `MYSQL_PWD` |
| No HSTS and proxy client IPs when `--trusted-proxies` is missing behind TLS | Info | Startup warning |
| Restore accepted nested entries below single files (stray directories) | Info | Nested entries only below uploads/plugins/themes |
| Retention could delete the archive just written (clock skew); killed backups left temp files; the snapshot was briefly 0640 | Info | Fixed |
| The importer copied WordPress post meta named `_gopress_*` | Info | Skipped |
| README run example without the hardening it describes; helper image of the smoke test not pinned | Info | Fixed |
| Restore size limit of 50 GiB lets a gzip bomb fill a disk before it fails (cleaned up afterwards); `dev/visual` runs Chrome without its sandbox | Info | Accepted (operator input, dev tool) |

## Open issues
- Staging and the cutover follow the runbook on the server (site owner):
  - a DNS record for the staging host;
  - a dedicated network between Traefik and GoPress;
  - a registry login with a read-only token;
  - a backup job (the server has none so far).
- The e2e suite logs browser page errors but does not fail on them; the jQuery error went unnoticed since M1.
- The local reference WordPress does not redirect `/feed/rss2/` (the live one does); parity skips that path against it.

## Next
- Staging setup by the site owner, then the comparison and acceptance against staging, then the cutover.
