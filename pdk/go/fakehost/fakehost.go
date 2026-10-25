//go:build !wasip1

// Package fakehost is an in-memory GoPress host for testing plugins with
// go test (outside WebAssembly): storage, settings, media, imported data,
// HTTP and the notifications a plugin sends.
//
//	h := fakehost.New()
//	h.Media[5] = gp.Media{ID: 5, URL: "https://example.org/a.jpg"}
//	h.Install()
//	var out gp.Render
//	gp.CallExport("render_shortcode", gp.ShortcodeRequest{Tag: "x"}, &out)
package fakehost

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	gp "github.com/theinvisible/gopress/pdk/go"
)

// Host is the fake host. Its fields may be set before and inspected
// after calls.
type Host struct {
	mu       sync.Mutex
	KV       map[string]map[string]json.RawMessage // collection → key → value
	Settings map[string]map[string]any             // page → values
	Media    map[int64]gp.Media
	Stash    map[string]map[string]json.RawMessage // kind → key → value
	Comments map[int64]gp.Comment
	Site     gp.SiteInfo
	// Templates are the plugin's templates (file name → source); see
	// LoadTemplates.
	Templates map[string]string
	// Fetch answers HTTP requests (nil: every request fails).
	Fetch func(gp.HTTPRequest) (gp.HTTPResponse, error)

	Requests    []gp.HTTPRequest
	Mails       []Mail
	Notices     []Notice
	Events      []Event
	Progress    []int
	Statuses    map[int64]string // comment id → status set by the plugin
	Logs        []string
	Invalidated int

	templateVersion int
}

// LoadTemplates reads the plugin's templates/*.html (dir is usually
// "templates").
func (h *Host) LoadTemplates(dir string) error {
	files, err := filepath.Glob(filepath.Join(dir, "*.html"))
	if err != nil {
		return err
	}
	h.Templates = map[string]string{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		h.Templates[filepath.Base(f)] = string(b)
	}
	return nil
}

// Mail is a MailAdmins call.
type Mail struct{ Subject, Body string }

// Notice is a NotifyAdmins call.
type Notice struct{ Text, Kind, Capability string }

// Event is a LivePublish call.
type Event struct {
	Page string
	Data json.RawMessage
}

// New returns an empty host for https://example.org/.
func New() *Host {
	return &Host{KV: map[string]map[string]json.RawMessage{}, Settings: map[string]map[string]any{},
		Media: map[int64]gp.Media{}, Stash: map[string]map[string]json.RawMessage{}, Comments: map[int64]gp.Comment{},
		Statuses: map[int64]string{},
		Site:     gp.SiteInfo{Name: "Test", URL: "https://example.org/", Language: "en-US", TimeZone: "UTC"}}
}

// Install makes h the host of the plugin's calls.
func (h *Host) Install() { gp.SetTestHost(h.call) }

// Get decodes a stored value; false if there is none.
func (h *Host) Get(collection, key string, v any) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	b, ok := h.KV[collection][key]
	if !ok {
		return false
	}
	return json.Unmarshal(b, v) == nil
}

// Put stores a value.
func (h *Host) Put(collection, key string, v any) {
	b, _ := json.Marshal(v)
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.KV[collection] == nil {
		h.KV[collection] = map[string]json.RawMessage{}
	}
	h.KV[collection][key] = b
}

// SetStash stores imported WordPress data.
func (h *Host) SetStash(kind, key string, v any) {
	b, _ := json.Marshal(v)
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.Stash[kind] == nil {
		h.Stash[kind] = map[string]json.RawMessage{}
	}
	h.Stash[kind][key] = b
}

