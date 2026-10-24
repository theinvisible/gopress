# 2026-10-24 – Analysis & planning

## Done
- Analyzed the live stack on `root@hadler.me` read-only:
  - Containers `wp-app` (wordpress:7.1.3-php8.3-apache) and `wp-db` (mariadb:11.8) behind Traefik v3.7.
  - Data: wp-content under `/data/wordpress/wp-content`, DB in the volume `wordpress_db_mariadb118`.
- Content inventory (see PLAN.md context):
  - 61 posts, 24 pages, 52 attachments, 41 MB uploads.
  - 679/97/2144 comments (approved/pending/spam).
  - Block markup in 15 posts and 3 pages (the first rough count of 87 included revisions).
- Plugin usage:
  - `[paypal-donation]` on the Donate page (ID 187) and as a widget in sidebar-1.
  - `foto-slider-lightbox/gallery` (gallery 791) on the tiBackup page (ID 149).
  - `donation_list` holds 53 legacy entries.
- Created PLAN.md, initialized the repo.

## Decisions
- **Go 1.27**, single binary, CGO-free.
- **Embedded SQLite** (modernc.org/sqlite). Reason: true single-binary deployment, `wp-db` goes away.
- **Own lightweight editor** (TipTap) instead of Gutenberg. Reason: lighter; block content is converted to HTML on import.
- **WASM plugins** via Extism/wazero. Reason: pure Go, sandboxed, runtime upload like WP.
- **v1 scope:** comments + spam protection, Twenty Twelve 1:1, SEO/feeds, donation log (IPN), Akismet plugin.
- Same post IDs and upload paths as in WP. Reason: links, shortcode IDs and SEO stay stable.
- Revisions (341) are not imported.
- Dropped: wpforms-lite (unused), ai, analytics. Sitemap and Duplicate Post move into the core.

## Open issues
- ~~Go is not installed locally~~ → done in M0.
- Extism go-pdk with standard Go (wasip1, `go:wasmexport`) must be verified in M3. Fallback is TinyGo.
- ~~Check the Akismet API key~~ → `wordpress_api_key` is set.

## Next
- M0: install Go 1.27, create the skeleton, provide test data (dump + uploads) locally.
