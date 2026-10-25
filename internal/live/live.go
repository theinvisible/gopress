// Package live delivers events to open admin pages over Server-Sent
// Events: notices (a new comment awaiting moderation, a new donation),
// who else is editing a post, progress of long tasks, and plugin events
// for plugin pages.
//
// Every event names who may receive it (a capability, a user, a topic
// such as "post:12"), and the hub checks it against each subscriber's
// user. Topics are authorized when a stream subscribes to them. A
// subscriber that does not keep up loses events and is told to resync;
// the hub never queues without bound.
package live

import (
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/theinvisible/gopress/internal/auth"
	"github.com/theinvisible/gopress/internal/db"
)

// Event is a message for open admin pages.
type Event struct {
	Type string // e.g. "comment.pending", "notice", "post.presence"
	Data any    // encoded as JSON

	// Receivers: all of these must match.
	Capability string // the user needs it ("" = any user of the admin)
	UserID     int64  // only this user (0 = any)
	ExceptUser int64  // not this user (0 = none)
	Topic      string // only streams subscribed to it ("" = all)
}

// Limits.
const (
	MaxStreams        = 500 // in total
	MaxStreamsPerUser = 5
	queueSize         = 32
	maxEventBytes     = 64 << 10
)

// ErrTooMany is returned when a user or the site has too many streams.
var ErrTooMany = errors.New("too many live connections")

// Hub distributes events to subscribers.
type Hub struct {
	mu      sync.Mutex
	subs    map[*Sub]struct{}
	perUser map[int64]int
}

// NewHub returns an empty hub.
func NewHub() *Hub { return &Hub{subs: map[*Sub]struct{}{}, perUser: map[int64]int{}} }

// Sub is one open stream.
type Sub struct {
	// C receives encoded events ("event: …\ndata: …\n\n").
	C      chan []byte
	topics map[string]bool

	mu     sync.Mutex
	user   *db.User
	missed bool // events were dropped: the client should resync
}

// SetUser updates the subscriber's user (role changes while connected).
func (s *Sub) SetUser(u *db.User) {
	s.mu.Lock()
	s.user = u
	s.mu.Unlock()
}

// TakeMissed reports (and resets) whether events were dropped.
func (s *Sub) TakeMissed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.missed
	s.missed = false
	return m
}

// Subscribe opens a subscription for u with authorized topics.
func (h *Hub) Subscribe(u *db.User, topics []string) (*Sub, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.subs) >= MaxStreams || h.perUser[u.ID] >= MaxStreamsPerUser {
		return nil, ErrTooMany
	}
	s := &Sub{C: make(chan []byte, queueSize), topics: map[string]bool{}, user: u}
	for _, t := range topics {
		s.topics[t] = true
	}
	h.subs[s] = struct{}{}
	h.perUser[u.ID]++
	return s, nil
}

// Unsubscribe closes s.
func (h *Hub) Unsubscribe(s *Sub) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.subs[s]; !ok {
		return
	}
	delete(h.subs, s)
	s.mu.Lock()
	id := s.user.ID
	s.mu.Unlock()
	if h.perUser[id]--; h.perUser[id] <= 0 {
		delete(h.perUser, id)
	}
}

// Publish sends e to every subscriber allowed to receive it. It never
// blocks: a full queue drops the event for that subscriber.
func (h *Hub) Publish(e Event) {
	data, err := json.Marshal(e.Data)
	if err != nil || len(data) > maxEventBytes || !validType(e.Type) {
		return
	}
	msg := []byte("event: " + e.Type + "\ndata: " + string(data) + "\n\n")
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.subs {
		if !s.allowed(&e) {
			continue
		}
		select {
		case s.C <- msg:
		default:
			s.mu.Lock()
			s.missed = true
			s.mu.Unlock()
		}
	}
}

func (s *Sub) allowed(e *Event) bool {
	s.mu.Lock()
	u := s.user
	s.mu.Unlock()
	switch {
	case e.UserID != 0 && u.ID != e.UserID, e.ExceptUser != 0 && u.ID == e.ExceptUser:
		return false
	case e.Topic != "" && !s.topics[e.Topic]:
		return false
	case e.Capability != "" && !auth.Can(u, e.Capability):
		return false
	}
	return true
}

// Count returns the number of open streams.
func (h *Hub) Count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}

// validType: event names are lower-case words with dots (they go into
// the stream unencoded).
func validType(t string) bool {
	if t == "" || len(t) > 40 {
		return false
	}
	for _, r := range t {
		if (r < 'a' || r > 'z') && r != '.' && r != '_' {
			return false
		}
	}
	return true
}

// Editor is a user who has a post open in the editor.
type Editor struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// PresenceTTL is how long an editor counts as present after a heartbeat.
const PresenceTTL = 75 * time.Second

// Presence tracks who edits which post and tells the post's topic when
// that changes.
type Presence struct {
	hub *Hub
	now func() time.Time

	mu    sync.Mutex
	posts map[int64]map[int64]presenceEntry
}

type presenceEntry struct {
	name string
	seen time.Time
}

// NewPresence returns a tracker that publishes to hub.
func NewPresence(hub *Hub) *Presence {
	return &Presence{hub: hub, now: time.Now, posts: map[int64]map[int64]presenceEntry{}}
}

// PluginTopic is the topic of a plugin admin page (live_publish).
func PluginTopic(plugin, page string) string { return "plugin:" + plugin + ":" + page }

// PostTopic is the topic of a post's editors.
func PostTopic(id int64) string { return "post:" + strconv.FormatInt(id, 10) }

// Beat records that u has post id open (editing) or closed it, and
// returns the other editors.
func (p *Presence) Beat(id int64, u *db.User, editing bool) []Editor {
	p.mu.Lock()
	now := p.now()
	m := p.posts[id]
	before := p.list(m, 0, now)
	if editing {
		if m == nil {
			m = map[int64]presenceEntry{}
			p.posts[id] = m
		}
		m[u.ID] = presenceEntry{name: u.DisplayName, seen: now}
	} else if m != nil {
		delete(m, u.ID)
	}
	after := p.list(m, 0, now)
	if len(after) == 0 {
		delete(p.posts, id)
	}
	// Forget posts nobody edits any more.
	for pid, pm := range p.posts {
		if len(p.list(pm, 0, now)) == 0 {
			delete(p.posts, pid)
		}
	}
	others := p.list(m, u.ID, now)
	p.mu.Unlock()
	if !sameEditors(before, after) {
		p.hub.Publish(Event{Type: "post.presence", Topic: PostTopic(id), Data: map[string]any{"post": id, "editors": after}})
	}
	return others
}

func (p *Presence) list(m map[int64]presenceEntry, except int64, now time.Time) []Editor {
	out := []Editor{}
	for uid, e := range m {
		if uid != except && now.Sub(e.seen) < PresenceTTL {
			out = append(out, Editor{ID: uid, Name: e.name})
		}
	}
	slices.SortFunc(out, func(a, b Editor) int { return int(a.ID - b.ID) })
	return out
}

func sameEditors(a, b []Editor) bool {
	return slices.EqualFunc(a, b, func(x, y Editor) bool { return x.ID == y.ID })
}
