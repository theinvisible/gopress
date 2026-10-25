package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	gp "github.com/theinvisible/gopress/pdk/go"
)

// Gallery is a photo gallery (KV galleries/<id>).
type Gallery struct {
	ID       int64   `json:"id"`
	Title    string  `json:"title"`
	Images   []int64 `json:"images"`   // attachment ids in display order
	Captions bool    `json:"captions"` // show the images' captions
	Owner    int64   `json:"owner,omitempty"`
	Created  string  `json:"created,omitempty"`
	Updated  string  `json:"updated,omitempty"`
}

// Limits of a gallery.
const (
	maxTitle  = 200
	maxImages = 500
)

const collGalleries = "galleries"

// loadGallery reads a gallery; false if there is none.
func loadGallery(id int64) (Gallery, bool, error) {
	var g Gallery
	ok, err := gp.KVGet(collGalleries, strconv.FormatInt(id, 10), &g)
	return g, ok, err
}

// allGalleries reads every gallery, ordered by title.
func allGalleries() ([]Gallery, error) {
	var out []Gallery
	after := ""
	for {
		entries, next, err := gp.KVList(collGalleries, after, 500)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			var g Gallery
			if json.Unmarshal(e.Value, &g) == nil {
				out = append(out, g)
			}
		}
		if next == "" {
			break
		}
		after = next
	}
	slices.SortFunc(out, func(a, b Gallery) int {
		if c := strings.Compare(strings.ToLower(a.Title), strings.ToLower(b.Title)); c != 0 {
			return c
		}
		return int(a.ID - b.ID)
	})
	return out, nil
}

// nextID returns an id above every gallery's (imported ids stay as they
// were, and ids of deleted galleries are not used again).
func nextID() (int64, error) {
	var last int64
	if _, err := gp.KVGet("meta", "last_id", &last); err != nil {
		return 0, err
	}
	all, err := allGalleries()
	if err != nil {
		return 0, err
	}
	for _, g := range all {
		last = max(last, g.ID)
	}
	last++
	return last, gp.KVSet("meta", "last_id", last)
}

// parseBool reads a shortcode flag like PHP's FILTER_VALIDATE_BOOLEAN:
// 1, true, on and yes are true.
func parseBool(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "on", "yes":
		return true
	}
	return false
}

// slide is an image of the slider (templates/gallery.html).
type slide struct {
	Full, Thumb, Src, Srcset, Alt, Caption string
	Width, Height                          int
}

// renderGallery builds the slider of g with the images in media (in the
// gallery's order; ids the host did not return are left out).
func renderGallery(g Gallery, media []gp.Media, captions bool) (string, error) {
	byID := map[int64]gp.Media{}
	for _, m := range media {
		byID[m.ID] = m
	}
	var slides []slide
	for _, id := range g.Images {
		m, ok := byID[id]
		if !ok || m.URL == "" || !strings.HasPrefix(m.Mime, "image/") {
			continue
		}
		s := slide{Full: m.URL, Thumb: m.URL, Src: m.URL, Width: m.Width, Height: m.Height, Alt: m.Alt,
			Caption: strings.TrimSpace(html.UnescapeString(stripTags(m.Caption)))}
		if l, ok := m.Sizes["large"]; ok && l.URL != "" {
			s.Src, s.Width, s.Height = l.URL, l.Width, l.Height
		}
		if t, ok := m.Sizes["thumbnail"]; ok && t.URL != "" {
			s.Thumb = t.URL
		}
		if s.Width > 0 && s.Height > 0 {
			s.Srcset = srcset(m)
		} else {
			s.Width, s.Height = 0, 0
		}
		slides = append(slides, s)
	}
	if len(slides) == 0 {
		return "", nil
	}
	return gp.Template("gallery.html", map[string]any{"ID": g.ID, "Captions": captions, "Slides": slides})
}

// srcset lists the full image and the sizes with its aspect ratio.
func srcset(m gp.Media) string {
	if m.Width <= 0 || m.Height <= 0 {
		return ""
	}
	ratio := float64(m.Width) / float64(m.Height)
	type cand struct {
		url string
		w   int
	}
	var list []cand
	seen := map[int]bool{}
	for _, s := range m.Sizes {
		if s.URL == "" || s.Width <= 0 || s.Height <= 0 || seen[s.Width] {
			continue
		}
		if math.Abs(float64(s.Width)/float64(s.Height)-ratio) > 0.01*ratio {
			continue // cropped (thumbnail)
		}
		seen[s.Width] = true
		list = append(list, cand{s.URL, s.Width})
	}
	if !seen[m.Width] {
		list = append(list, cand{m.URL, m.Width})
	}
	if len(list) < 2 {
		return ""
	}
	slices.SortFunc(list, func(a, b cand) int { return a.w - b.w })
	parts := make([]string, len(list))
	for i, c := range list {
		parts[i] = c.url + " " + strconv.Itoa(c.w) + "w"
	}
	return strings.Join(parts, ", ")
}

