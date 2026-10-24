package auth

import (
	"sync"
	"time"
)

// ceremonyTTL bounds how long a started WebAuthn ceremony stays valid.
const ceremonyTTL = 5 * time.Minute

// ceremonyStore keeps the server side state (challenge) of WebAuthn
// ceremonies in memory. Each entry can be taken exactly once.
type ceremonyStore struct {
	mu sync.Mutex
	m  map[string]ceremony
}

type ceremony struct {
	data    any
	expires time.Time
}

func newCeremonyStore() *ceremonyStore { return &ceremonyStore{m: map[string]ceremony{}} }

func (c *ceremonyStore) put(key string, data any, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.m) > 10000 {
		c.pruneLocked(now)
	}
	c.m[key] = ceremony{data: data, expires: now.Add(ceremonyTTL)}
}

// take returns and removes the entry of key if it has not expired.
func (c *ceremonyStore) take(key string, now time.Time) (any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[key]
	delete(c.m, key)
	if !ok || now.After(e.expires) {
		return nil, false
	}
	return e.data, true
}

func (c *ceremonyStore) prune(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pruneLocked(now)
}

func (c *ceremonyStore) pruneLocked(now time.Time) {
	for k, e := range c.m {
		if now.After(e.expires) {
			delete(c.m, k)
		}
	}
}
