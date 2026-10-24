// Package content holds the write logic for posts, pages, revisions and
// comments shared by the admin (and later plugins): WordPress-compatible
// slugs, statuses, dates and scheduling, term assignment, revisions,
// trash and comment moderation.
package content

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"

	"github.com/theinvisible/gopress/internal/db"
	"github.com/theinvisible/gopress/internal/render"
)

// accentSpecial maps characters remove_accents() converts that do not
// decompose into a base letter plus combining marks.
var accentSpecial = map[rune]string{
	'ß': "ss", 'ẞ': "SS", 'æ': "ae", 'Æ': "AE", 'ø': "o", 'Ø': "O", 'œ': "oe", 'Œ': "OE",
	'đ': "d", 'Đ': "D", 'ð': "d", 'Ð': "D", 'ł': "l", 'Ł': "L", 'þ': "th", 'Þ': "TH",
	'ħ': "h", 'Ħ': "H", 'ı': "i", 'ĳ': "ij", 'Ĳ': "IJ", 'ŀ': "l", 'Ŀ': "L", 'ŉ': "n",
	'ŋ': "n", 'Ŋ': "N", 'ſ': "s", 'ŧ': "t", 'Ŧ': "T", 'ª': "a", 'º': "o",
	'€': "E", '£': "", '¢': "",
}

// RemoveAccents converts accented Latin letters to ASCII like WordPress'
// remove_accents() (default locale).
func RemoveAccents(s string) string {
	ascii := true
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			ascii = false
			break
		}
	}
	if ascii {
		return s
	}
	var b strings.Builder
	for _, r := range norm.NFD.String(s) {
		if r < 0x80 {
			b.WriteRune(r)
			continue
		}
		if unicode.Is(unicode.Mn, r) {
			continue // combining mark of a decomposed letter
		}
		if rep, ok := accentSpecial[r]; ok {
			b.WriteString(rep)
			continue
		}
		b.WriteRune(r)
	}
	return norm.NFC.String(b.String())
}

var (
	reOctet      = regexp.MustCompile(`%([a-fA-F0-9][a-fA-F0-9])`)
	reOctetMark  = regexp.MustCompile(`---([a-fA-F0-9][a-fA-F0-9])---`)
	reEntity     = regexp.MustCompile(`&.+?;`)
	reSlugJunk   = regexp.MustCompile(`[^%a-z0-9 _-]`)
	reSlugSpace  = regexp.MustCompile(`\s+`)
	reSlugDashes = regexp.MustCompile(`-+`)
)

// slugDashes are characters sanitize_title_with_dashes() turns into
// dashes, slugDrop those it removes (context "save").
var (
	slugDashes = strings.NewReplacer(" ", "-", "–", "-", "—", "-", "&nbsp;", "-", "&#160;", "-",
		"&ndash;", "-", "&#8211;", "-", "&mdash;", "-", "&#8212;", "-", "/", "-")
	slugDrop = strings.NewReplacer("¡", "", "¿", "", "«", "", "»", "", "‹", "", "›", "",
		"‘", "", "’", "", "“", "", "”", "", "‚", "", "‛", "", "„", "", "‟", "",
		"•", "", "©", "", "®", "", "°", "", "…", "", "™", "",
		"´", "", "ˊ", "", "́", "", "́", "", "̀", "", "̀", "",
		"̃", "", "͂", "", "¨", "", "·", "")
)

// SanitizeTitle turns a title into a slug like sanitize_title() with
// sanitize_title_with_dashes() in the "save" context: accents removed,
// lowercase, special characters dropped, spaces to dashes, other
// non-ASCII characters percent-encoded.
func SanitizeTitle(title string) string {
	s := RemoveAccents(render.StripAllTags(title))
	s = reOctet.ReplaceAllString(s, "---$1---")
	s = strings.ReplaceAll(s, "%", "")
	s = reOctetMark.ReplaceAllString(s, "%$1")
	if utf8.ValidString(s) {
		s = strings.ToLower(s)
		s = slugDashes.Replace(s)
		s = slugDrop.Replace(s)
		s = strings.ReplaceAll(s, "×", "x")
		s = uriEncode(s, 200)
	}
	s = strings.ToLower(s)
	s = reEntity.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, ".", "-")
	s = reSlugJunk.ReplaceAllString(s, "")
	s = reSlugSpace.ReplaceAllString(s, "-")
	s = reSlugDashes.ReplaceAllString(s, "-")
	return strings.Trim(s, "-")
}

// uriEncode percent-encodes non-ASCII characters (utf8_uri_encode), up to
// max encoded bytes, never splitting a character.
func uriEncode(s string, max int) string {
	var b strings.Builder
	for _, r := range s {
		var piece string
		if r < 0x80 {
			piece = string(r)
		} else {
			var enc strings.Builder
			for _, c := range []byte(string(r)) {
				fmt.Fprintf(&enc, "%%%02x", c)
			}
			piece = enc.String()
		}
		if b.Len()+len(piece) > max {
			break
		}
		b.WriteString(piece)
	}
	return b.String()
}

// reservedSlugs cannot be used by posts or pages because they are
// endpoints of the permalink structure.
var reservedSlugs = map[string]bool{"feed": true, "rdf": true, "rss": true, "rss2": true, "atom": true,
	"embed": true, "page": true, "comments": true, "trackback": true, "wp-admin": true, "admin": true,
	"wp-content": true, "wp-includes": true, "login": true, "static": true}

var reNumericSlug = regexp.MustCompile(`^\d+$`)

// UniqueSlug returns slug, or slug-2, slug-3, … if it is taken, like
// wp_unique_post_slug(): pages are unique among siblings, posts among
// posts; attachments share the namespace with both.
func UniqueSlug(ctx context.Context, q db.Querier, slug string, p *db.Post) (string, error) {
	if slug == "" {
		return "", nil
	}
	types := []string{p.Type, db.TypeAttachment}
	parent := int64(-1)
	if p.Type == db.TypePage {
		parent = p.ParentID
	}
	taken := func(s string) (bool, error) {
		if reservedSlugs[s] || (p.Type == db.TypePost && reNumericSlug.MatchString(s)) {
			return true, nil
		}
		return db.SlugTaken(ctx, q, s, types, parent, p.ID)
	}
	ok, err := taken(slug)
	if err != nil || !ok {
		return slug, err
	}
	base := slug
	if len(base) > 190 {
		base = base[:190]
	}
	for n := 2; n < 10000; n++ {
		cand := base + "-" + strconv.Itoa(n)
		ok, err := taken(cand)
		if err != nil {
			return "", err
		}
		if !ok {
			return cand, nil
		}
	}
	return "", fmt.Errorf("no unique slug for %q", slug)
}
