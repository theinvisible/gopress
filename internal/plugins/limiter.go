package plugins

import (
	"sync"
	"time"
)

// limiter is a sliding-window counter with a bounded number of keys:
// expired keys are pruned at most once a minute; while it is full of live
// keys, new keys are refused.
type limiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	hits   map[string][]time.Time
	pruned time.Time
}

const limiterKeys = 50000

func newLimiter(max int, window time.Duration) *limiter {
	return &limiter{max: max, window: window, hits: map[string][]time.Time{}}
}

func (l *limiter) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	ts, known := l.hits[key]
	if !known && len(l.hits) >= limiterKeys {
		if now.Sub(l.pruned) < time.Minute {
			return false
		}
		l.pruned = now
		for k, kt := range l.hits {
			if len(kt) == 0 || now.Sub(kt[len(kt)-1]) > l.window {
				delete(l.hits, k)
			}
		}
		if len(l.hits) >= limiterKeys {
			return false
		}
	}
	i := 0
	for i < len(ts) && now.Sub(ts[i]) > l.window {
		i++
	}
	ts = ts[i:]
	if len(ts) >= l.max {
		l.hits[key] = ts
		return false
	}
	l.hits[key] = append(ts, now)
	return true
}
