# 2026-10-24 – M1 core & import

## Done
- **Storage** (`internal/db`): SQLite via `modernc.org/sqlite`, one writer connection plus a read-only pool (WAL), embedded migrations, schema for users, posts (posts, pages, attachments), meta, old slugs, terms, media, comments, options, widgets and an import stash for plugin data.
- **Import** (`gopress import-wp`, `internal/wpimport`, `internal/phpser`):
  - Same IDs as WordPress; PHP-serialized options, widgets and attachment metadata are decoded.
  - Block content is rendered like `do_blocks()` does for classic themes (`wp-block-paragraph`, list/quote/column layout classes, group inner container, image outer container, gallery `data-id`). Verified against `do_blocks()` output of the reference WordPress for all 22 block documents.
  - Classic content is kept raw (`content_format = wpautop`) and gets `wpautop` at render time, exactly like `the_content`.
  - Plugin-owned data (`fslb_gallery`, `donation_list`, PayPal and Akismet options) is stashed for M4.
  - Counts match `local/reference-counts.txt` (61 posts, 24 pages, 52 media, 650 + 29 approved comments, 97 pending, 2145 spam, 147 old slugs).
- **Render pipeline** (`internal/render`): ports of `wpautop`, `shortcode_unautop`, `wptexturize`, `convert_smilies`, `capital_P_dangit`, `do_shortcode`/`shortcode_parse_atts`/`strip_shortcodes`, `gallery_shortcode`, `img_caption_shortcode`, `wp_filter_content_tags` (width/height, srcset, sizes, loading), the more tag, `wp_trim_excerpt`, `make_clickable`. Golden tests against PHP output from the reference container (`make golden`).
- **Public site** (`internal/site`, `internal/theme`, `themes/twentytwelve`):
  - WordPress-compatible routing: permalink structure, hierarchical pages and categories, tag/author/date archives, search, attachment pages, comment pages, query URLs (`?p=`, `?page_id=`, `?cat=`, …), trailing slash, old-slug and slug-guess redirects.
  - Twenty Twelve as Go templates with the original CSS/JS, page menu, widgets (search, text, block, recent posts, categories, tag cloud, archives, meta).
  - Threaded, paginated comments and the comment form; RSS2 feeds (posts, comments, per post, term, author, search); core sitemaps plus `/sitemap.xml`; `robots.txt`.
  - In-memory page cache (LRU, generation-based invalidation, precompressed brotli/gzip, ETag/304), bypassed for visitors with commenter cookies.
- **Parity** (`dev/parity`): crawls 162 URLs (sitemaps, archives, paging, comment pages, feeds, redirects, 404s) against the reference WordPress and compares status, redirect target, title, body classes and the normalized content, sidebar and menu. Result: **162/162 identical** (plugin output and intentional differences excluded, see below). Screenshots of home, post, gallery page and block page match visually.
- **Performance** (`dev/bench`, 20-core dev machine, client on the same host):
  - Cached: ~170,000 req/s at 64 connections, p99 < 2 ms.
  - Uncached render: p50 ≈ 3 ms per page (6 ms for the page with 280 comments); the reference WordPress needs ≈ 31 ms.
- **Security** (see below).

## Decisions
- **wpautop at render time** instead of baking it into the stored content: `wptexturize` runs before `wpautop` in WordPress, and the order matters for quotes next to line breaks. The editor (M2) will convert classic content to HTML on first save.
- **Search like WordPress** (`LIKE` on title, excerpt and raw content, stopwords, relevance order) instead of SQLite FTS5: identical results and paging; the site is small enough.
- **Sorting** of pages, categories and tags with `golang.org/x/text/collate` to match MySQL's `utf8mb4_unicode_ci`.
- **Intentional differences** to WordPress: Atom/RDF feeds and `?feed=` redirect to the RSS2 URL; old slugs also redirect without the date prefix; footer and meta widget credit GoPress; `xmlrpc.php` returns 410 (pingback receiving removed).
- **Plugin shortcodes** `[paypal-donation]` and `[fotogalerie]` render nothing until the plugin host exists (M4); their output is excluded from the parity check.
- Comment output is **sanitized last** (after the WordPress filters), so the typography matches WordPress and the HTML is safe regardless of what is stored.

