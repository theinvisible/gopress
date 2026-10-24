# 2026-10-24 – M3 plugin host

## Done
- **Spike (step 0):** standard Go 1.27 builds plugins (`GOOS=wasip1 GOARCH=wasm -buildmode=c-shared`, `//go:wasmexport` also inside the PDK package) that run in Extism go-sdk 1.7.1 on wazero 1.12. TinyGo is not needed.
  - Example module: 4.4 MB (1.3 MB packed with `-s -w`).
  - Timings: compile about 1 s (cached on disk afterwards); instantiate 2.4 ms; call 0.2 ms.
  - Behavior: an endless loop is stopped by the context deadline, and a Go panic comes back as a trap error.
- **Packages** (`internal/plugins`):
  - `plugin.json`: strict validation (unknown keys refused) of identity, permissions, shortcodes, widgets, admin pages, editor buttons, dashboard boxes and form fields.
  - Zip checks: paths, links, duplicates and case collisions, sizes counted while reading, entry count, file types, WASM magic.
  - Store: bundled packages embedded from `plugins/bundled/zips`; uploaded ones in `data/plugins/<name>.zip`; the newest version wins.
- **Runtime:**
  - One compiled module per plugin (disk compilation cache in `data/cache/wasm`) and a pool of at most 4 instances.
  - Time per call: 1 s render, 5 s hooks, 15 s routes/admin, 2 min import. Memory: 64 MB. Output: at most 2 MB.
  - Crashes discard the instance. Five within a minute pause the plugin for five minutes, with the last error shown in the admin. Errors the plugin reports itself do not count.
  - stdout/stderr go to the log, line-buffered and rate-limited.
- **Host function** (one JSON import, `extism:host/user.gopress`):
  - KV storage (50 MB quota; the `_settings` collection is admin-only), settings, site info.
  - Published posts and media (no drafts, private or password-protected posts), comments.
  - HTTPS requests: approved hosts, public addresses only after DNS, redirects only to approved hosts.
  - Import stash (approved kinds only), cache invalidation.
