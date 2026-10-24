# 2026-10-24 – M2b admin completion

## Done
- **Mail** (`internal/mail`):
  - Queued delivery over SMTP by a background worker: STARTTLS or TLS is required (plain text only to a relay on localhost), certificates are verified, and the password is only sent over TLS.
  - Plain-text templates; addresses, sender name and subject cannot carry header injections.
  - Retries with backoff; messages with one-time links expire with the link.
  - Settings › Email: the password is stored AES-GCM-encrypted with a key derived from `secret.key`, write-only, and needed again when the server changes; re-authentication required; test mail to your own address; queue view.
  - CLI `gopress mail queue|test`; `--mail-sink` writes `.eml` files for development and tests.
- **Notifications:**
  - Security notices from the audit log: password changed or reset, factors added or removed, recovery codes generated or used, 2FA reset by an admin, lockouts (once per day).
  - Comment moderation mails and comment notifications (`moderation_notify`, `comments_notify`).
  - Email changes are reported to the old address.
- **Lost password and invitations:**
  - One-time links: 256 bit, only the hash stored, single use, bound to the purpose and the current password hash, newest link only. Reset links are valid for 1 h, invitations for 72 h.
  - The token is moved from the URL into a cookie at once.
  - `/login/lost` gives the same answer for every input and does the work after the response. Limits: per network, one mail per 10 minutes and five per day per account.
  - A reset ends all sessions and keeps the second factor (and its lock).
  - An invitation leads straight to the 2FA setup and stands in for the setup code.
- **Users:**
  - List with role views, search, sort, 2FA status, last login and pending invitations; bulk role change and delete.
  - Add a user by invitation or with password plus setup code; logins and email addresses may not collide with each other.
  - Edit a user: profile, role, account security (reset 2FA, setup code, invitation, reset mail, end sessions, unlock).
  - Delete with reassignment of all content.
  - Every change needs a recent re-authentication; no self-demotion or self-deletion; at least one administrator remains.
- **Categories and tags:**
  - edit-tags style screens with quick edit, hierarchy, bulk delete and "make default".
  - Renamed slugs keep their archive URLs (301).
  - Deleting a category moves its posts to the default category and its children up.
  - Post list filter by tag.
- **Media** (`internal/media`, pure Go with `golang.org/x/image`):
  - Upload checks: type allowlist checked against the content; limits on pixels and decoded bytes; a shared memory budget; uploads per user and hour.
  - Metadata removed without re-encoding (EXIF/GPS, XMP, IPTC, comments, JFIF thumbnails, MPF trailers; ICC profiles kept); the EXIF orientation is applied.
  - `-scaled` copies above 2560 px; image sizes from the settings, plus 1536/2048 and the theme's post-thumbnail.
  - Media Library: grid/list, filters, drag-and-drop/paste uploads with progress, details dialog, bulk delete.
  - **Ctrl+V and drag and drop in the editor** (user request): the image is uploaded, attached to the post (a new post is saved as a draft first) and inserted at the cursor. Pasted HTML keeps remote images; the server fetches nothing.
  - Featured images rendered like `the_post_thumbnail`.
  - CLI `gopress media regenerate|strip-metadata`.
- **Settings** General, Writing, Reading, Discussion, Media, Permalinks:
  - Validated, stored as strings like WordPress.
  - Site title and tagline stored HTML-escaped (`sanitize_option`).
  - Time zones from an embedded IANA list (`time/tzdata` is in the binary).
  - A new admin email address is confirmed by link.
  - Closing comments on old posts is now applied.
  - Earlier permalink structures redirect to the new URLs.
- **Appearance:**
  - **Widgets:** three areas plus inactive widgets, drag and drop and keyboard, a form per type; new renderers custom HTML, pages and navigation menu. The Twenty Twelve front page template now works (featured image, front widget areas, body classes), and `full-width` is applied when the main sidebar is empty.
  - **Menus:** tables `menus`/`menu_items`, location `primary`, nesting by drag and drop, `wp_nav_menu` markup with current/ancestor classes, the page list as fallback. Custom URLs are limited to http(s), relative, `mailto:` and `tel:`.
  - **Customizer** with live preview: site identity, site icon, header text and color, header image, background, primary menu, homepage, additional CSS.
- **Site for logged-in users:**
  - Toolbar (`#wpadminbar`, can be switched off in the profile) and edit links.
  - Commenting under the account; "users must be logged in to comment".
  - Personal pages are never cached.
