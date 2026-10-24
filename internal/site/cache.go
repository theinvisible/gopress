package site

import (
	"container/list"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

// pageCache is an in-memory LRU cache of rendered responses for anonymous
// visitors. Entries belong to a content generation; a new generation drops
// everything.
type pageCache struct {
	max int64

	mu    sync.Mutex
	gen   uint64
	size  int64
	lru   *list.List // front = most recent; values are *cacheItem
	items map[string]*list.Element
}

type cacheItem struct {
	key string
	enc *encoded
}

func newPageCache(max int64) *pageCache {
	return &pageCache{max: max, lru: list.New(), items: map[string]*list.Element{}}
}

// cacheParams are the query parameters that change a response; all others
// (tracking parameters, cache busters) are ignored for the cache key, so
// random query strings cannot flood the cache.
var cacheParams = []string{"s", "paged", "replytocom", "feed", "p", "page_id", "attachment_id", "cat", "tag", "author", "m", "post"}

func cacheKey(r *http.Request) string {
	if r.URL.RawQuery == "" {
		return r.URL.Path
	}
	q := r.URL.Query()
	var b strings.Builder
	b.WriteString(r.URL.Path)
	for _, k := range cacheParams {
		if v, ok := q[k]; ok {
			b.WriteString("?" + k + "=" + url.QueryEscape(strings.Join(v, ",")))
		}
	}
	return b.String()
}

// reset drops all entries if gen is newer than the cached generation.
func (c *pageCache) reset(gen uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if gen != c.gen {
		c.gen = gen
		c.size = 0
		c.lru.Init()
		c.items = map[string]*list.Element{}
	}
}

// serve answers r from the cache and reports whether it did.
func (c *pageCache) serve(w http.ResponseWriter, r *http.Request, gen uint64) bool {
	if c.max <= 0 {
		return false
	}
	c.mu.Lock()
	el, ok := c.items[cacheKey(r)]
	if !ok || c.gen != gen {
		c.mu.Unlock()
		return false
	}
	c.lru.MoveToFront(el)
	enc := el.Value.(*cacheItem).enc
	c.mu.Unlock()
	w.Header().Set("X-Cache", "HIT")
	enc.serve(w, r)
	return true
}

// store adds a response; it compresses outside the lock.
func (c *pageCache) store(r *http.Request, gen uint64, resp *response) {
	if c.max <= 0 {
		return
	}
	enc := encode(resp)
	if enc.totalSize > c.max/8 {
		return
	}
	key := cacheKey(r)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.gen != gen {
		return
	}
	if el, ok := c.items[key]; ok {
		c.size -= el.Value.(*cacheItem).enc.totalSize
		c.lru.Remove(el)
	}
	c.items[key] = c.lru.PushFront(&cacheItem{key: key, enc: enc})
	c.size += enc.totalSize
	for c.size > c.max {
		el := c.lru.Back()
		it := el.Value.(*cacheItem)
		c.lru.Remove(el)
		delete(c.items, it.key)
		c.size -= it.enc.totalSize
	}
}
