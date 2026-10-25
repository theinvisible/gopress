package live

import (
	"strings"
	"testing"
	"time"

	"github.com/theinvisible/gopress/internal/auth"
	"github.com/theinvisible/gopress/internal/db"
)

func recv(s *Sub) string {
	select {
	case m := <-s.C:
		return string(m)
	default:
		return ""
	}
}

func TestHubFilters(t *testing.T) {
	h := NewHub()
	admin := &db.User{ID: 1, Role: auth.RoleAdministrator}
	author := &db.User{ID: 2, Role: auth.RoleAuthor}
	sa, _ := h.Subscribe(admin, []string{PostTopic(5)})
	sb, _ := h.Subscribe(author, nil)

	h.Publish(Event{Type: "comment.pending", Capability: auth.CapModerateComments, Data: map[string]int{"pending": 3}})
	if got := recv(sa); got != "event: comment.pending\ndata: {\"pending\":3}\n\n" {
		t.Errorf("admin: %q", got)
	}
	if got := recv(sb); got != "" {
		t.Errorf("author got a moderation event: %q", got)
	}
	h.Publish(Event{Type: "post.saved", Topic: PostTopic(5), ExceptUser: 1, Data: 1})
	h.Publish(Event{Type: "post.saved", Topic: PostTopic(5), Data: 2})
	if got := recv(sa); !strings.Contains(got, "data: 2") {
		t.Errorf("topic: %q", got)
	}
	if recv(sb) != "" || recv(sa) != "" {
		t.Error("topic or except-user ignored")
	}
	h.Publish(Event{Type: "notice", UserID: 2, Data: "x"})
	if recv(sa) != "" || recv(sb) == "" {
		t.Error("user filter")
	}
	// Event names go into the stream unencoded.
	h.Publish(Event{Type: "x\ndata: evil", Data: 1})
	if recv(sa) != "" {
		t.Error("invalid event type published")
	}
	// A role change while connected takes effect.
	sb.SetUser(&db.User{ID: 2, Role: auth.RoleEditor})
	h.Publish(Event{Type: "comment.pending", Capability: auth.CapModerateComments, Data: 1})
	if recv(sb) == "" {
		t.Error("new role not applied")
	}
}

func TestHubLimits(t *testing.T) {
	h := NewHub()
	u := &db.User{ID: 1, Role: auth.RoleAdministrator}
	var subs []*Sub
	for range MaxStreamsPerUser {
		s, err := h.Subscribe(u, nil)
		if err != nil {
			t.Fatal(err)
		}
		subs = append(subs, s)
	}
	if _, err := h.Subscribe(u, nil); err != ErrTooMany {
		t.Errorf("sixth stream: %v", err)
	}
	h.Unsubscribe(subs[0])
	h.Unsubscribe(subs[0]) // twice is harmless
	if _, err := h.Subscribe(u, nil); err != nil {
		t.Errorf("after unsubscribe: %v", err)
	}
	// A slow subscriber loses events instead of blocking the hub.
	s := subs[1]
	for range queueSize + 10 {
		h.Publish(Event{Type: "notice", Data: 1})
	}
	if !s.TakeMissed() || s.TakeMissed() {
		t.Error("missed events not reported once")
	}
}

func TestPresence(t *testing.T) {
	h := NewHub()
	p := NewPresence(h)
	now := time.Now()
	p.now = func() time.Time { return now }
	ann := &db.User{ID: 1, DisplayName: "Ann", Role: auth.RoleEditor}
	bob := &db.User{ID: 2, DisplayName: "Bob", Role: auth.RoleEditor}
	sa, _ := h.Subscribe(ann, []string{PostTopic(7)})
	if others := p.Beat(7, ann, true); len(others) != 0 {
		t.Errorf("alone: %v", others)
	}
	recv(sa)
	if others := p.Beat(7, bob, true); len(others) != 1 || others[0].Name != "Ann" {
		t.Errorf("bob sees: %v", others)
	}
	if got := recv(sa); !strings.Contains(got, `"name":"Bob"`) {
		t.Errorf("ann not told: %q", got)
	}
	p.Beat(7, bob, true) // no change, no event
	if got := recv(sa); got != "" {
		t.Errorf("event without change: %q", got)
	}
	now = now.Add(PresenceTTL + time.Second)
	if others := p.Beat(7, ann, true); len(others) != 0 {
		t.Errorf("stale editor still listed: %v", others)
	}
	p.Beat(7, ann, false)
	if len(p.posts) != 0 {
		t.Errorf("posts kept: %v", p.posts)
	}
}
