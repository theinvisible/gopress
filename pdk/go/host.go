// Package gopress is the plugin development kit for GoPress plugins
// written in Go. Build plugins with the standard Go toolchain:
//
//	GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o plugin.wasm .
//
// (or `gopress plugin-pack DIR`), register handlers in init() and call
// the host through the functions below. See docs/plugins.md.
package gopress

import (
	"encoding/json"
	"errors"

	"github.com/extism/go-pdk"
)

//go:wasmimport extism:host/user gopress
func hostCall(uint64) uint64

type hostResp struct {
	Result json.RawMessage `json:"result"`
	Error  string          `json:"error"`
}

// call runs a host operation and decodes its result into out (may be nil).
func call(op string, args any, out any) error {
	req, err := json.Marshal(map[string]any{"op": op, "args": args})
	if err != nil {
		return err
	}
	mem := pdk.AllocateBytes(req)
	defer mem.Free()
	off := hostCall(mem.Offset())
	if off == 0 {
		return errors.New("host call failed")
	}
	rm := pdk.FindMemory(off)
	b := rm.ReadBytes()
	rm.Free()
	var resp hostResp
	if err := json.Unmarshal(b, &resp); err != nil {
		return err
	}
	if resp.Error != "" {
		return errors.New(resp.Error)
	}
	if out != nil {
		return json.Unmarshal(resp.Result, out)
	}
	return nil
}

// Log writes a line to the server log (level: debug, info, warn, error).
func Log(level, msg string) { call("log", map[string]string{"level": level, "message": msg}, nil) }

// SiteInfo describes the site.
type SiteInfo struct {
	Name     string `json:"name"`
	Tagline  string `json:"tagline"`
	URL      string `json:"url"`
	Language string `json:"language"`
	TimeZone string `json:"timezone"`
}

// Site returns information about the site.
func Site() (SiteInfo, error) {
	var s SiteInfo
	err := call("site_info", nil, &s)
	return s, err
}

// KVGet reads a value of the plugin's storage into v; found is false if
// the key does not exist.
func KVGet(collection, key string, v any) (found bool, err error) {
	var r struct {
		Found bool            `json:"found"`
		Value json.RawMessage `json:"value"`
	}
	if err := call("kv_get", map[string]string{"collection": collection, "key": key}, &r); err != nil {
		return false, err
	}
	if !r.Found {
		return false, nil
	}
	return true, json.Unmarshal(r.Value, v)
}

// KVSet stores v (as JSON, at most 1 MB) under collection/key.
func KVSet(collection, key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return call("kv_set", map[string]any{"collection": collection, "key": key, "value": json.RawMessage(b)}, nil)
}

// KVDelete removes a key.
func KVDelete(collection, key string) error {
	return call("kv_delete", map[string]string{"collection": collection, "key": key}, nil)
}

// Entry is a stored key and its JSON value.
type Entry struct {
	Key   string          `json:"key"`
	Value json.RawMessage `json:"value"`
}

// KVList returns up to limit entries with keys after after (in key
// order) and the key to continue with ("" at the end).
func KVList(collection, after string, limit int) ([]Entry, string, error) {
	var r struct {
		Entries []Entry `json:"entries"`
		Next    string  `json:"next"`
	}
	err := call("kv_list", map[string]any{"collection": collection, "after": after, "limit": limit}, &r)
	return r.Entries, r.Next, err
}

// KVCount returns the number of entries of a collection.
func KVCount(collection string) (int, error) {
	var n int
	err := call("kv_count", map[string]string{"collection": collection}, &n)
	return n, err
}

// Settings returns the values of one of the plugin's settings pages.
func Settings(page string) (map[string]any, error) {
	var m map[string]any
	err := call("settings_get", map[string]string{"page": page}, &m)
	return m, err
}

// Post is a published post or page.
type Post struct {
	ID      int64  `json:"id"`
	Type    string `json:"type"`
	Title   string `json:"title"`
	Slug    string `json:"slug"`
	URL     string `json:"url"`
	Date    string `json:"date"`
	Excerpt string `json:"excerpt"`
	Content string `json:"content,omitempty"`
}

// Posts lists published posts (typ "post" or "page"); needs posts:read.
func Posts(typ, search string, limit, offset int) ([]Post, int, error) {
	var r struct {
		Posts []Post `json:"posts"`
		Total int    `json:"total"`
	}
	err := call("posts_query", map[string]any{"type": typ, "search": search, "limit": limit, "offset": offset}, &r)
	return r.Posts, r.Total, err
}

// GetPost returns a published post with its content; needs posts:read.
func GetPost(id int64) (Post, error) {
	var p Post
	err := call("post_get", map[string]int64{"id": id}, &p)
	return p, err
}

// Size is a generated image size.
type Size struct {
	URL    string `json:"url"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

// Media describes an attachment.
type Media struct {
	ID          int64           `json:"id"`
	URL         string          `json:"url"`
	Mime        string          `json:"mime"`
	Width       int             `json:"width"`
	Height      int             `json:"height"`
	Alt         string          `json:"alt"`
	Title       string          `json:"title"`
	Caption     string          `json:"caption"`
	Description string          `json:"description"`
	Sizes       map[string]Size `json:"sizes"`
}

// GetMedia describes attachments (of published posts or unattached);
// needs media:read. Unknown ids are left out.
func GetMedia(ids ...int64) ([]Media, error) {
	var m []Media
	err := call("media_get", map[string]any{"ids": ids}, &m)
	return m, err
}

// GetComment returns a comment; needs comments:read.
func GetComment(id int64) (Comment, error) {
	var c Comment
	err := call("comment_get", map[string]int64{"id": id}, &c)
	return c, err
}

// SetCommentStatus sets approve, hold, spam or trash; needs
// comments:moderate.
func SetCommentStatus(id int64, status string) error {
	return call("comment_set_status", map[string]any{"id": id, "status": status}, nil)
}

// HTTPRequest is an outgoing HTTPS request to a host of the manifest.
type HTTPRequest struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body,omitempty"`
}

// HTTPResponse is the answer (the body as text).
type HTTPResponse struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

// Fetch sends an HTTPS request to one of the hosts in the manifest.
func Fetch(r HTTPRequest) (HTTPResponse, error) {
	var resp HTTPResponse
	err := call("http_request", r, &resp)
	return resp, err
}

// Stash returns imported WordPress data of a kind declared as
// import:<kind> in the manifest (key → JSON).
func Stash(kind string) (map[string]json.RawMessage, error) {
	var m map[string]json.RawMessage
	err := call("stash_list", map[string]string{"kind": kind}, &m)
	return m, err
}

// Invalidate clears the page cache (after changing what the plugin
// renders).
func Invalidate() error { return call("invalidate", nil, nil) }
