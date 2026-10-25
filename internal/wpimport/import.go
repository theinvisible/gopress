// Package wpimport imports a WordPress site from its MySQL/MariaDB database
// and uploads directory into a fresh GoPress database.
package wpimport

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/theinvisible/gopress/internal/db"
	"github.com/theinvisible/gopress/internal/phpser"
)

// Options configures an import.
type Options struct {
	DSN        string // go-sql-driver/mysql DSN of the WordPress database
	Prefix     string // table prefix, e.g. "wp_"
	UploadsDir string // wp-content/uploads of the source site ("" = skip files)
	DataDir    string // GoPress data directory
	Force      bool   // replace an existing GoPress database
	Log        *slog.Logger
}

// Report summarizes an import.
type Report struct {
	Counts   map[string]int
	Warnings []string
}

func (r *Report) add(key string, n int) { r.Counts[key] += n }

func (r *Report) warn(format string, args ...any) {
	r.Warnings = append(r.Warnings, fmt.Sprintf(format, args...))
}

// importedOptions are copied verbatim (decoded) from wp_options.
var importedOptions = []string{
	"blogname", "blogdescription", "home", "admin_email", "blog_public", "WPLANG",
	"date_format", "time_format", "timezone_string", "gmt_offset", "start_of_week",
	"posts_per_page", "posts_per_rss", "rss_use_excerpt", "show_on_front", "page_on_front", "page_for_posts",
	"sticky_posts", "default_category", "use_smilies", "permalink_structure", "category_base", "tag_base",
	"default_comment_status", "default_ping_status", "require_name_email", "comment_registration",
	"comment_moderation", "comment_previously_approved", "comment_max_links", "moderation_keys", "disallowed_keys",
	"close_comments_for_old_posts", "close_comments_days_old", "thread_comments", "thread_comments_depth",
	"page_comments", "comments_per_page", "default_comments_page", "comment_order", "show_comments_cookies_opt_in",
	"comments_notify", "moderation_notify",
	"show_avatars", "avatar_rating", "avatar_default", "users_can_register", "default_role",
	"thumbnail_size_w", "thumbnail_size_h", "thumbnail_crop", "medium_size_w", "medium_size_h",
	"medium_large_size_w", "medium_large_size_h", "large_size_w", "large_size_h", "uploads_use_yearmonth_folders",
	"image_default_link_type", "image_default_size", "image_default_align", "wp_attachment_pages_enabled",
	"wp_page_for_privacy_policy", "site_icon",
}

// pluginOptionPrefixes select options owned by plugins; they are stashed for
// the plugins to migrate.
var pluginOptionPrefixes = []string{"paypal_donation", "widget_paypal_donations", "wordpress_api_key", "akismet_", "fslb_"}

// pluginPostTypes are custom post types owned by plugins; stashed with meta.
var pluginPostTypes = []string{"fslb_gallery", "donation_list"}

// skippedMeta are meta keys that are mapped to columns or are WP internals.
var skippedMeta = map[string]bool{
	"_edit_lock": true, "_edit_last": true, "_wp_attached_file": true, "_wp_attachment_metadata": true,
	"_wp_old_slug": true, "_thumbnail_id": true, "_wp_page_template": true, "_wp_attachment_image_alt": true,
	"_dp_original": true, "_encloseme": true, "_pingme": true, "_wp_trash_meta_status": true, "_wp_trash_meta_time": true,
}

type importer struct {
	opts   Options
	src    *sql.DB
	tx     *sql.Tx
	rep    *Report
	home   string
	postID map[int64]string // imported post ids -> type
}

