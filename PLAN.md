# GoPress – WordPress-Ersatz als Go Single-Binary für hadler.me

## Context

hadler.me („Bits and Bytes“) läuft auf `root@hadler.me` als Docker-Stack: `wp-app` (wordpress:7.1.3-php8.3-apache) + `wp-db` (mariadb:11.8) hinter Traefik.
Ziel: eine schnelle Single-Binary in Go, die sich im Admin wie WordPress bedient, Plugins als WASM lädt und die bestehende Seite ohne sichtbaren Bruch ersetzt (gleiche URLs, gleicher Look).

**Ist-Stand (live gelesen):**
- Theme **Twenty Twelve**. Theme-Mods: Hintergrund `#e6e6e6` mit Bild `uploads/2014/04/IMG_0485.jpg`, fixed/repeat, Header-Textfarbe `#515151`. Keine Nav-Menüs (Default-Seitenliste).
- Permalinks `/%year%/%monthnum%/%postname%/`, Locale en_US, Startseite = neueste Posts.
- Inhalt: 61 Posts, 24 Pages, 52 Attachments (41 MB Uploads), 39 Terms, 1 User. Kommentare: 679 freigegeben, 97 pending, 2144 Spam. 341 Revisionen (werden nicht importiert).
- 87 Posts nutzen Gutenberg-Block-Markup, der Rest ist klassisches HTML (braucht wpautop). Shortcodes im Einsatz: `[gallery ids=…]`, `[paypal-donation]`. Blöcke: `foto-slider-lightbox/gallery {"galleryId":791}`, core gallery/image/columns/group/quote/list.
- Sidebar-1-Widgets: search, block (leer), paypal_donations („Donate“), text („Partner Sites“), recent-posts, categories, tag_cloud, archives, meta.
- Plugins:
  - **paypal-donations**: Optionen `paypal_donations_options` (account rene@hadler.me, EUR, button large, new_tab …).
  - **foto-slider-lightbox** (eigenes Plugin, Swiper lokal): CPT `fslb_gallery` 791 mit Meta `_fslb_image_ids` und `_fslb_show_captions`.
  - Alt-CPT `donation_list`: 53 IPN-Einträge.
  - Ersetzt bzw. entfällt: akismet → eigenes Plugin; google-sitemap-generator, duplicate-post → Core; wpforms-lite (ungenutzt), ai, analytics → entfallen.

**Entscheidungen (mit dir abgestimmt):**
- Eigener schlanker Editor statt Gutenberg.
- SQLite embedded.
- WASM-Plugins via wazero/Extism.
- Umfang v1: Kommentare + Spamschutz, Twenty Twelve 1:1, SEO/Feeds, Donation-Log (IPN) und ein Akismet-Plugin.

## Architektur

- **Single Binary** `gopress` (CGO_ENABLED=0). Alles per `go:embed` eingebettet: Admin-UI, Editor-Bundle, Theme, Bundled-Plugins.
- **Laufzeitdaten** in `/data`: `gopress.db`, `uploads/`, `plugins/` (hochgeladene Plugins), `themes/` (optionale Overrides).
- **Stack:**
  - **Go 1.27**: `go.mod` mit `go 1.27`. Lokal ist noch kein Go installiert, deshalb wird zuerst das Go-1.27-Toolchain eingerichtet. Docker-Build-Stage `golang:1.27`.
  - Routing: stdlib `net/http` (ServeMux-Patterns).
  - Templates: `html/template`.
  - DB: `modernc.org/sqlite` (WAL).
  - Plugins: `github.com/extism/go-sdk` (wazero).
  - Sanitizing: `bluemonday`.
  - Bilder: `golang.org/x/image/draw`.
  - Kompression: `andybalholm/brotli`.
  - Nur für den Import: `go-sql-driver/mysql`.
  - Admin: htmx (embedded). Editor: TipTap (vanilla, esbuild).
- **Performance:**
  - Full-Page-Cache im RAM für anonyme GETs, vorkomprimiert mit br/gzip, ETag/304.
  - Invalidierung über einen globalen Generationszähler bei jedem Write.
  - Plugin-Calls laufen nur bei Cache-Miss.
  - Statische Assets mit Content-Hash und `immutable`. Uploads mit langen Cache-Headern.
  - Ziel: >10k req/s für gecachte Seiten.