- **Site:**
  - Plugin shortcodes in the render pipeline; widgets; `the_content` as the last filter; `head`/`footer` (only `<meta>`/`<link>` to the plugin's own assets).
  - Assets: added before `</head>`/`</body>` only on pages that use them, so pages without plugin output stay byte-identical (parity 162/162).
  - Plugin HTML goes through `render.SanitizePluginHTML`.
  - Routes `/plugins/<name>/…` and legacy query URLs: no cookies, header allowlist, CSP sandbox, no `Set-Cookie`/`Location`, rate limited.
  - Asset serving: approved CSS/JS only; images and fonts; SVG under a sandbox CSP.
  - Comment hooks: `comment_pre_save` (fails open after its time limit); `comment_status_changed` (asynchronous, from moderation in the content service).
- **Admin:**
  - Plugins screen: upload (re-auth), review with the permissions in words (new ones of an update highlighted), "Approve and Activate" bound to the package's SHA-256, deactivate, delete (data optional), re-run the import.
  - Plugin pages in the menu by capability:
    - settings forms validated against the manifest (media and sortable media list fields with the picker, write-only passwords);
    - tables with bulk actions;
    - custom pages in a sandboxed iframe with a postMessage bridge (`call`, `media.pick`, `notice`, `resize`, `navigate`, `insertShortcode`).
  - Dashboard boxes; plugin widget forms; editor buttons (form dialog or iframe dialog → escaped shortcode).
  - New `GET /admin/api/media/{id}`.
- **WordPress import:** `migrate_wordpress` runs once on first activation, in the background, with the plugin's `import:<kind>` data from the import stash.
- **PDK and tools:**
  - `pdk/go` (handlers and host calls).
  - `gopress plugin-pack DIR` (deterministic zip; `-wasm` for modules built elsewhere).
  - `plugins/hello` (example), `make hello`/`make plugins`, `docs/plugins.md`.
- **Tests:**
  - Manifest and zip attack matrix; store.
  - Runtime limits (loop, panic, 200 MB allocation, 3 MB output, breaker); every host function with and without permission; SSRF cases; KV quota.
  - Site integration (shortcode, widget, filter, assets, routes, legacy URL, deactivation); comment hooks.
  - Admin screens (re-auth, SHA-256 binding, capability matrix, settings validation, table escaping, bridge API with CSRF, iframe headers, update with more permissions).
  - E2E with Chrome: upload → approve → settings → editor button → public page with asset → board iframe (no cookies, no parent access, picker through the bridge) → route → deactivate.

## Decisions
- **Extism instead of a custom ABI:** plugins can be written in any language with an Extism PDK, and memory and call plumbing are maintained upstream. Extism's own HTTP and file access stay off; all access goes through one host function, which is easy to audit.
- **One host function with JSON ops** instead of one import per operation: one place for permission checks, limits and error handling. The PDK hides it.
- **Uploaded plugins are trusted for what they declare:** scripts on public pages run with the site's origin. They are therefore listed as a permission, and administrators approve them. Everything else (HTML, admin UI) is isolated or filtered.
- **Admin iframe pages without a session for their files:** a sandboxed document has an opaque origin, so its subresource requests carry no cookies and are cross-origin (CORP `cross-origin`). The files are public package contents; data only flows through the bridge, which runs in the admin with the session and CSRF token.
- **Shortcodes of inactive plugins stay text** (as in WordPress); the hidden stubs for the source site's two shortcodes stay until M4.
- **Unknown paths below `/plugins/<name>/`** go to the site's router like any URL.

## Security review
An independent review agent attacked the sandbox, permissions, SSRF, output filtering, routes and assets, the admin integration (iframe, bridge, dashboard), packages and concurrency. It found no sandbox escape and no script execution in the admin origin. Every finding below was reproduced and fixed with a regression test (`TestSanitizePluginURLs`, `TestReviewFindings`, `TestCheckModule`, `TestSanitizeHeadTags`, admin plugin tests).

| Finding | Severity | Fix |
|---|---|---|
| Dashboard boxes kept `data-*` attributes; the admin's comment script handled any `[data-moderate]` element, so a box could turn one admin click into a CSRF-valid POST such as deleting a post | Medium | Dashboard boxes use a strict admin policy (no class, id, style, `data-*`, forms); the comment script only accepts numeric ids and known actions |
| Waiting for a free instance counted as a failure, so load (e.g. uncached searches) paused a plugin and switched off its spam check | Medium/Low | Queueing has its own limit and does not count; the call timeout starts after an instance is free |
| Head hook `<link>` accepted any `/plugins/` path (another plugin's route as a stylesheet) | Low | Only the plugin's own approved assets, URL built by the host |
| `?msg=n:<text>` showed any text as a success notice on plugin pages | Low | Table action notices go through the session's flash store |
| `javascript:`/`data:` URLs survived in `poster`, `srcset`, input/source `src`; form action `/\host` passed | Low/Info | URL checks on these attributes; backslashes refused |
| Function tables are allocated outside the memory limit; the Extism kernel has its own memory | Low | Tables over 2^20 entries refused before compiling; documented 2 × 64 MB per instance |
| `posts_query` counted password protected posts in `total` (searchable content) | Info | Filtered in SQL |
| Iframe pages could open popups; `navigate` worked inside editor dialogs | Info | `allow-popups` removed; `navigate` only on admin pages; documented that a page can navigate itself away |
| Approval checked the reviewed SHA-256 before, not inside, the activation | Info | `Activate` takes the expected SHA-256 |
| Legacy query routes applied on every path, before core routes | Info | Only on the home URL |
| `kv_list`/`kv_count` accepted `_settings`; oversized output kept the instance; `invalidate` unlimited | Info | Consistent checks; instance discarded; at most once a second |

## Open issues
- Values with quotes in shortcode attributes arrive HTML-encoded, as in WordPress (the editor escapes `"`, `[` and `]`). This is harmless but can show up double-encoded if a plugin escapes again.

## Next
- M4: the three plugins of the source site on this host.
