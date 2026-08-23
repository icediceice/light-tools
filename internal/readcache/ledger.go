package readcache

import (
	"container/list"
	"sync"
	"time"
)

type key struct {
	epoch string
	path  string
	hash  string
}

type entry struct {
	key     key
	expires time.Time
}

type Ledger struct {
	mu       sync.Mutex
	ttl      time.Duration
	capacity int
	items    map[key]*list.Element
	order    *list.List
}

func New(ttl time.Duration, capacity int) *Ledger {
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	if capacity <= 0 {
		capacity = 512
	}
	return &Ledger{ttl: ttl, capacity: capacity, items: make(map[key]*list.Element), order: list.New()}
}

// ShouldElide is disabled when contextEpoch is empty. Otherwise it records the
// content-hash key and returns true only for a live prior observation.
func (l *Ledger) ShouldElide(contextEpoch, path, hash string, force bool) bool {
	if contextEpoch == "" {
		return false
	}
	now := time.Now()
	itemKey := key{epoch: contextEpoch, path: path, hash: hash}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.expire(now)
	if element, ok := l.items[itemKey]; ok {
		element.Value.(*entry).expires = now.Add(l.ttl)
		l.order.MoveToFront(element)
		return !force
	}
	element := l.order.PushFront(&entry{key: itemKey, expires: now.Add(l.ttl)})
	l.items[itemKey] = element
	for l.order.Len() > l.capacity {
		l.remove(l.order.Back())
	}
	return false
}

func (l *Ledger) Invalidate(path string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for itemKey, element := range l.items {
		if itemKey.path == path {
			l.remove(element)
		}
	}
}

func (l *Ledger) expire(now time.Time) {
	for element := l.order.Back(); element != nil; {
		previous := element.Prev()
		if element.Value.(*entry).expires.After(now) {
			break
		}
		l.remove(element)
		element = previous
	}
}

func (l *Ledger) remove(element *list.Element) {
	if element == nil {
		return
	}
	delete(l.items, element.Value.(*entry).key)
	l.order.Remove(element)
}