### Projektstruktur (`/home/rene/DEV/gopress`)
```
cmd/gopress/            main: serve | import-wp | user | backup | plugin-pack
internal/db/            Schema, Migrations, Queries (posts, terms, comments, media, options, users, sessions, plugin_kv)
internal/content/       Posts/Pages/Terms/Comments/Revisions-Logik, Scheduling, Duplicate
internal/render/        Shortcode-Parser (WP-kompatibel), wpautop-Port, [gallery]/[caption] built-in, Filter-Pipeline
internal/media/         Upload, Resize (thumbnail 150, medium 300, medium_large 768, large 1024), srcset
internal/site/          Public-Handler, Permalink-Router, Feeds, Sitemap, robots.txt, Suche, Archive, Redirects, Page-Cache
internal/theme/         Theme-Loader, Widgets (search, text, recent posts, categories, tag cloud, archives, meta + Plugin-Widgets), Menüs
internal/admin/         wp-admin-artige UI (server-rendered + htmx)
internal/auth/          Sessions, CSRF, argon2id + Verify von WP-Hashes (phpass & WP-6.8-bcrypt "$wp$2y$…"), Rehash beim Login
internal/antispam/      Honeypot, HMAC-Zeit-Token, Rate-Limit pro IP, Link-Heuristik
internal/plugins/       Extism-Host, Manifest, Hook-Registry, Instance-Pool, Zip-Install, Host-Funktionen
internal/wpimport/      MariaDB → SQLite, Block→HTML-Konvertierung, Plugin-Migrations-Hooks
web/admin/  web/editor/ Admin-Templates/CSS/JS, TipTap-Quellcode → dist (embedded)
themes/twentytwelve/    Go-Template-Port + Original-CSS (GPL), Open Sans self-hosted
pdk/go/                 Plugin-SDK für Plugin-Autoren (wrappt extism go-pdk + JSON-ABI)
plugins/paypal-donations/  plugins/foto-slider-lightbox/  plugins/akismet/
Makefile                editor → plugins (.zip) → embed → binary; Dockerfile (FROM scratch)
```

## Komponenten im Detail

### 1. Datenmodell & Import (`internal/db`, `internal/wpimport`)

**Tabellen:**
- `posts` (id behalten, type post/page, status, slug, title, content, excerpt, date/gmt, modified, author, parent, menu_order, comment_status, password, featured_media)
- `post_meta`, `terms` + `term_taxonomy` (category/post_tag), `post_terms`
- `comments` (Threading, Status approved/pending/spam/trash, IP, UA)
- `media` (Datei, Größen-JSON, alt, caption, mime)
- `options` (KV), `users`, `sessions`, `revisions`
- `plugins` (Name, Version, aktiv)
- `plugin_kv` (Plugin, Collection, Key, JSON)

**Import:** `gopress import-wp --dsn … --uploads /data/wordpress/wp-content/uploads`
- **IDs bleiben gleich.** Damit funktionieren `?p=`, `?page_id=` und Shortcode-IDs weiter.
- **Block-Content:** Kommentare statischer Core-Blöcke werden entfernt, das HTML bleibt. Dynamische Plugin-Blöcke werden zu Shortcodes, z.B. `foto-slider-lightbox/gallery` → `[fotogalerie id="791"]`.
- **Klassischer Content:** wird einmalig durch den wpautop-Port in echtes `<p>`-HTML überführt. Das Original bleibt in `post_meta._wp_original_content`.
- **Uploads:** werden 1:1 nach `/data/uploads` kopiert, inkl. vorhandener Größen. Die Pfade `/wp-content/uploads/YYYY/MM/…` bleiben über eine Route erhalten.
- **Plugin-Daten:** Der Import ruft für jedes Bundled-Plugin dessen Export `migrate_wordpress` auf. Übergeben werden die zugehörigen CPT-Posts, Meta und Optionen (`fslb_gallery`, `donation_list`, `paypal_donations_options`, `widget_paypal_donations`, `wordpress_api_key`). So bleibt der Core generisch.
- **Widgets und Theme-Mods** aus `sidebars_widgets`/`widget_*` werden in die eigene Widget-Konfiguration übernommen.

