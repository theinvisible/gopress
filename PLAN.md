# GoPress – a single-binary WordPress replacement in Go

Devlog: [docs/devlog/](docs/devlog/README.md)

## Context

Goal: a fast single binary in Go that is operated like WordPress in the admin, loads plugins as WASM and replaces an existing WordPress site without a visible break (same URLs, same look).

The first migration target (the *reference site*) is a WordPress 7.1 site running as a Docker stack (WordPress + MariaDB behind Traefik). Its setup drives the v1 scope:

**Reference site (read from the live installation):**
- Theme **Twenty Twelve** with a custom background (color + image) and header text color. No nav menus (default page list).
- Permalinks `/%year%/%monthnum%/%postname%/`, locale en_US, front page = latest posts.
- Content: 61 posts, 24 pages, 52 attachments (41 MB uploads), 39 terms, 1 user. Comments: 679 approved (650 comments + 29 pingbacks), 97 pending, ~2145 spam. 341 revisions (not imported).
- 15 posts and 3 pages use Gutenberg block markup, the rest is classic HTML (needs wpautop). Shortcodes in use: `[gallery ids=…]`, `[paypal-donation]`. Blocks: `foto-slider-lightbox/gallery {"galleryId":791}`, core gallery/image/columns/group/quote/list/code/more.
- Sidebar-1 widgets: search, block (empty), paypal_donations, text, recent-posts, categories, tag_cloud, archives, meta.
- Plugins:
  - **paypal-donations**: options `paypal_donations_options` (account, currency, button style, new tab, …).
  - **foto-slider-lightbox** (Swiper bundled locally): CPT `fslb_gallery` with meta `_fslb_image_ids` and `_fslb_show_captions`.
  - Legacy CPT `donation_list`: 53 IPN entries.
  - Replaced or dropped: akismet → own plugin; google-sitemap-generator, duplicate-post → core; wpforms-lite (unused), ai, analytics → dropped.

**Decisions:**
- Own lightweight editor instead of Gutenberg.
- Embedded SQLite.
- WASM plugins via wazero/Extism.
- Scope v1: comments + spam protection, Twenty Twelve 1:1, SEO/feeds, donation log (IPN) and an Akismet plugin.
- Go 1.27, module `github.com/theinvisible/gopress`.
- License GPL-2.0-or-later (ports WordPress code, theme CSS/JS).
- Footer credit: "Proudly powered by GoPress".
- All repo docs and code comments in English; devlog entries live in `docs/devlog/`.

## Architecture

- **Single binary** `gopress` (CGO_ENABLED=0). Everything embedded via `go:embed`: admin UI, editor bundle, theme, bundled plugins.
- **Runtime data** in `/data`: `gopress.db`, `uploads/`, `plugins/` (uploaded plugins), `themes/` (optional overrides).
- **Stack:**
  - **Go 1.27**: `go.mod` with `go 1.27`, `toolchain go1.27.3`. Docker build stage `golang:1.27`.
  - Routing: stdlib `net/http` (ServeMux patterns).
  - Templates: `html/template`.
  - DB: `modernc.org/sqlite` (WAL).
  - Plugins: `github.com/extism/go-sdk` (wazero).
  - Sanitizing: `bluemonday`.
  - Images: `golang.org/x/image/draw`.
  - Compression: `andybalholm/brotli`.
  - Import only: `go-sql-driver/mysql`.
  - Admin: server-rendered `html/template` + small vanilla JS (no inline scripts, strict CSP). Editor: TipTap (vanilla, esbuild; bundle committed, so building needs no Node).
- **Performance:**
  - In-memory full-page cache for anonymous GETs, pre-compressed with br/gzip, ETag/304.
  - Invalidation via a global generation counter bumped on every write.
  - Plugin calls only happen on cache misses.
  - Static assets with content hash and `immutable`. Uploads with long cache headers.
  - Target: >10k req/s for cached pages.

