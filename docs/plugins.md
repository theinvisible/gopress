# Writing plugins

GoPress plugins are WebAssembly modules. They run in a sandbox: no file system, no environment variables and no network of their own. Everything a plugin does goes through the host. An administrator sees what a plugin asks for before activating it.

The example plugin `plugins/hello` shows every part. This page is the reference.

## Package

A plugin is a `.zip` with:

| Path | Content |
|---|---|
| `plugin.json` | The manifest (below). |
| `plugin.wasm` | The compiled module. |
| `assets/` | Files for public pages: CSS, JS, images, fonts, JSON. |
| `admin/` | Files of custom admin pages: HTML, CSS, JS, images. |
| `templates/` | HTML templates for the plugin's output (`*.html`, at most 64 KB each, no folders). |
| `README.md`, `LICENSE`, `CHANGELOG.md` | Optional documentation. |

Nothing else is accepted.

Limits:
- 20 MB packed and 64 MB unpacked;
- 500 files;
- no links or path tricks.

Build and pack a Go plugin with:

```sh
gopress plugin-pack plugins/hello          # writes hello-1.0.0.zip
gopress plugin-pack -wasm x.wasm DIR       # pack a module built some other way
```

Plugins written in Go use the standard toolchain:

```sh
GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared
```

`plugin-pack` runs this for you. Other languages work through their Extism PDKs, because the protocol is plain JSON.

## Manifest (`plugin.json`)

The manifest is validated strictly: unknown keys are errors.

```json
{
  "name": "hello", "version": "1.0.0", "title": "Hello", "api": 1,
  "description": "…", "author": "…", "url": "https://…",
  "permissions": {
    "http": ["api.example.org"],
    "routes": ["/stats", "/api/*"],
    "legacy_routes": [{"query": {"old_param": "value"}, "route": "/stats"}],
    "hooks": ["the_content", "head", "footer", "comment_pre_save", "comment_status_changed"],
    "data": ["posts:read", "media:read", "comments:read", "comments:moderate", "import:post:my_type",
             "import:option:my_option", "mail:admins", "notify:admins"],
    "frontend_assets": ["assets/hello.css", "assets/hello.js"]
  },
  "shortcodes": [{"name": "hello", "assets": ["assets/hello.css"]}],
  "widgets": [{"type": "hello", "title": "Hello", "fields": [{"name": "title", "label": "Title"}]}],
  "admin": [
    {"id": "settings", "title": "Hello", "menu": "settings", "capability": "manage_options", "kind": "settings", "fields": […]},
    {"id": "list", "title": "Items", "menu": "plugin", "capability": "edit_posts", "kind": "table", "columns": […]},
    {"id": "board", "title": "Board", "menu": "plugin", "capability": "edit_posts", "kind": "iframe", "file": "admin/board.html"}
  ],
  "editor": [{"id": "greet", "label": "Greeting", "icon": "smiley", "shortcode": "hello", "fields": […]}],
  "dashboard": [{"id": "summary", "title": "Hello", "capability": "edit_posts"}]
}
```

### Permissions

These are what the approval screen lists:
- **`http`:** host names the plugin may send HTTPS requests to. Only public addresses are allowed (checked after DNS resolution), and redirects only to listed hosts.
- **`routes`:** paths below `/plugins/<name>/`.
  - Requests reach the plugin without cookies, rate-limited (60 per minute per network).
  - Answers can set `Content-Type` and `Content-Disposition` only, and always carry `Content-Security-Policy: sandbox`.
- **`legacy_routes`:** query parameters of old WordPress URLs on the home URL (e.g. `/?my_ipn=process`) mapped to a route. Core parameters (`p`, `s`, `feed` …) are not allowed.
- **`hooks`:** see below.
- **`data`:**
  - `posts:read`: published posts and pages.
  - `media:read`: attachments of published posts.
  - `comments:read` and `comments:moderate`.
  - `import:<kind>`: imported WordPress data of that kind (e.g. `post:my_type`).
  - `import:option:<name>`: one imported WordPress option. Options are granted one by one, because the import holds the options of all plugins of the source site, API keys included.
  - `mail:admins`: send plain-text mail to the administrators (`MailAdmins`).
  - `notify:admins`: show live notices in the admin (`NotifyAdmins`).
- **`frontend_assets`:** CSS and JavaScript files added to public pages, and only to pages whose content uses them. Scripts on the site are trusted code; this is the only way a plugin can run JavaScript there.

An update that asks for more permissions stays inactive until an administrator approves it again.

