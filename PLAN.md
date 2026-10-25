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

### 6. Bundled plugins (`plugins/…`, M4)

The source site's three plugins, ported onto the host and bundled (`plugins/bundled/zips`, reproducible builds, `make plugins` / `make plugins-check`). They are activated like uploads: through the approval screen or `gopress plugin activate --import`.

**foto-slider-lightbox** ("Photo Slider & Lightbox"):
- **Galleries:** KV `galleries` (title, image ids, captions, owner). Imported from `fslb_gallery` with the same ids.
- **Output:** `[fotogalerie id captions]` from `templates/gallery.html`. Swiper slider plus lightbox (`view.js` ported; image data in data attributes instead of a JSON script).
- **Admin:** "Photo Galleries" page in a sandboxed iframe (media picker, drag and keyboard sorting). Authors edit their own galleries, editors all. The editor button opens a dialog page.

**paypal-donations:**
- **Settings:** the original options plus "Record donations (IPN)" and admin mail.
- **Output:** `[paypal-donation]` and the widget, with the original markup, from templates.
- **IPN** (opt-in): `/plugins/paypal-donations/ipn`, and the old `/?ppd_paypal_ipn=process`.
  - The postback must answer `VERIFIED`; account, new transaction and status are checked; refunds update the donation.
  - Records are minimal (no addresses).
  - Donations list (search, delete), dashboard box, live notice and mail.
- **Import:** the options and the 53 `donation_list` entries of the older plugin.

**akismet** ("Akismet Anti-spam"):
- **Settings:** a write-only key, checked with `verify-key` on save; the "discard obvious spam" option.
- **`comment_pre_save` → `comment-check`:** an error or no answer within 3 s holds the comment.
- **`comment_status_changed` → `submit-spam`/`submit-ham`.**
- **Dashboard box** with counters.
- **Import:** key, strictness and spam count.

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
- Two-factor authentication is mandatory for every user: TOTP (secret encrypted with AES-GCM, replay protection) or passkeys (WebAuthn, user verification required); 10 single-use recovery codes (HMAC-hashed). The first factor needs a one-time setup code from an administrator or the CLI; until a factor is set up, a session can only reach the setup page. A passkey alone is a complete login. Second-factor attempts are reserved atomically and limited per pending login (5) and per user (10, then locked); failures are shown to the user at the next login.
- Login rate limiting per IP (/64 and /48 for IPv6) and progressive locks per account name and client network (also for unknown names), plus an account-wide limit against distributed guessing that browsers with a device cookie of the account are exempt from; attempts are counted atomically before the check; one generic error message and one argon2id run per failure (no enumeration, also not by timing). Passkey logins are not subject to the password lockout.
- Sessions: random 256-bit tokens, only SHA-256 stored; `__Host-` cookie `Secure`, `HttpOnly`, `SameSite=Strict`; new session id on login and after the second factor; idle and absolute timeouts (2 h/12 h, "remember me" 3 d/14 d); logout everywhere; password change ends other sessions.
- CSRF: per-session HMAC token on every state-changing request plus strict `Origin`/`Sec-Fetch-Site` checks; login form token bound to a pre-session cookie (no login CSRF).
- Re-authentication (password + factor, or passkey) within 10 minutes for password, email and factor changes and new recovery codes.
- Role/capability checks (WordPress roles) and ownership checks server-side in every handler and in the write layer; contributors/authors get their HTML sanitized.
- Audit log for logins, failures, lockouts, factor and password changes and content changes; users see their recent activity.
- Admin responses: `Cache-Control: no-store`, strict CSP (scripts only from `/admin/static/`, no inline scripts, `frame-ancestors 'none'`), `X-Frame-Options: DENY`, `Referrer-Policy: same-origin`; previews are never cached and 404 for everyone not allowed to edit.

**Admin completion (M2b)**
- Mail only over TLS (except to a localhost relay); SMTP password encrypted, write-only, bound to its server; no header injection; mails with one-time links expire with them.
- One-time links (reset 1 h, invitation 72 h, admin email 48 h): 256-bit tokens, only hashes stored, single use, bound to purpose and password hash; moved out of the URL at once; resets end all sessions but never skip the second factor or its lock; no account enumeration (same answer, work after the response), rate limits per network and account.
- User management only for administrators with a recent re-authentication; no self-demotion or self-deletion; at least one administrator.
- Uploads: type allowlist checked against the content (no HTML, SVG or scripts), pixel and decoded-size limits with a shared memory budget, per-user upload limit, metadata removed, all file access through `os.Root`, temp files never served.
- Settings, widgets, menus, customizer: values validated and escaped; HTML only with `unfiltered_html`; menu URLs only http(s)/relative/mailto/tel; custom CSS cannot leave its style element.
- Logged-in visitors (toolbar, customizer preview, commenting): the session is validated by the admin; personal pages are never cached; customizer drafts only in the same-origin preview frame; logged-in comments need the session's CSRF token.
- SQL: values only as parameters; a `go/ast` guard test checks every query in the module.