func (h *Host) call(op string, raw json.RawMessage) (any, error) {
	var a struct {
		Collection, Key, After, Page, Kind, Subject, Body, Text, Capability, Status, Level, Message string
		Limit, Percent                                                                              int
		ID                                                                                          int64
		IDs                                                                                         []int64
		Value, Data                                                                                 json.RawMessage
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &a); err != nil {
			return nil, err
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	switch op {
	case "log":
		h.Logs = append(h.Logs, a.Level+": "+a.Message)
		return true, nil
	case "site_info":
		return h.Site, nil
	case "kv_get":
		v, ok := h.KV[a.Collection][a.Key]
		if !ok {
			return map[string]any{"found": false}, nil
		}
		return map[string]any{"found": true, "value": v}, nil
	case "kv_set":
		if strings.HasPrefix(a.Collection, "_") || a.Collection == "" || a.Key == "" {
			return nil, errors.New("invalid collection or key")
		}
		if len(a.Value) > 1<<20 {
			return nil, errors.New("the value must be JSON of at most 1 MB")
		}
		if h.KV[a.Collection] == nil {
			h.KV[a.Collection] = map[string]json.RawMessage{}
		}
		h.KV[a.Collection][a.Key] = a.Value
		return true, nil
	case "kv_delete":
		delete(h.KV[a.Collection], a.Key)
		return true, nil
	case "kv_list":
		var keys []string
		for k := range h.KV[a.Collection] {
			if k > a.After {
				keys = append(keys, k)
			}
		}
		slices.Sort(keys)
		limit := min(max(a.Limit, 1), 500)
		next := ""
		if len(keys) > limit {
			keys = keys[:limit]
			next = keys[len(keys)-1]
		}
		entries := []gp.Entry{}
		for _, k := range keys {
			entries = append(entries, gp.Entry{Key: k, Value: h.KV[a.Collection][k]})
		}
		return map[string]any{"entries": entries, "next": next}, nil
	case "kv_count":
		return len(h.KV[a.Collection]), nil
	case "settings_get":
		s := h.Settings[a.Page]
		if s == nil {
			s = map[string]any{}
		}
		return s, nil
	case "media_get":
		out := []gp.Media{}
		for _, id := range a.IDs {
			if m, ok := h.Media[id]; ok {
				out = append(out, m)
			}
		}
		return out, nil
	case "comment_get":
		c, ok := h.Comments[a.ID]
		if !ok {
			return nil, errors.New("not found")
		}
		return c, nil
	case "comment_set_status":
		h.Statuses[a.ID] = a.Status
		return true, nil
	case "stash_list":
		m := h.Stash[a.Kind]
		if m == nil {
			m = map[string]json.RawMessage{}
		}
		return m, nil
	case "http_request":
		var r gp.HTTPRequest
		json.Unmarshal(raw, &r)
		h.Requests = append(h.Requests, r)
		if h.Fetch == nil {
			return nil, errors.New("request failed: no network in tests")
		}
		fetch := h.Fetch
		h.mu.Unlock()
		resp, err := fetch(r)
		h.mu.Lock()
		if err != nil {
			return nil, err
		}
		return resp, nil
	case "mail_admins":
		h.Mails = append(h.Mails, Mail{a.Subject, a.Body})
		return true, nil
	case "notify_admins":
		h.Notices = append(h.Notices, Notice{a.Text, a.Kind, a.Capability})
		return true, nil
	case "live_publish":
		h.Events = append(h.Events, Event{a.Page, a.Data})
		return true, nil
	case "progress":
		h.Progress = append(h.Progress, a.Percent)
		return true, nil
	case "settings_set":
		var r struct {
			Page   string         `json:"page"`
			Values map[string]any `json:"values"`
		}
		json.Unmarshal(raw, &r)
		if h.Settings[r.Page] == nil {
			h.Settings[r.Page] = map[string]any{}
		}
		for k, v := range r.Values {
			h.Settings[r.Page][k] = v
		}
		return true, nil
	case "templates":
		h.templateVersion++
		return map[string]any{"version": fmt.Sprint(h.templateVersion), "files": h.Templates}, nil
	case "invalidate":
		h.Invalidated++
		return true, nil
	}
	return nil, fmt.Errorf("fakehost: unknown operation %q", op)
}
