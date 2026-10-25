// Photo Slider & Lightbox: photo galleries as a Swiper slider with a
// lightbox (zoom, thumbnails, keyboard, focus trap). Galleries are kept in
// the plugin's storage and managed on an admin page; posts show them with
// [fotogalerie id="…" captions="…"]. The WordPress plugin's galleries
// (fslb_gallery posts) are imported on activation.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	gp "github.com/theinvisible/gopress/pdk/go"
)

var assets = []string{"assets/swiper/swiper-bundle.min.css", "assets/view.css", "assets/swiper/swiper-bundle.min.js", "assets/view.js"}

func init() {
	gp.HandleShortcode("fotogalerie", shortcode)
	gp.HandleAPI("galleries", api)
	gp.HandleAPI("pick", api)
	gp.HandleMigrate(migrate)
}

// [fotogalerie id="12" captions="1"]: captions empty uses the gallery's
// setting.
func shortcode(r gp.ShortcodeRequest) (gp.Render, error) {
	id, err := strconv.ParseInt(r.Attr("id", ""), 10, 64)
	if err != nil || id <= 0 {
		return gp.Render{}, nil
	}
	g, ok, err := loadGallery(id)
	if err != nil || !ok || len(g.Images) == 0 {
		return gp.Render{}, err
	}
	captions := g.Captions
	if c := r.Attr("captions", ""); c != "" {
		captions = parseBool(c)
	}
	media, err := gp.GetMedia(g.Images...)
	if err != nil {
		return gp.Render{}, err
	}
	site, err := gp.Site()
	if err != nil {
		return gp.Render{}, err
	}
	out, err := renderGallery(g, media, captions, site.ContentWidth)
	if err != nil || out == "" {
		return gp.Render{}, err
	}
	return gp.Render{HTML: out, Assets: assets}, nil
}

// galleryInfo is a gallery as the admin pages see it.
type galleryInfo struct {
	Gallery
	Count    int  `json:"count"`
	Editable bool `json:"editable"`
}

// thumb is an image of a gallery in the admin.
type thumb struct {
	ID    int64  `json:"id"`
	URL   string `json:"url"`
	Title string `json:"title"`
}

// api answers the admin pages (galleries: manage; pick: the editor
// dialog). Everyone who may open the pages may list galleries; changing
// one needs to be its owner, an editor or an administrator.
func api(r gp.AdminRequest) (any, error) {
	switch r.Method {
	case "list":
		all, err := allGalleries()
		if err != nil {
			return nil, err
		}
		out := []galleryInfo{}
		for _, g := range all {
			out = append(out, galleryInfo{Gallery: g, Count: len(g.Images), Editable: canEdit(r.User, g)})
		}
		return out, nil
	case "get":
		g, err := galleryParam(r.Params)
		if err != nil {
			return nil, err
		}
		thumbs, err := thumbsOf(g.Images)
		if err != nil {
			return nil, err
		}
		return map[string]any{"gallery": galleryInfo{Gallery: g, Count: len(g.Images), Editable: canEdit(r.User, g)},
			"thumbs": thumbs}, nil
	case "media":
		var p struct {
			IDs []int64 `json:"ids"`
		}
		if json.Unmarshal(r.Params, &p) != nil || len(p.IDs) > maxImages {
			return nil, errInvalid
		}
		return thumbsOf(p.IDs)
	case "save":
		if r.Page != "galleries" {
			return nil, errors.New("not allowed here")
		}
		return save(r)
	case "delete":
		if r.Page != "galleries" {
			return nil, errors.New("not allowed here")
		}
		g, err := galleryParam(r.Params)
		if err != nil {
			return nil, err
		}
		if !canEdit(r.User, g) {
			return nil, errors.New("you may not delete this gallery")
		}
		if err := gp.KVDelete(collGalleries, strconv.FormatInt(g.ID, 10)); err != nil {
			return nil, err
		}
		gp.Invalidate()
		return true, nil
	}
	return nil, errors.New("unknown method")
}

