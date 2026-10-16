package datalog

import (
	"container/list"
	"sync"

	"github.com/panyam/jaala/ns"
)

// lookupCache keeps what a LookupSource returned across Evals (#212), so a host answering many
// questions over one Base asks its store once for each pattern and values rather than once per
// question. It is a tier between the Eval's own cache (evalRun.looked), which holds everything the
// Eval read for as long as it runs, and the store: bounded in tuples, least recently used dropped
// first, and dropped whole when a Versioned Source's version moves or the host calls Forget.
//
// Behind a pointer on Base, as derivedCache is, so every Eval's copy shares one cache. A result's
// slice is never changed once stored.
type lookupCache struct {
	mu      sync.Mutex
	gen     int64 // bumped whenever the entries are dropped, so an Eval started before can't store
	limit   int   // the most tuples kept in all; 0 keeps none
	size    int
	entries map[string]*list.Element
	lru     *list.List // of *lookupEntry, most recently used first
}

type lookupEntry struct {
	key  string
	rows []ns.Tuple
}

// defaultLookupLimit is how many looked-up tuples a Base keeps unless LimitLookupCache says otherwise.
const defaultLookupLimit = 1 << 20

func newLookupCache() *lookupCache {
	return &lookupCache{limit: defaultLookupLimit, entries: map[string]*list.Element{}, lru: list.New()}
}

// LimitLookupCache sets how many tuples a LookupSource returned, across all lookups, the Base keeps for
// later queries (#212); 0 keeps none, so every query asks the Source again. A result that matched
// nothing counts as one, since knowing a value has no facts saves a call too. When a result would
// overflow the limit, the ones used least recently are dropped first, and one larger than the limit is
// never kept. It applies to the Base and every copy of it, and is safe to call while queries run. A
// query keeps whatever it looked up until it finishes, whatever the limit.
func (b *Base) LimitLookupCache(tuples int) {
	c := b.lookups
	if c == nil {
		return
	}
	b.versions.each(func(vb *Base) { vb.LimitLookupCache(tuples) })
	c.mu.Lock()
	defer c.mu.Unlock()
	c.limit = max(tuples, 0)
	c.evict(0)
}

// generation is the generation an Eval starting now may store into.
func (c *lookupCache) generation() int64 {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.gen
}

func (c *lookupCache) drop() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen++
	c.entries = map[string]*list.Element{}
	c.lru.Init()
	c.size = 0
}

// get returns a kept result, marking it used.
func (c *lookupCache) get(key string) ([]ns.Tuple, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	c.lru.MoveToFront(el)
	return el.Value.(*lookupEntry).rows, true
}

// put keeps a result an Eval fetched, unless the entries were dropped since that Eval began (gen), or
// it is already kept, as when two queries looked it up at once.
func (c *lookupCache) put(gen int64, key string, rows []ns.Tuple) {
	if c == nil {
		return
	}
	n := max(len(rows), 1)
	c.mu.Lock()
	defer c.mu.Unlock()
	if gen != c.gen || n > c.limit {
		return
	}
	if _, ok := c.entries[key]; ok {
		return
	}
	c.evict(n)
	c.entries[key] = c.lru.PushFront(&lookupEntry{key: key, rows: rows[:len(rows):len(rows)]})
	c.size += n
}

// evict drops the least recently used results until room more tuples fit under the limit.
func (c *lookupCache) evict(room int) {
	for c.size+room > c.limit && c.lru.Len() > 0 {
		el := c.lru.Back()
		e := el.Value.(*lookupEntry)
		c.lru.Remove(el)
		delete(c.entries, e.key)
		c.size -= max(len(e.rows), 1)
	}
}