A spam filter can set `"comment_check_failure": "hold"` at the top level of the manifest. Then a comment whose check fails, times out or meets a paused plugin is held for moderation instead of keeping the core's decision.

### Form fields

Settings pages, widget forms and editor dialogs share one field format:
- **Keys:** `name`, `label`, `type`, `help`, `default`, `required`, `min`, `max`, `max_length`, `options` (`[{value, label}]`).
- **Types:** `text` (default), `textarea`, `number`, `checkbox`, `select`, `url`, `email`, `password`, `color`, `media` (one attachment id), `media_list` (sortable ids).
- **Storage:** the admin validates the values and stores them; the plugin reads them with `Settings(page)`.
- **Passwords:** password fields are never sent back to the browser.

### Admin pages

- **`settings`:** a form from `fields`.
- **`table`:** a list from the plugin (`HandleTable`) with bulk actions (`HandleSubmit`). Cells are plain text.
- **`iframe`:** your own HTML page from `admin/`.
  - **Isolation:** it runs in a sandboxed iframe with an opaque origin, so it has no cookies and no access to the admin. The CSP allows scripts only from `/plugin-ui/`, and the page has no network access of its own.
  - **Bridge:** include `/plugin-ui/_frame.js`; `window.gopress` then offers:

    ```js
    gopress.call(method, params)        // → the plugin's HandleAPI handler (Promise)
    gopress.media.pick({multiple, imagesOnly})   // → [{id, url, alt, title, sizes, …}]
    gopress.notice(text, kind)          // admin notice (success, error, warning, info)
    gopress.resize(height)              // automatic with ResizeObserver
    gopress.navigate(pageID)            // another page of the plugin (not in editor dialogs)
    gopress.insertShortcode(tag, attrs, content)  // in editor dialogs
    gopress.close()                     // close an editor dialog
    gopress.on("event", fn)             // live events the plugin publishes for this page (LivePublish)
    ```

- **Trust:** the iframe page can still navigate itself to another site, and data the bridge hands it could leave that way. Popups are not allowed.
- **Menu placement:** `menu` places the page under Settings, Tools, Posts or Comments, or as the plugin's own entry (`plugin`). `none` keeps an iframe page out of the menu; editor dialogs use such pages. Its capability is still checked.
- **Checking settings:** after a settings page was saved, `HandleSettingsSaved(page, fn)` runs. It can check the new values (for example an API key against its service). Its result is shown as a notice; a returned error is shown as a problem, and the values stay saved.
- **Capability:** `capability` is one of `manage_options`, `edit_posts`, `edit_others_posts`, `moderate_comments`, `upload_files`.

### Editor buttons

An editor button inserts one of the plugin's shortcodes. Its dialog is a form (`fields`) or an iframe page (`page`). The editor builds the shortcode from the tag and attributes and escapes them; plugins never insert HTML into posts.

## Go API (`pdk/go`)

Register handlers in `init()`. Plugins are loaded as WASI reactors: package initialization runs, `main` does not.

```go
import gp "github.com/theinvisible/gopress/pdk/go"

func init() {
	gp.HandleShortcode("hello", func(r gp.ShortcodeRequest) (gp.Render, error) {
		return gp.Render{HTML: "<p>Hello, " + html.EscapeString(r.Attr("name", "world")) + "</p>",
			Assets: []string{"assets/hello.css"}}, nil
	})
}
```

### Handlers

| Handler | Called for |
|---|---|
| `HandleShortcode(name, fn)` | A shortcode; returns HTML and assets. |
| `HandleWidget(type, fn)` | A widget; `TitleHTML` is the title in the theme's markup. |
| `HandleFilter(hook, fn)` | `the_content`, `head` (only `<meta>` and `<link>` to own assets pass), `footer`. |
| `HandleComment(fn)` | `comment_pre_save`: `Keep`, `Approve`, `Hold`, `Spam` or `Discard`. The input has the comment, the post's URL, title, modified time and terms, the commenter's role, the referrer and the `Accept`, `Accept-Charset` and `Accept-Encoding` headers. |
| `HandleAction(name, fn)` | `activate`, `deactivate`, `comment_status_changed` (`CommentStatusChange`: the comment, the action, the status before and the editor). |
| `HandleRoute(path, fn)` | Requests to a route. |
| `HandleTable(page, fn)`, `HandleSubmit(page, fn)` | Table pages. |
| `HandleAPI(page, fn)` | Bridge calls of an iframe page. |
| `HandleDashboard(id, fn)` | A dashboard box. |
| `HandleSettingsSaved(page, fn)` | After a settings page was saved. |
| `HandleMigrate(fn)` | The WordPress import; it runs once when the plugin is first activated, and administrators can run it again. |