func galleryParam(params json.RawMessage) (Gallery, error) {
	var p struct {
		ID int64 `json:"id"`
	}
	if json.Unmarshal(params, &p) != nil || p.ID <= 0 {
		return Gallery{}, errInvalid
	}
	g, ok, err := loadGallery(p.ID)
	if err != nil {
		return Gallery{}, err
	}
	if !ok {
		return Gallery{}, errors.New("the gallery does not exist")
	}
	return g, nil
}

// thumbsOf describes images for the admin (those the host knows, in the
// given order).
func thumbsOf(ids []int64) ([]thumb, error) {
	out := []thumb{}
	if len(ids) == 0 {
		return out, nil
	}
	media, err := gp.GetMedia(ids...)
	if err != nil {
		return nil, err
	}
	byID := map[int64]gp.Media{}
	for _, m := range media {
		byID[m.ID] = m
	}
	for _, id := range ids {
		m, ok := byID[id]
		if !ok {
			continue
		}
		u := m.URL
		for _, size := range []string{"thumbnail", "medium"} {
			if s, ok := m.Sizes[size]; ok && s.URL != "" {
				u = s.URL
				break
			}
		}
		out = append(out, thumb{ID: m.ID, URL: u, Title: m.Title})
	}
	return out, nil
}

func save(r gp.AdminRequest) (any, error) {
	var in galleryInput
	if err := json.Unmarshal(r.Params, &in); err != nil {
		return nil, errInvalid
	}
	if err := in.clean(); err != nil {
		return nil, err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	var g Gallery
	if in.ID > 0 {
		old, ok, err := loadGallery(in.ID)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, errors.New("the gallery does not exist")
		}
		if !canEdit(r.User, old) {
			return nil, errors.New("you may not change this gallery")
		}
		g = old
	} else {
		if !canCreate(r.User) {
			return nil, errors.New("you may not create galleries")
		}
		all, err := allGalleries()
		if err != nil {
			return nil, err
		}
		mine := 0
		for _, x := range all {
			if x.Owner == r.User.ID {
				mine++
			}
		}
		if len(all) >= maxGalleries || mine >= maxGalleriesPerUser {
			return nil, errors.New("there are too many galleries")
		}
		id, err := nextID()
		if err != nil {
			return nil, err
		}
		g = Gallery{ID: id, Owner: r.User.ID, Created: now}
	}
	// Only images the host knows (and shows) are kept.
	if len(in.Images) > 0 {
		media, err := gp.GetMedia(in.Images...)
		if err != nil {
			return nil, err
		}
		known := map[int64]bool{}
		for _, m := range media {
			known[m.ID] = strings.HasPrefix(m.Mime, "image/")
		}
		ids := in.Images[:0]
		for _, id := range in.Images {
			if known[id] {
				ids = append(ids, id)
			}
		}
		in.Images = ids
	}
	g.Title, g.Images, g.Captions, g.Updated = in.Title, in.Images, in.Captions, now
	if g.Images == nil {
		g.Images = []int64{}
	}
	if err := gp.KVSet(collGalleries, strconv.FormatInt(g.ID, 10), g); err != nil {
		return nil, err
	}
	gp.Invalidate()
	return galleryInfo{Gallery: g, Count: len(g.Images), Editable: true}, nil
}

// migrate imports the WordPress plugin's galleries (again: the stash's
// values replace the stored ones with the same id).
func migrate() (string, error) {
	items, err := gp.Stash("post:fslb_gallery")
	if err != nil {
		return "", err
	}
	n := 0
	for _, raw := range items {
		g, ok := fromStash(raw)
		if !ok {
			continue
		}
		if old, found, err := loadGallery(g.ID); err == nil && found {
			g.Owner = old.Owner
		}
		if err := gp.KVSet(collGalleries, strconv.FormatInt(g.ID, 10), g); err != nil {
			return "", err
		}
		n++
		gp.Progress(n*100/len(items), "Galleries")
	}
	gp.Invalidate()
	if n == 1 {
		return "1 gallery imported.", nil
	}
	return fmt.Sprintf("%d galleries imported.", n), nil
}

func main() {}
