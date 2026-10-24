package render

import (
	"html"
	"regexp"
	"strconv"
	"strings"
)

var reBlockEnd = regexp.MustCompile(`</(?:p|li|h[1-6]|div|blockquote|pre|figure|figcaption|td|th|dt|dd)>`)

// Pipeline applies the_content filters in WordPress' order.
type Pipeline struct {
	Shortcodes map[string]ShortcodeFunc
	UseSmilies bool
}

// NewPipeline returns a pipeline with the built-in shortcodes.
func NewPipeline() *Pipeline {
	return &Pipeline{
		Shortcodes: map[string]ShortcodeFunc{
			"gallery":    GalleryShortcode,
			"caption":    CaptionShortcode,
			"wp_caption": CaptionShortcode,
			// Plugin shortcodes of the source site; rendered by the
			// plugins once the plugin host exists (M4), hidden until then.
			"fotogalerie":     func(*Env, *Shortcode) string { return "" },
			"paypal-donation": func(*Env, *Shortcode) string { return "" },
		},
		UseSmilies: true,
	}
}

// Register adds a shortcode handler.
func (p *Pipeline) Register(name string, fn ShortcodeFunc) { p.Shortcodes[name] = fn }

func (p *Pipeline) names() map[string]bool {
	m := make(map[string]bool, len(p.Shortcodes))
	for k := range p.Shortcodes {
		m[k] = true
	}
	return m
}

// Filter runs the_content filters in WordPress' priority order:
// wptexturize, wpautop + shortcode_unautop (classic content only),
// capital_P_dangit, do_shortcode, wp_filter_content_tags, convert_smilies.
// format is db.FormatWPAutop for classic content, else the content is
// final HTML (block content, where WordPress skips wpautop).
func (p *Pipeline) Filter(env *Env, content, format string) string {
	names := p.names()
	content = Texturize(content, names)
	if format == "wpautop" {
		content = ShortcodeUnautop(Autop(content), names)
	}
	content = CapitalPDangit(content)
	content = DoShortcodes(env, content, p.Shortcodes)
	content = FilterContentTags(env, content)
	if p.UseSmilies {
		content = ConvertSmilies(content)
	}
	return content
}

// ContentOptions control how the post content is cut at <!--more-->.
type ContentOptions struct {
	Teaser   bool   // archive view: show only the part before <!--more-->
	MoreText string // link text, e.g. `Continue reading <span class="meta-nav">&rarr;</span>`
	URL      string // permalink of the post
}

// Content is the_content(): the more tag handling of get_the_content()
// followed by Filter.
func (p *Pipeline) Content(env *Env, content, format string, postID int64, opt ContentOptions) string {
	teaser, rest, moreText, found := SplitMore(content)
	if found {
		if opt.Teaser {
			linkText := opt.MoreText
			if moreText != "" {
				linkText = html.EscapeString(StripTags(moreText))
			}
			content = BalanceTags(teaser + ` <a href="` + html.EscapeString(opt.URL) + `#more-` + strconv.FormatInt(postID, 10) + `" class="more-link">` + linkText + `</a>`)
		} else {
			content = teaser + `<span id="more-` + strconv.FormatInt(postID, 10) + `"></span>` + rest
		}
	}
	return p.Filter(env, content, format)
}

// Excerpt is wp_trim_excerpt() for a post without a manual excerpt.
func (p *Pipeline) Excerpt(env *Env, content, format string) string {
	teaser, _, _, _ := SplitMore(content)
	text := StripShortcodes(teaser, p.Shortcodes)
	text = p.Filter(env, text, format)
	// Block content has no line breaks between elements; WordPress joins
	// blocks with blank lines, so separate words across block boundaries.
	text = reBlockEnd.ReplaceAllString(text, "$0 ")
	text = strings.ReplaceAll(text, "]]>", "]]&gt;")
	return TrimWords(text, 55, ExcerptMore)
}