**MCP interface (M2c)**
- Off by default; an administrator turns it on (with re-authentication); only over HTTPS (or loopback in development).
- Personal access tokens: 256-bit, only the SHA-256 stored, shown once, expire after at most a year, at most 10 per user; created only in a full 2FA session with a recent re-authentication (security notice by mail); revoked by the user, an administrator, a password change or reset, a 2FA reset, a role change and "log out everywhere".
- A token acts with its user's role limited to its scopes (`content:write`, `content:publish`, `media:upload`, `terms:manage`) through the normal capability checks; no scope allows unfiltered HTML, others' content, deleting or administration; agents see published content and their own only; updates use optimistic locking.
- `/mcp` accepts only Bearer tokens (no cookies, no CORS), refuses foreign `Origin`/cross-site fetches and JSON-RPC batches, limits failed authentication per network, calls per token, content and body size and concurrency; no tool fetches URLs; uploads only images and PDF through the media pipeline. Every change is audited with the token.

**Plugins (M3)**
- WASM sandbox (Extism/wazero): no files, environment or network; one host function checks every operation against the approved permissions; per call time limits (1 s render … 2 min import), 64 MB memory, bounded input/output; at most 4 instances per plugin; crashed instances are discarded, five crashes in a minute pause a plugin.
- Packages: strict manifest (unknown keys refused), zip checks (paths, links, duplicates, sizes counted while reading, entry count, file types, WASM magic); stored via `os.Root`.
- Approval: only administrators with a recent re-authentication upload, activate and delete; activation approves exactly the reviewed package (SHA-256); an update asking for more stays inactive until approved; everything audited.
- HTTP from plugins: HTTPS to approved host names only, public addresses checked after DNS resolution, redirects only to approved hosts, no proxies, size and time limits.
- Public pages: plugin HTML filtered (no scripts, handlers, iframes, javascript: URLs); scripts only from approved asset files, only on pages that need them; routes get no cookies, answer under a sandbox CSP without cookies or redirects, rate limited.
- Admin: plugin pages are forms and tables rendered by GoPress or custom pages in a sandboxed iframe (opaque origin, CSP sandbox, no network of its own) that reach the server only through a postMessage bridge with the admin's CSRF token; editor buttons insert escaped shortcodes, never HTML.