// stripTags removes markup from a caption (captions are shown as text).
func stripTags(s string) string {
	var b strings.Builder
	in := false
	for _, r := range s {
		switch {
		case r == '<':
			in = true
		case r == '>' && in:
			in = false
		case !in:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// galleryInput is what the admin page saves.
type galleryInput struct {
	ID       int64   `json:"id"`
	Title    string  `json:"title"`
	Images   []int64 `json:"images"`
	Captions bool    `json:"captions"`
}

var errInvalid = errors.New("invalid gallery")

// clean checks a gallery from the admin page.
func (in *galleryInput) clean() error {
	in.Title = strings.Join(strings.Fields(in.Title), " ")
	if in.Title == "" || utf8.RuneCountInString(in.Title) > maxTitle || !utf8.ValidString(in.Title) {
		return errors.New("the title must be 1–200 characters")
	}
	if len(in.Images) > maxImages {
		return errors.New("a gallery holds at most 500 images")
	}
	seen := map[int64]bool{}
	ids := in.Images[:0]
	for _, id := range in.Images {
		if id <= 0 {
			return errInvalid
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	in.Images = ids
	return nil
}

// canEdit: editors and administrators edit every gallery, authors their
// own. Contributors (no uploads of their own) only pick galleries.
func canEdit(u gp.User, g Gallery) bool {
	return u.Role == "administrator" || u.Role == "editor" || (u.Role == "author" && g.Owner != 0 && g.Owner == u.ID)
}

// canCreate: who may make galleries (the roles that may upload images).
func canCreate(u gp.User) bool {
	return u.Role == "administrator" || u.Role == "editor" || u.Role == "author"
}

// Limits against filling the plugin's storage.
const (
	maxGalleries        = 2000
	maxGalleriesPerUser = 200
)

// fromStash converts an imported fslb_gallery post.
func fromStash(raw json.RawMessage) (Gallery, bool) {
	var p struct {
		ID      int64          `json:"id"`
		Status  string         `json:"status"`
		Title   string         `json:"title"`
		DateGMT string         `json:"date_gmt"`
		Meta    map[string]any `json:"meta"`
	}
	if json.Unmarshal(raw, &p) != nil || p.ID <= 0 || p.Status == "trash" {
		return Gallery{}, false
	}
	g := Gallery{ID: p.ID, Title: strings.TrimSpace(html.UnescapeString(p.Title)), Captions: true}
	if g.Title == "" {
		g.Title = "Gallery " + strconv.FormatInt(p.ID, 10)
	}
	if t := strings.Replace(p.DateGMT, " ", "T", 1); t != "" {
		g.Created = t + "Z"
	}
	switch ids := p.Meta["_fslb_image_ids"].(type) {
	case []any:
		for _, v := range ids {
			if id := toID(v); id > 0 && !slices.Contains(g.Images, id) {
				g.Images = append(g.Images, id)
			}
		}
	case map[string]any: // a PHP array with gaps decodes as an object
		keys := make([]int, 0, len(ids))
		for k := range ids {
			if n, err := strconv.Atoi(k); err == nil {
				keys = append(keys, n)
			}
		}
		slices.Sort(keys)
		for _, k := range keys {
			if id := toID(ids[strconv.Itoa(k)]); id > 0 && !slices.Contains(g.Images, id) {
				g.Images = append(g.Images, id)
			}
		}
	}
	// Not saved means on (the original's default).
	if v, ok := p.Meta["_fslb_show_captions"]; ok {
		if s := fmt.Sprint(v); s != "" {
			g.Captions = s == "1"
		}
	}
	if len(g.Images) > maxImages {
		g.Images = g.Images[:maxImages]
	}
	return g, true
}

func toID(v any) int64 {
	switch x := v.(type) {
	case float64:
		if x > 0 && x == math.Trunc(x) {
			return int64(x)
		}
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(x), 10, 64)
		return n
	}
	return 0
}
