package site

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/theinvisible/gopress/internal/db"
)

// Settings are the site options in typed form. WordPress stores most of
// them as strings ("1", "", "10"); the accessors below accept both.
type Settings struct {
	Name, Description string
	Home              string // home URL without trailing slash
	Lang              string
	AdminEmail        string

	PostsPerPage  int
	PostsPerRSS   int
	RSSUseExcerpt bool
	ShowOnFront   string
	PageOnFront   int64
	PageForPosts  int64
	Sticky        []int64
	BlogPublic    bool

	DateFormat, TimeFormat string
	Loc                    *time.Location
	StartOfWeek            int
	UseSmilies             bool

	Permalink    string
	CategoryBase string
	TagBase      string

	DefaultCommentStatus      string
	RequireNameEmail          bool
	CommentRegistration       bool
	CommentModeration         bool
	CommentPreviouslyApproved bool
	CommentMaxLinks           int
	ModerationKeys            []string
	DisallowedKeys            []string
	CloseCommentsForOld       bool
	CloseCommentsDays         int
	ThreadComments            bool
	ThreadDepth               int
	PageComments              bool
	CommentsPerPage           int
	DefaultCommentsPage       string // "newest" or "oldest"
	CommentOrder              string // "asc" or "desc"
	ShowAvatars               bool
	AvatarDefault             string
	AvatarRating              string
	CookiesOptIn              bool
	CommentsNotify            bool // mail the post author about new comments
	ModerationNotify          bool // mail the admin about comments to moderate

	AttachmentPages bool
	ThemeMods       map[string]any
}

// LoadSettings reads the options table.
func LoadSettings(ctx context.Context, q db.Querier) (*Settings, error) {
	raw, err := db.AllOptions(ctx, q)
	if err != nil {
		return nil, err
	}
	o := newOptionMap(raw)
	s := &Settings{
		Name:                      o.str("blogname", "GoPress"),
		Description:               o.str("blogdescription", ""),
		Home:                      strings.TrimRight(o.str("home", "http://localhost:8080"), "/"),
		Lang:                      langTag(o.str("WPLANG", "")),
		AdminEmail:                o.str("admin_email", ""),
		PostsPerPage:              o.int("posts_per_page", 10),
		PostsPerRSS:               o.int("posts_per_rss", 10),
		RSSUseExcerpt:             o.bool("rss_use_excerpt", false),
		ShowOnFront:               o.str("show_on_front", "posts"),
		PageOnFront:               int64(o.int("page_on_front", 0)),
		PageForPosts:              int64(o.int("page_for_posts", 0)),
		Sticky:                    o.ids("sticky_posts"),
		BlogPublic:                o.bool("blog_public", true),
		DateFormat:                o.str("date_format", "F j, Y"),
		TimeFormat:                o.str("time_format", "g:i a"),
		StartOfWeek:               o.int("start_of_week", 1),
		UseSmilies:                o.bool("use_smilies", true),
		Permalink:                 o.str("permalink_structure", "/%year%/%monthnum%/%postname%/"),
		CategoryBase:              strings.Trim(o.str("category_base", ""), "/"),
		TagBase:                   strings.Trim(o.str("tag_base", ""), "/"),
		DefaultCommentStatus:      o.str("default_comment_status", "open"),
		RequireNameEmail:          o.bool("require_name_email", true),
		CommentRegistration:       o.bool("comment_registration", false),
		CommentModeration:         o.bool("comment_moderation", false),
		CommentPreviouslyApproved: o.bool("comment_previously_approved", true),
		CommentMaxLinks:           o.int("comment_max_links", 2),
		ModerationKeys:            lines(o.str("moderation_keys", "")),
		DisallowedKeys:            lines(o.str("disallowed_keys", "")),
		CloseCommentsForOld:       o.bool("close_comments_for_old_posts", false),
		CloseCommentsDays:         o.int("close_comments_days_old", 14),
		ThreadComments:            o.bool("thread_comments", true),
		ThreadDepth:               o.int("thread_comments_depth", 5),
		PageComments:              o.bool("page_comments", false),
		CommentsPerPage:           o.int("comments_per_page", 50),
		DefaultCommentsPage:       o.str("default_comments_page", "newest"),
		CommentOrder:              o.str("comment_order", "asc"),
		ShowAvatars:               o.bool("show_avatars", true),
		AvatarDefault:             o.str("avatar_default", "mystery"),
		AvatarRating:              o.str("avatar_rating", "G"),
		CookiesOptIn:              o.bool("show_comments_cookies_opt_in", false),
		CommentsNotify:            o.bool("comments_notify", true),
		ModerationNotify:          o.bool("moderation_notify", true),
		AttachmentPages:           o.bool("wp_attachment_pages_enabled", true),
	}
	if s.Permalink == "" {
		s.Permalink = "/%year%/%monthnum%/%postname%/"
	}
	if s.CategoryBase == "" {
		s.CategoryBase = "category"
	}
	if s.TagBase == "" {
		s.TagBase = "tag"
	}
	s.Loc = time.UTC
	if tz := o.str("timezone_string", ""); tz != "" {
		if loc, err := time.LoadLocation(tz); err == nil {
			s.Loc = loc
		}
	} else if off, err := strconv.ParseFloat(o.str("gmt_offset", "0"), 64); err == nil && off != 0 {
		s.Loc = time.FixedZone("", int(off*3600))
	}
	if mods, ok := o["theme_mods"].(map[string]any); ok {
		s.ThemeMods = mods
	} else {
		s.ThemeMods = map[string]any{}
	}
	return s, nil
}

type optionMap map[string]any

func newOptionMap(raw map[string]json.RawMessage) optionMap {
	o := optionMap{}
	for k, v := range raw {
		var x any
		if json.Unmarshal(v, &x) == nil {
			o[k] = x
		}
	}
	return o
}

func (o optionMap) str(name, def string) string {
	switch v := o[name].(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		if v {
			return "1"
		}
		return ""
	}
	return def
}

func (o optionMap) int(name string, def int) int {
	switch v := o[name].(type) {
	case float64:
		return int(v)
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return n
		}
	}
	return def
}

func (o optionMap) bool(name string, def bool) bool {
	switch v := o[name].(type) {
	case bool:
		return v
	case float64:
		return v != 0
	case string:
		return v != "" && v != "0"
	}
	return def
}

func (o optionMap) ids(name string) []int64 {
	list, _ := o[name].([]any)
	var out []int64
	for _, v := range list {
		switch v := v.(type) {
		case float64:
			out = append(out, int64(v))
		case string:
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				out = append(out, n)
			}
		}
	}
	return out
}

func lines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// langTag turns a WordPress locale ("de_DE", "" = en_US) into a BCP 47 tag.
func langTag(locale string) string {
	if locale == "" {
		return "en-US"
	}
	return strings.ReplaceAll(locale, "_", "-")
}

// ThemeMod returns a theme modification as a string.
func (s *Settings) ThemeMod(name string) string {
	switch v := s.ThemeMods[name].(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	return ""
}