// Run imports the WordPress site described by opts.
func Run(ctx context.Context, opts Options) (*Report, error) {
	if opts.Log == nil {
		opts.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if opts.Prefix == "" {
		opts.Prefix = "wp_"
	}
	if !regexp.MustCompile(`^[A-Za-z0-9_]+$`).MatchString(opts.Prefix) {
		return nil, fmt.Errorf("invalid table prefix %q", opts.Prefix)
	}
	dbPath := filepath.Join(opts.DataDir, "gopress.db")
	if _, err := os.Stat(dbPath); err == nil {
		if !opts.Force {
			return nil, fmt.Errorf("%s exists (use --force to replace it)", dbPath)
		}
		for _, suffix := range []string{"", "-wal", "-shm"} {
			if err := os.Remove(dbPath + suffix); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return nil, err
			}
		}
	}
	if err := os.MkdirAll(opts.DataDir, 0o750); err != nil {
		return nil, err
	}

	src, err := sql.Open("mysql", opts.DSN)
	if err != nil {
		return nil, err
	}
	defer src.Close()
	if err := src.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("connect to WordPress database: %w", err)
	}

	dst, err := db.Open(ctx, dbPath)
	if err != nil {
		return nil, err
	}
	defer dst.Close()

	im := &importer{opts: opts, src: src, rep: &Report{Counts: map[string]int{}}, postID: map[int64]string{}}
	steps := []struct {
		name string
		fn   func(context.Context) error
	}{
		{"options", im.importOptions},
		{"users", im.importUsers},
		{"terms", im.importTerms},
		{"posts", im.importPosts},
		{"post meta", im.importPostMeta},
		{"term relationships", im.importTermRelationships},
		{"comments", im.importComments},
		{"widgets", im.importWidgets},
		{"plugin data", im.stashPluginData},
	}
	err = dst.Tx(ctx, func(tx *sql.Tx) error {
		im.tx = tx
		for _, s := range steps {
			opts.Log.Info("importing", "step", s.name)
			if err := s.fn(ctx); err != nil {
				return fmt.Errorf("%s: %w", s.name, err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	if opts.UploadsDir != "" {
		opts.Log.Info("copying uploads", "from", opts.UploadsDir)
		n, err := copyTree(opts.UploadsDir, filepath.Join(opts.DataDir, "uploads"))
		if err != nil {
			return nil, fmt.Errorf("uploads: %w", err)
		}
		im.rep.add("upload files", n)
	}
	return im.rep, nil
}

func (im *importer) table(name string) string { return "`" + im.opts.Prefix + name + "`" }

func (im *importer) importOptions(ctx context.Context) error {
	rows, err := im.src.QueryContext(ctx, `SELECT option_name, option_value FROM `+im.table("options"))
	if err != nil {
		return err
	}
	defer rows.Close()
	wanted := map[string]bool{}
	for _, o := range importedOptions {
		wanted[o] = true
	}
	template := ""
	opts := map[string]string{}
	for rows.Next() {
		var name, value string
		if err := rows.Scan(&name, &value); err != nil {
			return err
		}
		opts[name] = value
		if name == "template" {
			template = value
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for name, value := range opts {
		switch {
		case wanted[name]:
			if err := db.SetOption(ctx, im.tx, name, phpser.Maybe(value)); err != nil {
				return err
			}
			im.rep.add("options", 1)
		case name == "theme_mods_"+template:
			mods := phpser.Maybe(value)
			if m, ok := mods.(map[string]any); ok {
				for _, k := range []string{"background_image", "header_image"} {
					if u, ok := m[k].(string); ok {
						m[k] = relativeURL(u, opts["home"])
					}
				}
			}
			if err := db.SetOption(ctx, im.tx, "theme_mods", mods); err != nil {
				return err
			}
		case hasAnyPrefix(name, pluginOptionPrefixes):
			if err := db.Stash(ctx, im.tx, "wordpress", "option", name, phpser.Maybe(value)); err != nil {
				return err
			}
			im.rep.add("stashed options", 1)
		}
	}
	if err := db.SetOption(ctx, im.tx, "theme", template); err != nil {
		return err
	}
	im.home = opts["home"]
	return nil
}

func (im *importer) importUsers(ctx context.Context) error {
	meta := map[int64]map[string]string{}
	mrows, err := im.src.QueryContext(ctx, `SELECT user_id, meta_key, meta_value FROM `+im.table("usermeta")+
		` WHERE meta_key IN (?, 'description')`, im.opts.Prefix+"capabilities")
	if err != nil {
		return err
	}
	for mrows.Next() {
		var id int64
		var k, v string
		if err := mrows.Scan(&id, &k, &v); err != nil {
			mrows.Close()
			return err
		}
		if meta[id] == nil {
			meta[id] = map[string]string{}
		}
		meta[id][k] = v
	}
	mrows.Close()

	rows, err := im.src.QueryContext(ctx, `SELECT ID, user_login, user_nicename, user_email, user_url, display_name, user_pass, user_registered FROM `+im.table("users"))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var u db.User
		var registered string
		if err := rows.Scan(&u.ID, &u.Login, &u.Nicename, &u.Email, &u.URL, &u.DisplayName, &u.PassHash, &registered); err != nil {
			return err
		}
		u.RegisteredGMT = parseTime(registered)
		u.Description = meta[u.ID]["description"]
		u.Role = "subscriber"
		if caps, ok := phpser.Maybe(meta[u.ID][im.opts.Prefix+"capabilities"]).(map[string]any); ok {
			for _, role := range []string{"administrator", "editor", "author", "contributor"} {
				if caps[role] == true {
					u.Role = role
					break
				}
			}
		}
		if err := db.InsertUser(ctx, im.tx, &u); err != nil {
			return err
		}
		im.rep.add("users", 1)
	}
	return rows.Err()
}

func (im *importer) importTerms(ctx context.Context) error {
	rows, err := im.src.QueryContext(ctx, `SELECT t.term_id, tt.taxonomy, t.slug, t.name, tt.description, tt.parent, tt.count
		FROM `+im.table("terms")+` t JOIN `+im.table("term_taxonomy")+` tt ON tt.term_id = t.term_id
		WHERE tt.taxonomy IN ('category', 'post_tag') ORDER BY t.term_id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var t db.Term
		if err := rows.Scan(&t.ID, &t.Taxonomy, &t.Slug, &t.Name, &t.Description, &t.ParentID, &t.Count); err != nil {
			return err
		}
		if err := db.InsertTerm(ctx, im.tx, &t); err != nil {
			return err
		}
		im.rep.add("terms:"+t.Taxonomy, 1)
	}
	return rows.Err()
}

func (im *importer) importPosts(ctx context.Context) error {
	rows, err := im.src.QueryContext(ctx, `SELECT ID, post_type, post_status, post_name, post_parent, post_author,
		post_title, post_content, post_excerpt, post_date, post_date_gmt, post_modified, post_modified_gmt,
		comment_status, ping_status, post_password, menu_order, post_mime_type, guid
		FROM `+im.table("posts")+`
		WHERE (post_type IN ('post', 'page') AND post_status IN ('publish', 'draft', 'pending', 'private', 'future'))
		   OR (post_type = 'attachment')
		ORDER BY ID`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var p db.Post
		var date, dateGMT, mod, modGMT string
		if err := rows.Scan(&p.ID, &p.Type, &p.Status, &p.Slug, &p.ParentID, &p.AuthorID, &p.Title, &p.Content,
			&p.Excerpt, &date, &dateGMT, &mod, &modGMT, &p.CommentStatus, &p.PingStatus, &p.Password,
			&p.MenuOrder, &p.MimeType, &p.GUID); err != nil {
			return err
		}
		p.Date, p.DateGMT = parseTime(date), parseTime(dateGMT)
		p.Modified, p.ModifiedGMT = parseTime(mod), parseTime(modGMT)
		if p.DateGMT.IsZero() { // drafts have no GMT date in WordPress
			p.DateGMT = p.Date
		}
		if p.Type != db.TypeAttachment {
			original := p.Content
			var warnings []string
			p.Content, p.ContentFormat, warnings = convertContent(p.Content, im.home)
			for _, w := range warnings {
				im.rep.warn("post %d: %s", p.ID, w)
			}
			if err := db.InsertPost(ctx, im.tx, &p); err != nil {
				return err
			}
			if p.Content != original {
				if err := db.AddPostMeta(ctx, im.tx, p.ID, "_wp_original_content", original); err != nil {
					return err
				}
			}
			if src, ok := excerptSource(original, im.home); ok && src != p.Content {
				if err := db.AddPostMeta(ctx, im.tx, p.ID, db.ExcerptSourceKey, db.ExcerptMeta(p.Content, src)); err != nil {
					return err
				}
			}
		} else if err := db.InsertPost(ctx, im.tx, &p); err != nil {
			return err
		}
		im.postID[p.ID] = p.Type
		im.rep.add("posts:"+p.Type+":"+p.Status, 1)
	}
	return rows.Err()
}

func (im *importer) importPostMeta(ctx context.Context) error {
	rows, err := im.src.QueryContext(ctx, `SELECT post_id, meta_key, meta_value FROM `+im.table("postmeta")+` ORDER BY meta_id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	media := map[int64]*db.Media{}
	getMedia := func(id int64) *db.Media {
		if media[id] == nil {
			media[id] = &db.Media{PostID: id}
		}
		return media[id]
	}
	for rows.Next() {
		var id int64
		var key, value string
		if err := rows.Scan(&id, &key, &value); err != nil {
			return err
		}
		typ, ok := im.postID[id]
		if !ok {
			continue
		}
		switch key {
		case "_wp_old_slug":
			if err := db.AddSlugRedirect(ctx, im.tx, value, id); err != nil {
				return err
			}
			im.rep.add("old slugs", 1)
		case "_thumbnail_id":
			if _, err := im.tx.ExecContext(ctx, `UPDATE posts SET featured_media_id = ? WHERE id = ?`, atoi(value), id); err != nil {
				return err
			}
		case "_wp_page_template":
			if value != "default" {
				if _, err := im.tx.ExecContext(ctx, `UPDATE posts SET template = ? WHERE id = ?`, value, id); err != nil {
					return err
				}
			}
		case "_wp_attached_file":
			getMedia(id).File = value
		case "_wp_attachment_image_alt":
			getMedia(id).Alt = value
		case "_wp_attachment_metadata":
			applyAttachmentMetadata(getMedia(id), phpser.Maybe(value))
		default:
			if skippedMeta[key] || typ == db.TypeAttachment && strings.HasPrefix(key, "_wp_attachment_") {
				continue
			}
			if err := db.AddPostMeta(ctx, im.tx, id, key, value); err != nil {
				return err
			}
			im.rep.add("post meta", 1)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	ids := make([]int64, 0, len(media))
	for id := range media {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		m := media[id]
		if m.File == "" {
			im.rep.warn("attachment %d has no file", id)
			continue
		}
		var mime string
		if err := im.tx.QueryRowContext(ctx, `SELECT mime_type FROM posts WHERE id = ?`, id).Scan(&mime); err != nil {
			return err
		}
		m.Mime = mime
		if m.Filesize == 0 && im.opts.UploadsDir != "" {
			if fi, err := os.Stat(filepath.Join(im.opts.UploadsDir, filepath.FromSlash(m.File))); err == nil {
				m.Filesize = fi.Size()
			} else {
				im.rep.warn("attachment %d: file %s missing", id, m.File)
			}
		}
		if err := db.InsertMedia(ctx, im.tx, m); err != nil {
			return err
		}
		im.rep.add("media", 1)
	}
	return nil
}

func applyAttachmentMetadata(m *db.Media, v any) {
	meta, ok := v.(map[string]any)
	if !ok {
		return
	}
	m.Width = int(toInt(meta["width"]))
	m.Height = int(toInt(meta["height"]))
	m.Filesize = toInt(meta["filesize"])
	sizes, _ := meta["sizes"].(map[string]any)
	if len(sizes) == 0 {
		return
	}
	m.Sizes = map[string]db.MediaSize{}
	for name, s := range sizes {
		sm, ok := s.(map[string]any)
		if !ok {
			continue
		}
		file, _ := sm["file"].(string)
		mime, _ := sm["mime-type"].(string)
		m.Sizes[name] = db.MediaSize{File: file, Width: int(toInt(sm["width"])), Height: int(toInt(sm["height"])), Mime: mime}
	}
}

func (im *importer) importTermRelationships(ctx context.Context) error {
	rows, err := im.src.QueryContext(ctx, `SELECT tr.object_id, tt.term_id, tr.term_order
		FROM `+im.table("term_relationships")+` tr JOIN `+im.table("term_taxonomy")+` tt ON tt.term_taxonomy_id = tr.term_taxonomy_id
		WHERE tt.taxonomy IN ('category', 'post_tag')`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var postID, termID int64
		var order int
		if err := rows.Scan(&postID, &termID, &order); err != nil {
			return err
		}
		if _, ok := im.postID[postID]; !ok {
			continue
		}
		if err := db.AddPostTerm(ctx, im.tx, postID, termID, order); err != nil {
			return err
		}
		im.rep.add("term relationships", 1)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return db.RecountTerms(ctx, im.tx)
}

func (im *importer) importComments(ctx context.Context) error {
	rows, err := im.src.QueryContext(ctx, `SELECT comment_ID, comment_post_ID, comment_parent, comment_type, comment_approved,
		comment_author, comment_author_email, comment_author_url, comment_author_IP, comment_agent, user_id,
		comment_date, comment_date_gmt, comment_content
		FROM `+im.table("comments")+` ORDER BY comment_ID`)
	if err != nil {
		return err
	}
	defer rows.Close()
	posts := map[int64]bool{}
	for rows.Next() {
		var c db.Comment
		var approved, date, dateGMT string
		if err := rows.Scan(&c.ID, &c.PostID, &c.ParentID, &c.Type, &approved, &c.Author, &c.AuthorEmail, &c.AuthorURL,
			&c.AuthorIP, &c.Agent, &c.UserID, &date, &dateGMT, &c.Content); err != nil {
			return err
		}
		if _, ok := im.postID[c.PostID]; !ok {
			im.rep.add("comments skipped (no post)", 1)
			continue
		}
		switch approved {
		case "1":
			c.Status = db.CommentApproved
		case "0":
			c.Status = db.CommentPending
		case "spam":
			c.Status = db.CommentSpam
		default: // trash, post-trashed
			c.Status = db.CommentTrash
		}
		if c.Type == "" {
			c.Type = "comment"
		}
		c.Date, c.DateGMT = parseTime(date), parseTime(dateGMT)
		if err := db.InsertComment(ctx, im.tx, &c); err != nil {
			return err
		}
		posts[c.PostID] = true
		im.rep.add("comments:"+c.Type+":"+c.Status, 1)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for id := range posts {
		if err := db.UpdateCommentCount(ctx, im.tx, id); err != nil {
			return err
		}
	}
	return nil
}

func (im *importer) importWidgets(ctx context.Context) error {
	opt := func(name string) any {
		var v string
		err := im.src.QueryRowContext(ctx, `SELECT option_value FROM `+im.table("options")+` WHERE option_name = ?`, name).Scan(&v)
		if err != nil {
			return nil
		}
		return phpser.Maybe(v)
	}
	sidebars, ok := opt("sidebars_widgets").(map[string]any)
	if !ok {
		return nil
	}
	names := make([]string, 0, len(sidebars))
	for name := range sidebars {
		names = append(names, name)
	}
	sort.Strings(names)
	instances := map[string]map[string]any{}
	for _, sidebar := range names {
		if sidebar == "wp_inactive_widgets" || sidebar == "array_version" {
			continue
		}
		list, _ := sidebars[sidebar].([]any)
		for pos, w := range list {
			id, _ := w.(string)
			cut := strings.LastIndexByte(id, '-')
			if cut < 0 {
				im.rep.warn("widget %q: bad id", id)
				continue
			}
			typ, num := id[:cut], id[cut+1:]
			if instances[typ] == nil {
				instances[typ], _ = opt("widget_" + typ).(map[string]any)
			}
			settings, _ := instances[typ][num].(map[string]any)
			if content, ok := settings["content"].(string); ok && typ == "block" {
				converted, _, warnings := convertContent(content, im.home)
				for _, w := range warnings {
					im.rep.warn("widget %s: %s", id, w)
				}
				settings["content"] = converted
			}
			if err := db.InsertWidget(ctx, im.tx, &db.Widget{ID: id, Sidebar: sidebar, Position: pos, Type: typ, Settings: settings}); err != nil {
				return err
			}
			im.rep.add("widgets", 1)
		}
	}
	return nil
}

func (im *importer) stashPluginData(ctx context.Context) error {
	for _, typ := range pluginPostTypes {
		rows, err := im.src.QueryContext(ctx, `SELECT ID, post_status, post_name, post_title, post_content, post_date_gmt
			FROM `+im.table("posts")+` WHERE post_type = ? AND post_status NOT IN ('auto-draft', 'trash', 'inherit')`, typ)
		if err != nil {
			return err
		}
		type entry struct {
			ID      int64          `json:"id"`
			Status  string         `json:"status"`
			Slug    string         `json:"slug"`
			Title   string         `json:"title"`
			Content string         `json:"content"`
			DateGMT string         `json:"date_gmt"`
			Meta    map[string]any `json:"meta"`
		}
		var entries []entry
		for rows.Next() {
			var e entry
			if err := rows.Scan(&e.ID, &e.Status, &e.Slug, &e.Title, &e.Content, &e.DateGMT); err != nil {
				rows.Close()
				return err
			}
			entries = append(entries, e)
		}
		rows.Close()
		for _, e := range entries {
			e.Meta = map[string]any{}
			mrows, err := im.src.QueryContext(ctx, `SELECT meta_key, meta_value FROM `+im.table("postmeta")+` WHERE post_id = ? ORDER BY meta_id`, e.ID)
			if err != nil {
				return err
			}
			for mrows.Next() {
				var k, v string
				if err := mrows.Scan(&k, &v); err != nil {
					mrows.Close()
					return err
				}
				if k != "_edit_lock" && k != "_edit_last" {
					e.Meta[k] = phpser.Maybe(v)
				}
			}
			mrows.Close()
			if err := db.Stash(ctx, im.tx, "wordpress", "post:"+typ, strconv.FormatInt(e.ID, 10), e); err != nil {
				return err
			}
			im.rep.add("stashed "+typ, 1)
		}
	}
	return nil
}

func parseTime(s string) db.Time {
	t, err := time.Parse(db.TimeLayout, s)
	if err != nil {
		return db.Time{}
	}
	return db.Time{Time: t}
}

func atoi(s string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return n
}

func toInt(v any) int64 {
	switch v := v.(type) {
	case int64:
		return v
	case float64:
		return int64(v)
	case string:
		return atoi(v)
	}
	return 0
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// copyTree copies the regular files below src to dst, hardlinking when
// possible. It returns the number of files.
func copyTree(src, dst string) (int, error) {
	n := 0
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		// Skip hidden files/dirs (.htaccess, .git) and server-side scripts;
		// they are never served and may hold plugin secrets.
		if strings.HasPrefix(d.Name(), ".") && rel != "." {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if ext := strings.ToLower(filepath.Ext(d.Name())); ext == ".php" || ext == ".phtml" || ext == ".phar" {
			return nil
		}
		if d.IsDir() {
			return os.MkdirAll(target, 0o750)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		n++
		if _, err := os.Stat(target); err == nil {
			return nil
		}
		if err := os.Link(path, target); err == nil {
			return nil
		}
		return copyFile(path, target)
	})
	return n, err
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
