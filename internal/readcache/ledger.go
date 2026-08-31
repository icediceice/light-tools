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
	// delivered is the byte count of the response actually shipped for this
	// key, recorded once that response is fully materialized — for the read
	// lane, spill metadata included. Zero means "not recorded yet": a hit on
	// such an entry credits nothing rather than reconstructing a delivery it
	// cannot reproduce.
	delivered int
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

// ShouldElide is disabled when contextEpoch is empty. That is now the kill
// switch rather than the default: the filetool handler derives a per-process
// epoch when the client sends none, and only LIGHT_NO_READ_DEDUP (or an
// unseeded server) leaves it empty. Otherwise it records the content-hash key
// and returns true only for a live prior observation. A miss inserts the key
// with no delivery size; the caller records that size through RecordDelivery
// once the response it goes on to build is complete, so a later hit can
// credit exactly the bytes the prior response shipped.
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

// PriorDelivery reports the recorded delivery size for a live observation of
// this exact key without inserting, refreshing, or otherwise marking
// anything: it is the read side of RecordDelivery, consulted on a dedup hit
// to credit the bytes the prior response actually shipped. ok is false when
// no live entry exists or its response never finished recording, and the
// caller must credit nothing rather than guess.
func (l *Ledger) PriorDelivery(contextEpoch, path, hash string) (int, bool) {
	if contextEpoch == "" {
		return 0, false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.expire(time.Now())
	element, ok := l.items[key{epoch: contextEpoch, path: path, hash: hash}]
	if !ok {
		return 0, false
	}
	delivered := element.Value.(*entry).delivered
	return delivered, delivered > 0
}

// RecordDelivery upserts the delivered byte count for a key after the
// response for that observation has been fully materialized. Upsert rather
// than update: a capacity eviction between the miss and this call must not
// lose the only observation. It also refreshes the entry, mirroring the
// liveness a fresh observation would carry — a force:true re-read records
// its own, possibly different, delivery the same way.
func (l *Ledger) RecordDelivery(contextEpoch, path, hash string, delivered int) {
	if contextEpoch == "" || delivered <= 0 {
		return
	}
	now := time.Now()
	itemKey := key{epoch: contextEpoch, path: path, hash: hash}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.expire(now)
	if element, ok := l.items[itemKey]; ok {
		entry := element.Value.(*entry)
		entry.delivered = delivered
		entry.expires = now.Add(l.ttl)
		l.order.MoveToFront(element)
		return
	}
	element := l.order.PushFront(&entry{key: itemKey, expires: now.Add(l.ttl), delivered: delivered})
	l.items[itemKey] = element
	for l.order.Len() > l.capacity {
		l.remove(l.order.Back())
	}
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