- **SQL injection guard** (user request): `internal/db/sqlguard_test.go` checks every SQL call in the module with `go/ast`. Query text may only come from literals, constants, placeholders and static helper results, and a negative test proves the guard catches unsafe queries. LIMIT/OFFSET are now parameters; hostile inputs are tested.
- **Tests:**
  - Unit and HTTP tests for every area.
  - `make e2e` covers 26 scenarios, including uploads (file input and Ctrl+V), invitation by mail with 2FA setup, lost password, settings, widgets, menus, customizer and the toolbar. The server writes mail into a sink that the test reads; the uploads are copied so the run never touches the source data.
  - All 26 pass.
  - `make roundtrip`: 85 documents, nothing lost.
  - Parity: 162/162.
  - `govulncheck`: no vulnerable code is called.

## Decisions
- **SMTP in the admin** (user decision) instead of environment variables. The password is encrypted, write-only and bound to the server it was entered for.
- **Photo metadata is always removed** (user decision), losslessly where possible.
- **Mail is plain text only** (no HTML part): smaller attack surface, like WordPress' default mails.
- **No "Plain" permalinks:** GoPress keeps pretty permalinks; `?p=`/`?page_id=` keep working.
- **Animated GIFs get no resized copies** (they would lose the animation).
- **WebP sizes are written as JPEG** (or PNG with transparency), because there is no pure-Go WebP encoder; WebP originals are not scaled down.
- **Deleting a user always reassigns their content** (WordPress can also delete it).
- **The customizer preview uses `Sec-Fetch-Dest: iframe`:**
  - The draft applies only in the same-origin preview frame of a logged-in user with `edit_theme_options`; normal page views never see it.
  - There is no toolbar inside the frame.
- **Logged-in commenting needs the session's CSRF token in the form** in addition to the same-origin check. Without both, the post counts as anonymous.

## Security review
Independent review agent (OWASP ASVS 5) over the whole M2b diff, with probe tests. It found no Critical or High issues. Every finding is fixed with a regression test.

| Finding | Severity | Fix |
|---|---|---|
| A small 16-bit PNG (7000×7000) decoded to about 1.1 GB per upload; two in parallel could get the server killed | Medium | Limit on decoded bytes (256 MB) in addition to pixels; weighted memory budget shared by all uploads; 200 uploads per user and hour |
| A password reset lifted the second-factor lock, which allowed about 6× faster TOTP guessing with password and mailbox | Low | A reset only lifts password locks |
| The stored SMTP password was sent to a changed SMTP host | Low | Changing host, port, security or user needs the password again |
| Other uploads were read fully into memory without a limit | Low | Sniffing reads the first 512 bytes; the file is moved into place |
| Regenerated or site icon sizes could overwrite another upload's file of the same name | Low | Size files are created exclusively; a name in use gets a suffix |
| Changing the own email address kept links sent to the old address valid | Low | Pending links are voided (as for admin changes) |
| Live reset or invitation tokens could stay in the mail queue | Low/Info | Mails with links expire with the link and are deleted |
| The time zone offset accepted NaN | Info | NaN/Inf refused |
| JFIF thumbnails, JFXX, foreign APP14 data and unknown WebP chunks were kept | Info | Dropped |
| Any administrator could confirm another admin's email change; reset mails could be sent to one account about 144 times a day | Info | Only the requesting admin confirms; at most 5 reset mails per account and day |

Accepted:
- The SMTP test shows the server's error message. Admins can use it to probe ports; it is admin-only and useful for setting up mail.
- The reset token is visible in reverse-proxy access logs for the first request. It is moved into a cookie right away, and the link expires after an hour.

The review checked these areas and found them sound:
- mail header and TLS handling
- token handling (entropy, hashing, binding, single use, enumeration, CSRF, fixation)
- user management authorization and re-authentication
- upload type, path and permission checks
- settings escaping and redirect handling
- widget, menu and customizer XSS and CSS handling
- draft isolation and cache isolation of personal pages
- logged-in commenting
- SQL parameterization
- term capabilities

## Open issues
- Plugin widgets have no settings form yet (M3 delivers the form schema).
- `npm audit` reports `basic-ftp` (high) in the e2e tooling (`puppeteer-core` → proxy agent). It is a dev-only tool that is never shipped and does not use FTP; fixing it needs a major puppeteer update.
- Images without all sizes from WordPress keep their old size files until `gopress media regenerate` runs.

## Next
- M2c: MCP interface for AI agents (to be planned).