## Security
- Security headers on every response: CSP with `script-src 'self'` (no inline scripts), `frame-ancestors 'self'`, `X-Content-Type-Options`, `X-Frame-Options`, `Referrer-Policy`, `Permissions-Policy`, COOP, HSTS behind TLS.
- Comments: allowlist sanitizer (bluemonday) on save and on output, `rel="nofollow ugc"` added on parsed tokens, only http(s) author URLs, honeypot, HMAC-signed single-use form token (min 3 s, max 48 h; refreshed via same-origin JS for cached pages), rate limit per IPv4 address / IPv6 /64 plus a site-wide limit, link limit, duplicate check, cross-site post rejection (`Origin`/`Sec-Fetch-Site`), body limit 64 KiB.
- Pending comments are only shown to their author via a signed, HttpOnly cookie (WordPress uses a forgeable email cookie); the form is pre-filled client-side so all other pages come from the cache.
- Uploads: only media files (with their generated sizes) and files referenced by published content are served, with an extension allowlist, no dotfiles, access through `os.Root` (no symlink escape); downloads get `Content-Disposition: attachment`, SVG a sandbox CSP. Import skips dotfiles and PHP files.
- Page cache: key built from known query parameters only; cannot be bypassed with forged cookies; personalized pages are `private, no-store`.
- HTTP server timeouts and header size limit; site secret generated with `crypto/rand`, stored with mode 0600, refused if group/world readable.
- Client IP from `X-Real-Ip`/`X-Forwarded-For` only if the peer is in `--trusted-proxies`.
- `govulncheck`: no known vulnerabilities.
- Tests cover reflected-input escaping, security headers, upload confinement (traversal, symlinks, plugin files, dotfiles), attribute injection in the sanitizer, comment spam checks, single-use tokens, IPv6 /64 limits, cross-site posts and cache bypass attempts.

### Security review (independent agent)
| Finding | Severity | Fix |
|---|---|---|
| `/wp-content/uploads/` served everything, incl. plugin files WordPress protected with `.htaccess` (e.g. `wpforms/cache/*.json`) | High | Allowlist (media + referenced files), extension allowlist, no dotfiles, `os.Root` |
| Theme asset hash cache grew with every requested name (memory DoS) | High | Hashes computed once at load, misses never stored |
| `rel` rewriter regex on sanitizer output allowed attribute injection | Medium | Rewritten on parsed tokens; regression test |
| Cache bypass via cookies / random query strings, quadratic `BalanceTags`, expensive pending-comment renders | Medium | Normalized cache key, signed commenter cookie, linear `BalanceTags`, cheaper compression |
| Rate limit per IPv6 address, reusable tokens, cache flush on every comment | Medium | /64 + global limit, single-use tokens, invalidate only on approved comments |
| Proxy headers trusted from any peer | Low | `--trusted-proxies` CIDR list |
| Pending comments readable via forged email cookie | Low | Signed commenter cookie |
| No cross-site check on comment posts | Low | `Origin`/`Sec-Fetch-Site` check |
| Personalized pages without cache headers | Low | `Cache-Control: private, no-store` |
| User sitemap listed all users | Low | Only authors with published posts |
| Cookie consent checkbox ignored | Low | Cookies only with consent when the option is on |
| Feed search not length-capped | Low | 200-byte limit |

## Open issues
- The live WordPress auto-approves comments (`comment_moderation` and `comment_previously_approved` are off). GoPress keeps these settings; until Akismet (M4) runs, spam would be published. Recommendation: enable "comment must be manually approved" or "previously approved" before going live.
- `excerpt_remove_blocks()` drops some blocks from excerpts in WordPress; GoPress cannot after import. Only affects search result excerpts of block posts.
- The editor (M2) must handle `content_format = wpautop` content.

## Next
- M2: admin area with login (argon2id, 2FA with TOTP/passkeys, sessions, CSRF), post/page editor, media, comments moderation, settings.