Dashboard boxes are shown inside the admin, so a stricter filter applies to them: text, links, lists, tables and images only (no `class`, `id`, `style`, `data-*` or form elements).

All other HTML from plugins is filtered:
- allowed: formatting, links, images, media, forms (to https or same-site targets), `data-*` and `aria-*` attributes;
- removed: scripts, event handlers, iframes and `javascript:` URLs.

### Templates

Plugins can render their HTML from templates in Go's `html/template` syntax instead of building strings. Values are escaped by context, so text, attributes and URLs are safe, and `javascript:` URLs become `#ZgotmplZ`.

```go
out, err := gp.Template("gallery.html", map[string]any{"title": g.Title, "images": imgs})
return gp.Render{HTML: out}, err
```

- **Location:** templates are files in `templates/`. They can call each other by file name: `{{template "item.html" .}}`.
- **Validation:** templates are checked when the package is uploaded.
- **Sandbox:** they run inside the plugin's sandbox with its time and memory limits.
- **Theme overrides:** a theme replaces a file with its own copy at `plugins/<plugin>/<file>` (e.g. `themes/twentytwelve/plugins/foto-slider-lightbox/gallery.html`), as WordPress themes override plugin templates. The plugin always gets the active theme's version.
- **Filtering:** the result is filtered like any plugin HTML.

### Host functions

| Function | Notes |
|---|---|
| `Log(level, msg)` | Server log, rate-limited. |
| `Site()` | Name, tagline, URL, language, time zone. |
| `KVGet`, `KVSet`, `KVDelete`, `KVList`, `KVCount` | The plugin's own storage: JSON values up to 1 MB, 50 MB in total. |
| `Settings(page)` | Values of a settings page. |
| `Posts`, `GetPost` | Published content (`posts:read`). |
| `GetMedia(ids…)` | Attachment details (`media:read`). |
| `GetComment`, `SetCommentStatus` | `comments:read` / `comments:moderate`. |
| `Fetch(HTTPRequest)` | HTTPS to `permissions.http` hosts; 10 s (or `TimeoutMS`, 100–10000), 1 MB request, 2 MB response. |
| `Stash(kind)` | Imported WordPress data (`import:<kind>`); `Stash("option")` returns the options granted with `import:option:<name>`. |
| `NotifyAdmins(text, kind, capability)` | A live notice in the admin for users with the capability (`notify:admins`); plain text, at most 300 characters and 30 a minute. |
| `LivePublish(page, data)` | JSON (at most 16 KB, 120 a minute) to everyone who has the plugin's iframe page open (`gopress.on("event", fn)`). |
| `Progress(percent, text)` | Progress of the WordPress import, shown on the Plugins screen (only in `HandleMigrate`). |
| `MailAdmins(subject, body)` | Plain text to the administrators (`mail:admins`). The host picks the recipients and puts the site and plugin name into the subject; at most 20 an hour. |
| `Invalidate()` | Clear the page cache after changing what the plugin renders. |
| `Template(name, data)` | Render `templates/<name>` (see Templates). |

## Runtime limits

- **Time per call:**
  - 1 s for rendering;
  - 5 s for hooks and dashboard boxes;
  - 15 s for routes and admin calls;
  - 2 min for the import.
- **Memory:** 64 MB for the plugin's module and 64 MB for the Extism kernel of each instance. Function tables can have at most 2^20 entries (checked before loading). Each plugin has at most 4 instances, so calls run in parallel. Calls that find all instances busy wait up to their time limit, then fail without counting as errors.
- **Crashes:** a crash, timeout or oversized answer (more than 2 MB) discards the instance.
- **Cache:** `Invalidate()` takes effect at most once a second per plugin. Five of them within a minute pause the plugin for five minutes: its output is empty, its hooks are skipped and comments keep the core's decision. The admin shows the last error.
- **Errors the plugin reports** (`return err`) do not count.

## Testing plugins

Outside WebAssembly the PDK runs against a fake host, so plugin code can be tested with `go test`:

```go
func TestShortcode(t *testing.T) {
	gp.SetTestHost(func(op string, args json.RawMessage) (any, error) {
		switch op {
		case "kv_get":
			return map[string]any{"found": false}, nil
		}
		return nil, errors.New("unexpected " + op)
	})
	var out gp.Render
	if err := gp.CallExport("render_shortcode", gp.ShortcodeRequest{Tag: "hello"}, &out); err != nil {
		t.Fatal(err)
	}
}
```