**Plugins and live updates (M4)**
- **Imported options:** granted one by one (the import holds every plugin's options, API keys too).
- **Settings:** plugins can set their own settings only during the import, validated like the admin form.
- **Mail from plugins:** only to the administrators. The host picks the recipients and writes the subject line. At most 20 an hour, audited.
- **Templates:** run inside the plugin's sandbox (time and memory limits); theme overrides are trusted theme files.
- **Live stream (`/admin/events`):**
  - Session cookie only, same-origin only, limited streams.
  - Every event names its receivers (capability, user, post or plugin page); topics are authorized when the stream opens.
  - The session is checked again every 25 s without counting as activity, so an open tab does not keep an idle session alive. Logout or revocation ends the stream.
  - Texts are shown with `textContent`.
- **Bundled plugins:**
  - **PayPal IPN:** confirmed by PayPal before anything is stored, then checked against the account, transaction and status. Donor records are minimal.
  - **Akismet:** holds comments when it cannot check them. The key is write-only.
  - **Gallery:** the lightbox only uses http(s) and same-site URLs.

**Operations (M5)**
- **Image:** `FROM scratch` (binary, CA bundle, user 65532), base image pinned by digest; the build context is an allowlist, so `local/` never reaches it; `make image-check` pins the exact file list.
- **Runtime:** non-root, read-only root filesystem, no capabilities, `no-new-privileges`, memory limit; the data volume is the only writable path; umask 027.
- **Secret:** one `secret.key` (0600, refused if more open), generated on first start; all keys are derived from it.
- **Proxy headers:** `X-Real-Ip`, `X-Forwarded-*` and `Forwarded` are dropped unless the peer is a trusted proxy.
- **Staging:** `--noindex` keeps search engines out; `--disable-outbound` stops plugin HTTP and mail, so a copy never uses the live site's keys or mails real people.
- **Backups:** `gopress backup` (consistent `VACUUM INTO` snapshot, 0600, includes the secret) and `gopress restore` (empty target only; refuses links, traversal, unknown entries and newer schemas). An older binary refuses a newer database schema.
- **Builds:** `make release` runs the tests, `govulncheck` (pinned), the plugin check and the image checks; images are only pushed from a clean tree.

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
- [x] Mail: SMTP queue with mandatory TLS, lost password, invitations, security notices, comment notifications
- [x] Media library with upload, metadata removal, sizes/srcset; paste and drop into the editor; featured images
- [x] Categories/tags screens
- [x] Appearance: widgets, menus, customizer with live preview
- [x] Users (list, add, roles, account security), settings (General/Writing/Reading/Discussion/Media/Permalinks/Email)
- [x] Admin bar, edit links and logged-in commenting on the public site
- [x] SQL injection guard test for every query
- [x] Security review (see devlog)

### M2c – MCP interface for AI agents
- [x] Generic MCP server (Streamable HTTP, `/mcp`, official Go SDK, stateless) so any MCP-capable agent (e.g. Claude Code) can write posts, e.g. release announcements
- [x] Tools: site info, list/get/create/update posts and pages (Markdown or HTML), terms, create category, media upload/list; publishing only with the `content:publish` scope
- [x] Personal access tokens with scopes, expiry and revocation (Profile › Access Tokens), switch in Settings › Writing, rate limits, audit log, agent content always sanitized
- [x] Tests (unit, HTTP with the SDK client, e2e), tried with Claude Code, security review (see devlog)
- [ ] Later, if needed: OAuth 2.1 authorization (MCP authorization spec) as an alternative to copying tokens

### M3 – Plugin host
- [x] Manifest format `plugin.json` and zip install/delete
- [x] Extism host: module cache, instance pool, timeouts, memory limits, panic isolation, circuit breaker
- [x] Host functions (kv, settings, posts, media, HTTPS to approved hosts, log, comments, import stash)
- [x] Hooks (`the_content`, `comment_pre_save`, `comment_status_changed`, dashboard boxes, `head`/`footer`)
- [x] Shortcodes, widgets, routes and legacy URLs, admin pages (forms, tables, sandboxed iframes with a bridge), on-demand assets, editor buttons
- [x] `pdk/go` (standard Go toolchain), `gopress plugin-pack`, example plugin `plugins/hello`, `docs/plugins.md`
- [x] Plugins admin: list, review and approve permissions, activate/deactivate, upload, delete, WordPress import
- [x] Security review of the plugin sandbox and upload (see devlog)

### M4 – Plugins
- [x] foto-slider-lightbox: gallery admin, shortcode, assets, editor button, import of `fslb_gallery`
- [x] paypal-donations: settings, shortcode, widget, IPN + verification, donations list, import
- [x] akismet: settings + verify key, comment-check, submit-spam/ham, dashboard widget
- [x] Host additions for them:
  - single imported options (`import:option:<name>`), request timeouts, settings import, `mail_admins`;
  - richer comment hooks, dialog pages outside the menu, checks after saving settings;
  - HTML templates with theme overrides.
- [x] Live updates over Server-Sent Events: moderation notices and badges, plugin paused, import progress, who else edits a post and newer saves, live preview in the editor, live notices and page events for plugins
- [x] `gopress plugin list|activate|deactivate`, bundled zips with reproducible builds
- [x] Tests (plugin modules with `pdk/go/fakehost`, host integration, e2e), parity without plugin exceptions, security review (see devlog)

### M5 – Deployment
- [x] Dockerfile (`golang:1.27` build → `FROM scratch`), volume `/data`, `gopress healthcheck`, `make release`/`image-push`
- [x] `gopress backup` (`VACUUM INTO` + tar of the uploads, secret, plugins) and `gopress restore`
- [x] Staging switches (`--noindex`, `--disable-outbound`), deployment docs, example compose, runbook
- [~] Staging hostname behind Traefik (the compose file and the server steps are done by the site owner, following `docs/deploy/runbook.md`)
- [x] URL parity crawl and visual comparison with live (`dev/parity -ref <live>`, `dev/visual`): a fresh import matches the live site 162/162; fixed on the way: canonical feed redirects, block excerpts, gallery image size, submenu script
- [x] Hardening: non-root, read-only rootfs, no capabilities, secrets in data dir, trusted proxy headers only, `govulncheck`
- [ ] Staging acceptance and cutover: switch the router, stop (not delete) the WP containers (runbook)

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