### Project layout
```
cmd/gopress/            main: serve | import-wp | user | backup | plugin-pack
internal/db/            schema, migrations, queries (posts, terms, comments, media, options, users, sessions, plugin_kv)
internal/content/       posts/pages/terms/comments/revisions logic, scheduling, duplicate
internal/render/        shortcode parser (WP-compatible), wpautop/wptexturize ports, built-in [gallery]/[caption], filter pipeline
internal/media/         upload, resize (thumbnail 150, medium 300, medium_large 768, large 1024), srcset
internal/site/          public handlers, permalink router, feeds, sitemap, robots.txt, search, archives, redirects, page cache
internal/theme/         theme loader, widgets (search, text, recent posts, categories, tag cloud, archives, meta + plugin widgets), menus
internal/admin/         wp-admin-like UI (server-rendered + vanilla JS)
internal/auth/          sessions, CSRF, lockout, argon2id + verification of WP hashes (phpass & WP 6.8 bcrypt "$wp$2y$…"), TOTP, passkeys, recovery codes, capabilities, audit log
internal/content/       write logic: posts/pages (slugs, scheduling, terms, revisions, trash), comment moderation
internal/antispam/      honeypot, HMAC time token, per-IP rate limit, link heuristics
internal/plugins/       Extism host, manifest, hook registry, instance pool, zip install, host functions
internal/wpimport/      MariaDB → SQLite, block → HTML conversion, plugin data stash
web/admin/  web/editor/ admin templates/CSS/JS (wp-admin stylesheets, GPL), TipTap sources → web/admin/static/editor
themes/twentytwelve/    Go template port + original CSS (GPL), Open Sans self-hosted
pdk/go/                 plugin SDK for plugin authors (wraps extism go-pdk + JSON ABI)
plugins/paypal-donations/  plugins/foto-slider-lightbox/  plugins/akismet/
dev/                    test data fetch, dev MariaDB + reference WordPress, golden generators, parity checker
docs/devlog/            devlog, one file per session/milestone
Makefile                editor → plugins (.zip) → embed → binary; Dockerfile (FROM scratch)
```

## Components

### 1. Data model & import (`internal/db`, `internal/wpimport`)

**Tables:**
- `posts` (ids kept, type post/page, status, slug, title, content, excerpt, local/GMT dates, modified, author, parent, menu_order, comment_status, password, template, featured_media)
- `post_meta`, `slug_redirects`, `terms` (category/post_tag, hierarchical), `post_terms`
- `comments` (threading, type comment/pingback, status approved/pending/spam/trash, IP, UA)
- `media` (file, sizes JSON, alt, caption, mime)
- `options` (KV), `widgets`, `users`, `sessions`, `revisions`
- `plugins` (name, version, active), `plugin_kv` (plugin, collection, key, JSON)
- `import_stash` (plugin-owned WP data for the M4 plugins)

**Import:** `gopress import-wp --dsn … --uploads …`
- **IDs stay the same.** `?p=`, `?page_id=` and shortcode IDs keep working.
- **Block content:** delimiters of static core blocks are stripped, the HTML stays. Dynamic plugin blocks become shortcodes, e.g. `foto-slider-lightbox/gallery` → `[fotogalerie id="791"]`.
- **Classic content:** converted once into real `<p>` HTML with the wpautop port. The original is kept in `post_meta._wp_original_content`.
- **Uploads:** copied 1:1 to `/data/uploads` including existing sizes. Paths `/wp-content/uploads/YYYY/MM/…` stay valid via a route.
- **Plugin data:** plugin-owned CPT posts, meta and options (`fslb_gallery`, `donation_list`, `paypal_donations_options`, `widget_paypal_donations`, `wordpress_api_key`) go into `import_stash`; each bundled plugin migrates its part through its `migrate_wordpress` export. The core stays generic.
- **Widgets and theme mods** from `sidebars_widgets`/`widget_*` are converted into GoPress' widget configuration.

### 2. Frontend (`internal/site`, `themes/twentytwelve`)

- **Twenty Twelve port:**
  - Templates: index, single, page (+ full width), archive (category/tag/date/author), search, attachment, 404.
  - Comments section incl. form, sidebar, customizer values (background, header color).
  - A subset of the `wp-block-library` CSS for `wp-block-*` classes (columns, group, image, gallery, quote).