### 2. Frontend (`internal/site`, `themes/twentytwelve`)

- **Twenty-Twelve-Port:**
  - Templates: index, single, page, archive (Kategorie/Tag/Datum/Autor), search, 404.
  - Comments-Bereich inkl. Formular, Sidebar, Customizer-Werte (Hintergrund, Header-Farbe).
  - Dazu eine Teilmenge von `wp-block-library` CSS für `wp-block-*`-Klassen (columns, group, image, gallery, quote).
- **Permalink-Engine** mit WP-Tokens. Außerdem:
  - Redirects für `?p=`, `?page_id=`, `?cat=`, `?s=`, alte Slugs, Trailing Slash.
  - Feeds: `/feed/`, `/comments/feed/`, `/category/x/feed/`, `/tag/x/feed/` (RSS2).
  - `/wp-sitemap.xml` und `/sitemap.xml` (Index + Posts/Pages/Terms), `robots.txt`, canonical, Pagination `/page/N/`.
- **Kommentare:** Threading, „awaiting moderation“ per Cookie, Gravatar.
- **Spam-Pipeline:**
  1. Built-in-Antispam
  2. Plugin-Filter `comment_pre_save` (Akismet)
  3. Moderationsregeln aus Settings → Discussion

### 3. Admin (`internal/admin`, `web/admin`)

