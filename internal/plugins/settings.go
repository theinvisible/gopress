package plugins

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/mail"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/theinvisible/gopress/internal/db"
)

// Settings pages store their values in the collection "_settings" (key:
// page id), which plugins can read but not write: only the admin form,
// validated against the manifest's fields, changes them.
const settingsColl = "_settings"

func (h *Host) settingsPage(name, page string) (*AdminPage, error) {
	pkg := h.opts.Store.Get(name)
	if pkg == nil {
		return nil, ErrUnavailable
	}
	for i, p := range pkg.Manifest.Admin {
		if p.ID == page && p.Kind == "settings" {
			return &pkg.Manifest.Admin[i], nil
		}
	}
	return nil, userErr("no settings page " + strconv.Quote(page))
}

// Settings returns the values of a settings page (defaults filled in).
func (h *Host) Settings(ctx context.Context, name, page string) (map[string]any, error) {
	pg, err := h.settingsPage(name, page)
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	for _, f := range pg.Fields {
		if f.Default != nil {
			out[f.Name] = f.Default
		} else {
			out[f.Name] = zeroValue(f)
		}
	}
	raw, ok, err := db.KVGet(ctx, h.opts.DB.R, name, settingsColl, page)
	if err != nil || !ok {
		return out, err
	}
	var stored map[string]any
	if json.Unmarshal([]byte(raw), &stored) == nil {
		for _, f := range pg.Fields {
			if v, ok := stored[f.Name]; ok {
				out[f.Name] = v
			}
		}
	}
	return out, nil
}

func zeroValue(f Field) any {
	switch f.Type {
	case "checkbox":
		return false
	case "number":
		return 0.0
	case "media_list":
		return []any{}
	case "media":
		return 0.0
	}
	return ""
}

var reColor = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// ParseFields validates submitted form values against fields; get returns
// the submitted string(s) of a field.
func ParseFields(fields []Field, get func(name string) []string) (map[string]any, error) {
	out := map[string]any{}
	for _, f := range fields {
		vals := get(f.Name)
		v := ""
		if len(vals) > 0 {
			v = strings.TrimSpace(vals[0])
		}
		maxLen := f.MaxLen
		if maxLen == 0 {
			maxLen = 10000
		}
		if f.Required && v == "" && f.Type != "checkbox" && f.Type != "media_list" {
			return nil, fmt.Errorf("%s is required.", f.Label)
		}
		switch f.Type {
		case "checkbox":
			out[f.Name] = v != ""
		case "number":
			if v == "" {
				out[f.Name] = 0.0
				continue
			}
			n, err := strconv.ParseFloat(v, 64)
			if err != nil || (f.Min != nil && n < *f.Min) || (f.Max != nil && n > *f.Max) {
				return nil, fmt.Errorf("%s must be a number in the allowed range.", f.Label)
			}
			out[f.Name] = n
		case "select":
			if !slices.ContainsFunc(f.Options, func(o Option) bool { return o.Value == v }) {
				return nil, fmt.Errorf("Please choose a valid value for %s.", f.Label)
			}
			out[f.Name] = v
		case "url":
			if v != "" && !strings.HasPrefix(v, "https://") && !strings.HasPrefix(v, "http://") && !strings.HasPrefix(v, "/") {
				return nil, fmt.Errorf("%s must be a URL.", f.Label)
			}
			out[f.Name] = v
		case "email":
			if v != "" {
				if a, err := mail.ParseAddress(v); err != nil || a.Address != v {
					return nil, fmt.Errorf("%s must be an email address.", f.Label)
				}
			}
			out[f.Name] = v
		case "color":
			if v != "" && !reColor.MatchString(v) {
				return nil, fmt.Errorf("%s must be a color like #1a2b3c.", f.Label)
			}
			out[f.Name] = v
		case "media":
			id, _ := strconv.ParseInt(v, 10, 64)
			out[f.Name] = float64(max(id, 0))
		case "media_list":
			var ids []any
			for _, part := range strings.FieldsFunc(strings.Join(vals, ","), func(r rune) bool { return r == ',' || r == ' ' }) {
				if id, err := strconv.ParseInt(part, 10, 64); err == nil && id > 0 && len(ids) < 1000 {
					ids = append(ids, float64(id))
				}
			}
			if ids == nil {
				ids = []any{}
			}
			out[f.Name] = ids
		default: // text, textarea, password
			if len(v) > maxLen || strings.ContainsRune(v, 0) {
				return nil, fmt.Errorf("%s is too long.", f.Label)
			}
			if f.Type != "textarea" && strings.ContainsAny(v, "\r\n") {
				return nil, fmt.Errorf("%s must be one line.", f.Label)
			}
			out[f.Name] = v
		}
	}
	return out, nil
}

// SaveSettings stores validated settings of a page.
func (h *Host) SaveSettings(ctx context.Context, name, page string, values map[string]any) error {
	if _, err := h.settingsPage(name, page); err != nil {
		return err
	}
	b, err := json.Marshal(values)
	if err != nil {
		return err
	}
	err = h.opts.DB.Tx(ctx, func(tx *sql.Tx) error {
		return db.KVSet(ctx, tx, name, settingsColl, page, string(b), h.now().Unix(), kvQuota)
	})
	if err != nil {
		return err
	}
	h.opts.Site.Invalidate()
	return nil
}