- **Permalink engine** with WP tokens, plus:
  - Redirects for `?p=`, `?page_id=`, `?cat=`, old slugs, trailing slash, 404 slug guessing.
  - Feeds: `/feed/`, `/comments/feed/`, `/category/x/feed/`, `/tag/x/feed/`, per-post comment feeds (RSS2).
  - `/wp-sitemap.xml` and `/sitemap.xml` (index + posts/pages/terms), `robots.txt`, canonical, pagination `/page/N/`.
- **Comments:** threading, "awaiting moderation" via cookie, Gravatar.
- **Spam pipeline:**
  1. Built-in antispam
  2. Plugin filter `comment_pre_save` (Akismet)
  3. Moderation rules from Settings → Discussion

### 3. Admin (`internal/admin`, `web/admin`)

- **Look:** based on wp-admin (its stylesheets and markup): dark side menu, admin bar, list tables with status filters (All/Published/Draft/Trash), search, bulk actions, row actions (Edit/Quick Edit/Trash/View/**Duplicate**), pagination. Not a pixel copy: where something can be nicer or more practical (login and 2FA screens, inline actions without reloads), GoPress deviates.
- **Login:** `/login` (password + second factor, or a passkey alone); admin under `/admin/`. `GET /wp-admin/…` and `GET /wp-login.php` redirect; other requests to `/wp-login.php` get 404.
- **Menu:**
  - Dashboard: At a Glance, Activity/Recent Comments, Quick Draft, plugin dashboard widgets
  - Posts: All, Add New, Categories, Tags
  - Media: grid/list, drag & drop upload, details with alt/caption
  - Pages
  - Comments: Approve/Unapprove/Spam/Trash/Reply, badge counter
  - Appearance: Widgets (drag order per sidebar), Menus, Customize-lite
  - Plugins: list, activate/deactivate, upload `.zip`, delete, settings links, plugin admin pages
  - Users: profile, password, additional users with roles admin/editor/author
  - Settings: General, Writing, Reading, Discussion, Media, Permalinks
- **Editor screen** like the classic WP editor:
  - Title, permalink edit, content editor.
  - Side boxes: Publish (status, visibility, scheduling, preview), Categories, Tags, Featured Image, Excerpt and Discussion.
  - Autosave, revisions with restore.

### 4. Editor (`web/editor`, TipTap/ProseMirror)

- **Modern and fast:** slash commands (`/`), floating format menu on selections, Markdown shortcuts, keyboard shortcuts, distraction-free mode, word count; framework-free bundle loaded only on the editor screen.
- **Tabs "Visual"/"HTML"**; HTML = the stored source.
- **Toolbar:** paragraph/headings/preformatted, bold/italic/strike/code, lists, quote, separator, alignment, link, table, read-more, undo/redo; "Add Media" with a media picker (image with size, alignment, caption and link; several images become a gallery).
- **Lossless:** every stored block is parsed and serialized again on load; anything not equivalent stays a raw HTML block (shown as a sandboxed preview). Shortcodes are blocks with an edit dialog. Inline elements without a button (colored spans, `<mark>`, `<sup>` …) keep their attributes. Verified over all imported documents (`make roundtrip`: no loss, identical rendering).
- **Classic content** (wpautop at render time) is converted to HTML when opened (texturize → autop → shortcode unautop, the order of `the_content`), which renders identically.
- **Extension API** `window.gopress.editor.registerInsert({id, label, icon, onClick})`. Plugins register e.g. "Insert photo gallery" through it. `window.gopress.media.pick({multiple})` is also available for plugin admin pages.

### 5. WASM plugin system (`internal/plugins`, `pdk/go`)

**Package:** `.zip` with
- `plugin.json`: name, version, author, description, capabilities, allowed HTTP hosts, shortcodes, widgets, admin pages, routes, hooks, assets, editor scripts
- `plugin.wasm`
- `assets/`, `editor/`

Plugins are bundled in the binary but can be deactivated and replaced by upload, as in WP.

**Guest exports** (all JSON in/out):
- `render_shortcode`, `render_widget`, `filter` (hook name in the input), `action`
- `admin_page` (returns a form schema or HTML), `admin_submit`
- `http_route`, `migrate_wordpress`

**Host functions:**
- `kv_get/set/delete/list` (namespaced per plugin), `option_get`
- `posts_query`, `media_get` (URLs, srcset, alt, caption)
- `http_request` (allowed hosts only), `log`, `site_info`
- `comment_get`, `comment_set_status`

**Hooks v1:**
- `the_content`, `comment_pre_save` (result approve/hold/spam/discard)
- `comment_status_changed`, `dashboard_widgets`, `head`/`footer`

**Assets:** a render result declares the assets it needs; the page includes them only when needed (as the photo plugin does today).

**Runtime:**
- Compiled modules are cached. Each plugin has an instance pool for concurrency.
- Every call has a timeout (context) and a memory limit. A panic disables only the plugin, never the server.

**PDK:** `pdk/go` for Go plugins, built with standard Go 1.27 (`GOOS=wasip1 GOARCH=wasm -buildmode=c-shared`, exports via `//go:wasmexport`). TinyGo is only the fallback if binaries get too large. `gopress plugin-pack` builds the zip.

### 6. First plugins (`plugins/…`)

**foto-slider-lightbox** (port of the existing PHP plugin):
- **Galleries** in the collection `galleries` (title, image_ids, show_captions). Import of `fslb_gallery` 791 with the same ID.
- **Admin:** page "Photo galleries" with list, new/edit, image selection via the media picker, drag reorder and caption toggle.
- **Output:** shortcode `[fotogalerie id=""]`. The markup is taken 1:1 from `includes/render.php`.
- **Assets:** `view.css`, `view.js` and Swiper are taken over unchanged.
- **Editor:** insert button with gallery selection.

**paypal-donations:**
- **Settings page** with the fields from `Admin.php`: account, currency, page style, return page, amount, purpose, reference, button small/large/custom, button URL, localization, new tab, checkout language, return method, sandbox, disable stats and IPN validation.
- **Output:** shortcode `[paypal-donation purpose reference amount]` with markup identical to `views/paypal-button.php`, plus the "Donate" widget.
- **IPN:**
  - Route `/plugins/paypal-donations/ipn`; the old URL `/?ppd_paypal_ipn=process` is mapped.
  - Verification via postback to `ipnpb.paypal.com` (HTTP allowlist), then the donation is stored.
  - Admin list "Donations" with date, name, email, amount, currency, memo and status. Import of the 53 `donation_list` entries.

**akismet:**
- **Settings:** API key (imported from `wordpress_api_key`), "Verify key" via `/1.1/verify-key`, option "discard obvious spam".
- **Hooks:**
  - `comment_pre_save` → `comment-check`.
  - `comment_status_changed` → `submit-spam`/`submit-ham`.
- **Dashboard widget** with counters.

## Security (high priority)

GoPress serves a public, internet-facing site. Security follows current best practice (OWASP ASVS 5, OWASP Top 10) in every milestone; each milestone ends with a security review recorded in the devlog.

**Public site**
- Treat all input as hostile: comment HTML sanitized with an allowlist (kses equivalent), output escaped by `html/template`, raw HTML only from trusted sources.
- No path traversal or directory listings when serving uploads and theme assets; request body limits; strict timeouts on the HTTP server.
- Security headers on every response: CSP without inline scripts (`script-src 'self'`), `frame-ancestors`, `X-Content-Type-Options`, `Referrer-Policy`, `Permissions-Policy`, HSTS behind TLS.
- Comment spam: honeypot, signed time token, per-IP rate limit, link limit, Akismet plugin.
- Only same-site redirects; no open redirects.
- `xmlrpc.php` and pingback receiving removed (common attack surface).

**Admin & login (M2a)**
- Passwords: argon2id (64 MiB, t=3), at most 4 hashes in parallel; WordPress hashes (`$wp$` bcrypt, phpass, bcrypt, MD5) verified and rehashed on login. Policy per NIST 800-63B: 12–256 characters, no composition rules, common-password blocklist, not the login or email.
- Two-factor authentication is mandatory for every user: TOTP (secret encrypted with AES-GCM, replay protection) or passkeys (WebAuthn, user verification required); 10 single-use recovery codes (HMAC-hashed). Until a factor is set up, a session can only reach the setup page. A passkey alone is a complete login.
- Login rate limiting per IP (/64 for IPv6) and progressive lockout per account name (also for unknown names); one generic error message and a dummy hash for unknown users (no enumeration). Passkey logins are not subject to the password lockout, so attackers cannot lock the owner out.
- Sessions: random 256-bit tokens, only SHA-256 stored; `__Host-` cookie `Secure`, `HttpOnly`, `SameSite=Strict`; new session id on login and after the second factor; idle and absolute timeouts (2 h/12 h, "remember me" 3 d/14 d); logout everywhere; password change ends other sessions.
- CSRF: per-session HMAC token on every state-changing request plus strict `Origin`/`Sec-Fetch-Site` checks; login form token bound to a pre-session cookie (no login CSRF).
- Re-authentication (password + factor, or passkey) within 10 minutes for password, email and factor changes and new recovery codes.
- Role/capability checks (WordPress roles) and ownership checks server-side in every handler and in the write layer; contributors/authors get their HTML sanitized.
- Audit log for logins, failures, lockouts, factor and password changes and content changes; users see their recent activity.
- Admin responses: `Cache-Control: no-store`, strict CSP (no inline scripts, `frame-ancestors 'none'`), `X-Frame-Options: DENY`, `Referrer-Policy: same-origin`; previews are never cached and 404 for everyone not allowed to edit.

**Plugins (M3)**
- WASM sandbox: no filesystem or network access except the host functions; HTTP only to allowlisted hosts; per-call timeout and memory limit; plugin zip uploads validated (size, paths, manifest).

**Operations (M5)**
- Run as non-root in a `FROM scratch` image, read-only root filesystem, data volume only.
- Secrets (session/HMAC keys) generated on first start and stored in the data dir with 0600 permissions.
- Dependency updates and `govulncheck` in the build.

## Milestones & checklists

Legend: `[ ]` open · `[x]` done · `[~]` in progress

### M0 – Setup
- [x] Create PLAN.md with devlog
- [x] `git init`
- [x] Install Go 1.27 toolchain locally (go1.27.3 in `~/sdk/go1.27.3`)
- [x] `go mod init` (`go 1.27`), skeleton `cmd/gopress`, Makefile, `.gitignore`
- [x] Local test data: fresh dump, uploads, plugin/theme sources (`make testdata`), dev MariaDB (`make devdb-up`), reference counts (`make devdb-counts`)

### M1 – Core & import (*result: the imported site runs locally, read-only*)
- [x] Docs in English, devlog moved to `docs/devlog/`, LICENSE (GPL-2.0-or-later)
- [x] SQLite schema and migrations (`internal/db`)
- [x] PHP unserialize (`internal/phpser`)
- [x] `import-wp`: posts/pages/terms/comments/users/options/media/widgets with the same IDs, plugin data stash
- [x] Block → HTML conversion like `do_blocks()` (classic-theme block supports), plugin blocks become shortcodes, same-site URLs become root-relative
- [x] Render pipeline: wpautop, wptexturize, smilies, shortcode parser, built-in `[gallery]`/`[caption]`, content image filter (srcset), more tag, excerpts, make_clickable
- [x] Golden tests against PHP output (reference WP container)
- [x] Copy uploads, route `/wp-content/uploads/…`
- [x] Twenty Twelve port: index/single/page/full-width/archive/search/attachment/404, sidebar widgets, customizer values
- [x] Comments display (threading, pagination) and form incl. antispam
- [x] Permalink router and redirects (`?p=`, `?page_id=`, trailing slash, old slugs, slug guessing)
- [x] RSS feeds, sitemaps, robots.txt, canonical, search (WordPress-compatible LIKE search instead of FTS5)
- [x] Page cache (br/gzip, ETag), load test
- [x] Parity check against a local reference WordPress (`dev/parity`): 162/162 URLs identical
- [x] Security: headers/CSP, comment sanitizing, file serving hardening, server limits, govulncheck, security review (12 findings fixed, see devlog)

### M2a – Admin core (*result: the site can be run and written with*)
- [x] Auth: login, sessions, CSRF, rate limit + lockout, argon2id, WP hash verification + rehash (see Security)
- [x] 2FA (mandatory for all users): TOTP and passkeys (WebAuthn, also as passwordless login), recovery codes
- [x] Audit log, re-authentication for sensitive actions, `gopress user` break-glass CLI
- [x] Admin layout (menu, admin bar, color schemes), dashboard (at a glance, activity, quick draft)
- [x] Posts/pages: list table, filters, bulk/row actions, duplicate, quick edit
- [x] Editor screen incl. side boxes, autosave, preview, scheduling, revisions with compare/restore
- [x] TipTap editor (visual/HTML, slash commands, media picker, shortcode and HTML blocks, extension API), lossless round trip
- [x] Comment moderation (inline approve/spam/trash/reply/quick edit, edit screen)
- [x] Profile and security page (factors, recovery codes, sessions, recent activity)
- [x] Tests: unit + HTTP tests, e2e suite with the local Chrome incl. passkeys (`make e2e`), editor round trip (`make roundtrip`)
- [x] Security review (see devlog)

### M2b – Admin completion
- [ ] Media library with upload and resizes/srcset
- [ ] Categories/tags screens
- [ ] Appearance: widgets, menus, customize-lite
- [ ] Users (list, add, roles), settings (General/Writing/Reading/Discussion/Media/Permalinks)
- [ ] Admin bar on the public site for logged-in users
- [ ] Mail (lost password, notifications)

### M2c – MCP interface for AI agents (*to be planned*)
- [ ] Generic MCP server (Streamable HTTP, `/mcp`) so any MCP-capable agent (e.g. Claude Code) can write posts, e.g. release announcements
- [ ] Tools: list/read/draft/edit posts and pages, media upload, categories/tags, optional publish
- [ ] Highly secure: per-user scoped tokens or OAuth 2.1 (MCP authorization), created only after re-authentication, drafts-only by default, expiry and revocation, rate limits, audit log, content sanitized like for authors without `unfiltered_html`

### M3 – Plugin host
- [ ] Manifest format `plugin.json` and zip install/delete
- [ ] Extism host: module cache, instance pool, timeouts, memory limits, panic isolation
- [ ] Host functions (kv, option, posts, media, HTTP allowlist, log, comments)
- [ ] Hook registry (`the_content`, `comment_pre_save`, `comment_status_changed`, `dashboard_widgets`, `head`/`footer`)
- [ ] Shortcodes, widgets, routes, admin pages (form schema), on-demand assets, editor scripts
- [ ] `pdk/go`, `gopress plugin-pack`, example plugin
- [ ] Plugins admin: list, activate/deactivate, upload
- [ ] Security review of the plugin sandbox and upload

### M4 – Plugins
- [ ] foto-slider-lightbox: gallery admin, shortcode, assets, editor button, import of `fslb_gallery`
- [ ] paypal-donations: settings, shortcode, widget, IPN + verification, donations list, import
- [ ] akismet: settings + verify key, comment-check, submit-spam/ham, dashboard widget

### M5 – Deployment
- [ ] Dockerfile (`golang:1.27` build → `FROM scratch`), volume `/data`
- [ ] `gopress backup` (`VACUUM INTO` + tar of the uploads)
- [ ] Staging hostname behind Traefik
- [ ] URL parity crawl and visual comparison with live
- [ ] Hardening: non-root, read-only rootfs, secrets in data dir, `govulncheck`
- [ ] Cutover: switch the router, stop (not delete) the WP containers

## Verification

- **Unit/golden tests:**
  - Shortcode parser against WP regex cases.
  - wpautop/wptexturize against PHP output from the reference container (golden files).
  - Permalink routing, block → HTML conversion.
  - Password verification with real hash formats (phpass, `$wp$2y$`).
- **Import test:** import from the dev MariaDB (fresh dump via `make testdata`) and compare counts with `local/reference-counts.txt`.
- **URL parity:** crawl all URLs from the reference sitemap against GoPress (locally, later staging). Expected: same status/redirect, same `<title>`, same normalized content and sidebar. Plus screenshots (home, post, page with gallery, donate page).
- **Plugins:**
  - Gallery: test slider/lightbox/keyboard manually.
  - PayPal: IPN against the PayPal sandbox simulator.
  - Akismet: with test author `viagra-test-123` or `akismet-guaranteed-spam@example.com`.
  - Deactivate a plugin zip, upload it again, a panicking plugin → server keeps running.
- **Load:** `oha` against home and a post (cached/uncached). Target >10k req/s cached.
- **Staging acceptance** before the cutover.
