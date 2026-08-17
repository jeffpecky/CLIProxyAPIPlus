package tokensaver

import (
	"container/list"
	"encoding/json"
	"sync"
	"time"
)

const (
	headroomPrefixCacheEntries = 256
	headroomPrefixCacheTTL     = 30 * time.Minute
)

type headroomPrefixEntry struct {
	scope     headroomPrefixScope
	source    []any
	forwarded []any
	expires   time.Time
}

type headroomPrefixScope struct {
	Session  string
	Format   string
	Model    string
	Endpoint string
	Config   string
}

func (s headroomPrefixScope) key() string {
	raw, _ := json.Marshal(s)
	return string(raw)
}

type headroomPrefixCache struct {
	mu      sync.Mutex
	max     int
	ttl     time.Duration
	now     func() time.Time
	entries map[string]*list.Element
	lru     *list.List
}

var defaultHeadroomPrefixCache = newHeadroomPrefixCache(headroomPrefixCacheEntries, headroomPrefixCacheTTL, time.Now)

func newHeadroomPrefixCache(max int, ttl time.Duration, now func() time.Time) *headroomPrefixCache {
	return &headroomPrefixCache{max: max, ttl: ttl, now: now, entries: make(map[string]*list.Element), lru: list.New()}
}

func (c *headroomPrefixCache) reuse(scope headroomPrefixScope, messages []any) ([]any, int) {
	if c == nil || scope.Session == "" {
		return messages, 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	element := c.entries[scope.key()]
	if element == nil {
		return messages, 0
	}
	entry := element.Value.(*headroomPrefixEntry)
	if !c.now().Before(entry.expires) {
		c.remove(element)
		return messages, 0
	}
	if len(messages) < len(entry.source) || !jsonEqual(messages[:len(entry.source)], entry.source) {
		return messages, 0
	}
	c.lru.MoveToFront(element)
	next := append(cloneMessages(entry.forwarded), messages[len(entry.source):]...)
	return next, len(entry.forwarded)
}

func (c *headroomPrefixCache) store(scope headroomPrefixScope, source, forwarded []any) {
	if c == nil || scope.Session == "" || len(source) == 0 || len(forwarded) == 0 || c.max <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	key := scope.key()
	if existing := c.entries[key]; existing != nil {
		c.remove(existing)
	}
	entry := &headroomPrefixEntry{scope: scope, source: cloneMessages(source), forwarded: cloneMessages(forwarded), expires: c.now().Add(c.ttl)}
	c.entries[key] = c.lru.PushFront(entry)
	for c.lru.Len() > c.max {
		c.remove(c.lru.Back())
	}
}

func (c *headroomPrefixCache) remove(element *list.Element) {
	if element == nil {
		return
	}
	delete(c.entries, element.Value.(*headroomPrefixEntry).scope.key())
	c.lru.Remove(element)
}

func cloneMessages(messages []any) []any {
	raw, err := json.Marshal(messages)
	if err != nil {
		return append([]any(nil), messages...)
	}
	var cloned []any
	if json.Unmarshal(raw, &cloned) != nil {
		return append([]any(nil), messages...)
	}
	return cloned
}

func jsonEqual(a, b any) bool {
	aRaw, errA := json.Marshal(a)
	bRaw, errB := json.Marshal(b)
	return errA == nil && errB == nil && string(aRaw) == string(bRaw)
}