- **Look:** wie wp-admin. Dunkles Seitenmenü, Admin-Bar, List-Tables mit Status-Filtern (All/Published/Draft/Trash), Suche, Bulk-Actions, Row-Actions (Edit/Quick Edit/Trash/View/**Duplicate**), Pagination.
- **Login:** `/admin/` und `/login`. Redirects von `/wp-admin/` und `/wp-login.php`, mit Rate-Limit.
- **Menüpunkte:**
  - Dashboard: At a Glance, Activity/Recent Comments, Quick Draft, Plugin-Dashboard-Widgets
  - Posts: All, Add New, Categories, Tags
  - Media: Grid/List, Drag&Drop-Upload, Details mit alt/caption
  - Pages
  - Comments: Approve/Unapprove/Spam/Trash/Reply, Badge-Zähler
  - Appearance: Widgets (Drag-Order pro Sidebar), Menus, Customize-Lite
  - Plugins: Liste, Activate/Deactivate, Upload `.zip`, Delete, Settings-Links, Plugin-Admin-Seiten
  - Users: Profil, Passwort, weitere User mit Rollen admin/editor/author
  - Settings: General, Writing, Reading, Discussion, Media, Permalinks
- **Editor-Screen** wie der klassische WP-Editor:
  - Titel, Permalink-Edit, Content-Editor.
  - Rechts die Boxen Publish (Status, Sichtbarkeit, Zeitplanung, Preview), Categories, Tags, Featured Image, Excerpt und Discussion.
  - Autosave, Revisionen mit Restore.

### 4. Schlanker Editor (`web/editor`, TipTap/ProseMirror)

- **Tabs „Visual“/„Text“** wie im WP Classic Editor. Text = HTML-Quelltext.
- **Toolbar:**
  - Absatz/H2–H4, Bold/Italic/Strike, Link, Listen, Quote, Code/Pre, Tabelle, Ausrichtung.
  - „Add Media“ mit dem Media-Library-Modal: Bild mit Größe, Ausrichtung, Caption und Link, sowie Galerie.
- **Shortcodes und Plugin-Inhalte** erscheinen als nicht-editierbare „Chips“ mit Edit-Popover. Das bestehende HTML (Columns/Group von Gutenberg) bleibt als Raw-HTML-Node erhalten.
- **Erweiterungs-API** `window.gopress.editor.registerInsert({id, label, icon, onClick})`. Darüber registrieren Plugins z.B. „Fotogalerie einfügen“. `window.gopress.media.pick({multiple})` steht auch für Plugin-Admin-Seiten bereit.

### 5. WASM-Plugin-System (`internal/plugins`, `pdk/go`)

**Paket:** `.zip` mit
- `plugin.json`: Name, Version, Autor, Beschreibung, Capabilities, erlaubte HTTP-Hosts, Shortcodes, Widgets, Admin-Seiten, Routen, Hooks, Assets, Editor-Scripts
- `plugin.wasm`
- `assets/`, `editor/`

Die Plugins sind in der Binary gebündelt, können aber wie in WP deaktiviert und durch Upload ersetzt werden.

**Guest-Exports** (alle JSON in/out):
- `render_shortcode`, `render_widget`, `filter` (Hook-Name im Input), `action`
- `admin_page` (gibt ein Formular-Schema oder HTML zurück), `admin_submit`
- `http_route`, `migrate_wordpress`

**Host-Funktionen:**
- `kv_get/set/delete/list` (namespaced pro Plugin), `option_get`
- `posts_query`, `media_get` (URLs, srcset, alt, caption)
- `http_request` (nur erlaubte Hosts), `log`, `site_info`
- `comment_get`, `comment_set_status`

**Hooks v1:**
- `the_content`, `comment_pre_save` (Ergebnis approve/hold/spam/discard)
- `comment_status_changed`, `dashboard_widgets`, `head`/`footer`

**Asset-Handling:** Ein Render-Ergebnis meldet die nötigen Assets. Die Seite bindet sie nur dann ein, wenn sie gebraucht werden (wie bisher beim Foto-Plugin).

**Runtime:**
- Kompilierte Module werden gecacht. Pro Plugin gibt es einen Instance-Pool für Parallelität.
- Pro Call gelten Timeout (Context) und Memory-Limit. Ein Panic deaktiviert nur das Plugin, nicht den Server.

**PDK:** `pdk/go` für Go-Plugins, gebaut mit Standard-Go 1.27 (`GOOS=wasip1 GOARCH=wasm -buildmode=c-shared`, Exports per `//go:wasmexport`). TinyGo ist nur der Fallback, falls die Binaries zu groß werden. `gopress plugin-pack` baut das Zip.

### 6. Erste Plugins (`plugins/…`)

**foto-slider-lightbox** (Port deines PHP-Plugins):
- **Galerien** in der Collection `galleries` (Titel, image_ids, show_captions). Import von `fslb_gallery` 791 mit gleicher ID.
- **Admin:** Seite „Foto Galerien“ mit Liste, Neu/Edit, Bildauswahl per Media-Picker, Drag-Reorder und Caption-Toggle.
- **Ausgabe:** Shortcode `[fotogalerie id=""]`. Das Render-Markup stammt 1:1 aus `includes/render.php`.
- **Assets:** `view.css`, `view.js` und Swiper werden unverändert übernommen.
- **Editor:** Insert-Button mit Galerie-Auswahl.

**paypal-donations:**
- **Settings-Seite** mit den Feldern aus `Admin.php`: Account, Currency, Page Style, Return Page, Amount, Purpose, Reference, Button small/large/custom, Button-URL, Localization, New Tab, Checkout Language, Return Method, Sandbox, Disable Stats und IPN-Validierung.
- **Ausgabe:** Shortcode `[paypal-donation purpose reference amount]` mit identischem Form-Markup wie `views/paypal-button.php`, außerdem das Widget „Donate“.
- **IPN:**
  - Route `/plugins/paypal-donations/ipn`, die alte URL `/?ppd_paypal_ipn=process` wird gemappt.
  - Verifikation per Postback an `ipnpb.paypal.com` (http-Allowlist). Danach wird die Spende gespeichert.
  - Admin-Liste „Donations“ mit Datum, Name, E-Mail, Betrag, Währung, Memo und Status. Import der 53 `donation_list`-Einträge.

**akismet:**
- **Settings:** API-Key (Import aus `wordpress_api_key`), „Verify key“ über `/1.1/verify-key`, Option „offensichtlichen Spam verwerfen“.
- **Hooks:**
  - `comment_pre_save` → `comment-check`.
  - `comment_status_changed` → `submit-spam`/`submit-ham`.
- **Dashboard-Widget** mit Zählern.

## Meilensteine & Checklisten

Legende: `[ ]` offen · `[x]` erledigt · `[~]` in Arbeit

### M0 – Setup
- [x] PLAN.md mit Devlog anlegen
- [x] `git init`
- [ ] Go-1.27-Toolchain lokal installieren
- [ ] `go mod init` (`go 1.27`), Grundgerüst `cmd/gopress`, Makefile, `.gitignore`
- [ ] Lokale MariaDB mit `/root/wp-dump-final.sql.gz` und eine Kopie der Uploads als Testdaten

### M1 – Core & Import (*Ergebnis: importierte Seite läuft lokal read-only*)
- [ ] SQLite-Schema und Migrations (`internal/db`)
- [ ] `import-wp`: Posts/Pages/Terms/Comments/Users/Options/Media mit gleichen IDs
- [ ] Block→HTML-Konvertierung, Plugin-Blöcke werden zu Shortcodes
- [ ] wpautop-Port mit Golden-Tests gegen PHP
- [ ] Shortcode-Parser (WP-kompatibel), built-in `[gallery]` und `[caption]`
- [ ] Uploads kopieren, Route `/wp-content/uploads/…`
- [ ] Twenty-Twelve-Port: index/single/page/archive/search/404, Sidebar-Widgets, Customizer-Werte
- [ ] Kommentare anzeigen (Threading) und Formular inkl. Antispam
- [ ] Permalink-Router und Redirects (`?p=`, `?page_id=`, Trailing Slash, alte Slugs)
- [ ] RSS-Feeds, Sitemap, robots.txt, canonical
- [ ] Page-Cache (br/gzip, ETag), Lasttest

### M2 – Admin
- [ ] Auth: Login, Sessions, CSRF, Rate-Limit, WP-Hash-Verify + Rehash
- [ ] Admin-Layout (Menü, Admin-Bar), Dashboard
- [ ] Posts/Pages: List-Table, Filter, Bulk/Row-Actions, Duplicate, Quick Edit
- [ ] Editor-Screen inkl. Seitenboxen, Autosave, Revisionen
- [ ] TipTap-Editor (Visual/Text, Media-Modal, Shortcode-Chips, Erweiterungs-API)
- [ ] Media-Library mit Upload und Resizes/srcset
- [ ] Kommentar-Moderation
- [ ] Categories/Tags
- [ ] Appearance: Widgets, Menus, Customize-Lite
- [ ] Users, Settings (General/Writing/Reading/Discussion/Media/Permalinks)

### M3 – Plugin-Host
- [ ] Manifest-Format `plugin.json` und Zip-Install/Delete
- [ ] Extism-Host: Module-Cache, Instance-Pool, Timeouts, Memory-Limits, Panic-Isolation
- [ ] Host-Funktionen (kv, option, posts, media, http-Allowlist, log, comments)
- [ ] Hook-Registry (`the_content`, `comment_pre_save`, `comment_status_changed`, `dashboard_widgets`, `head`/`footer`)
- [ ] Shortcodes, Widgets, Routen, Admin-Seiten (Form-Schema), Assets bei Bedarf, Editor-Scripts
- [ ] `pdk/go`, `gopress plugin-pack`, Beispiel-Plugin
- [ ] Plugins-Admin: Liste, Aktivieren/Deaktivieren, Upload

### M4 – Plugins
- [ ] foto-slider-lightbox: Galerien-Admin, Shortcode, Assets, Editor-Button, Import `fslb_gallery`
- [ ] paypal-donations: Settings, Shortcode, Widget, IPN + Verifikation, Donations-Liste, Import
- [ ] akismet: Settings + Verify-Key, comment-check, submit-spam/ham, Dashboard-Widget

### M5 – Deployment
- [ ] Dockerfile (`golang:1.27` Build → `FROM scratch`), Volume `/data`
- [ ] `gopress backup` (`VACUUM INTO` + tar der Uploads)
- [ ] Staging `new.hadler.me` hinter Traefik
- [ ] URL-Paritäts-Crawl und visueller Vergleich mit Live
- [ ] Cutover: Router umschalten, WP-Container stoppen (nicht löschen)

## Verifikation

- **Unit-/Golden-Tests:**
  - Shortcode-Parser gegen WP-Regex-Fälle.
  - wpautop gegen PHP-Ausgaben aus dem Container (Golden-Files).
  - Permalink-Routing, Block→HTML-Konvertierung.
  - Passwort-Verify mit echten Hash-Formaten (phpass, `$wp$2y$`).
- **Import-Test:** `/root/wp-dump-final.sql.gz` in einen lokalen MariaDB-Container laden, `import-wp` laufen lassen und Zählungen prüfen (61/24/52/679…).
- **URL-Parität:** alle URLs aus der Live-Sitemap von hadler.me gegen lokal/Staging crawlen. Erwartet: 200 bzw. passender 301 und gleicher `<title>`. Dazu ein visueller Vergleich per Screenshot (Startseite, Post, Page mit Galerie, Donate-Seite).
- **Plugins:**
  - Galerie: Slider/Lightbox/Keyboard manuell testen.
  - PayPal: IPN gegen den PayPal-Sandbox-Simulator.
  - Akismet: mit Testautor `viagra-test-123` bzw. `akismet-guaranteed-spam@example.com`.
  - Plugin-Zip deaktivieren, erneut hochladen, ein Plugin mit Panic → Server läuft weiter.
- **Last:** `oha`/`hey` gegen Startseite und Post (gecacht/ungecacht). Ziel >10k req/s gecacht.
- **Staging-Abnahme auf new.hadler.me** vor dem Cutover.

---

## Devlog

Neueste Einträge oben. Jeder Eintrag hat dieselbe Gliederung: Gemacht, Entscheidungen, Offen/Probleme, Nächster Schritt.

### 2026-10-24 – Analyse & Planung

**Gemacht**
- Live-Stack auf `root@hadler.me` nur lesend analysiert:
  - Container `wp-app` (wordpress:7.1.3-php8.3-apache) und `wp-db` (mariadb:11.8) hinter Traefik v3.7.
  - Daten: wp-content unter `/data/wordpress/wp-content`, DB im Volume `wordpress_db_mariadb118`.
- Content-Bestand erhoben (siehe Context):
  - 61 Posts, 24 Pages, 52 Attachments, 41 MB Uploads.
  - 679/97/2144 Kommentare (approved/pending/spam).
  - 87 Posts mit Block-Markup.
- Plugin-Nutzung ermittelt:
  - `[paypal-donation]` auf der Donate-Seite (ID 187) und als Widget in sidebar-1.
  - `foto-slider-lightbox/gallery` (Galerie 791) auf der tiBackup-Seite (ID 149).
  - `donation_list` hat 53 Alt-Einträge.
- PLAN.md angelegt, Repo initialisiert.

**Entscheidungen**
- **Go 1.27**, Single Binary, CGO-frei.
- **SQLite embedded** (modernc.org/sqlite). Grund: echtes Single-Binary-Deployment, `wp-db` entfällt.
- **Eigener schlanker Editor** (TipTap) statt Gutenberg. Grund: leichter; Block-Content wird beim Import zu HTML.
- **WASM-Plugins** über Extism/wazero. Grund: pure Go, sandboxed, Upload zur Laufzeit wie bei WP.
- **v1-Umfang:** Kommentare + Spamschutz, Twenty Twelve 1:1, SEO/Feeds, Donation-Log (IPN), Akismet-Plugin.
- Gleiche Post-IDs und Upload-Pfade wie in WP. Grund: Links, Shortcode-IDs und SEO bleiben stabil.
- Revisionen (341) werden nicht importiert.
- Entfallen: wpforms-lite (ungenutzt), ai, analytics. Sitemap und Duplicate Post wandern in den Core.

**Offen/Probleme**
- Go ist lokal nicht installiert, Go 1.27 muss eingerichtet werden.
- Extism go-pdk mit Standard-Go (wasip1, `go:wasmexport`) muss in M3 verifiziert werden. Fallback ist TinyGo.
- Akismet-API-Key: prüfen, ob `wordpress_api_key` in der WP-DB gesetzt ist.

**Nächster Schritt**
- M0 abschließen: Go 1.27 installieren, Grundgerüst anlegen, Testdaten (Dump + Uploads) lokal bereitstellen.
