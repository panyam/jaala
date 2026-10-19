package datalog

import (
	"cmp"
	"container/list"
	"sort"
	"strings"
	"sync"

	"github.com/panyam/jaala/ns"
)

// derivedCache keeps the derived relations SemiNaive evaluated in full, so a later query over the
// same Base reads them instead of deriving them again (#140). A host that keeps one Base per design
// and answers many queries over it otherwise re-derives the same library relations on every one.
//
// An entry is keyed by its relation's rules and the rules of every derived relation they read (see
// derivedKeys), so a query that defines a relation differently derives its own. Only relations
// evaluated in full are kept: a relation derived under demand holds only what one query asked for, and
// the rewrite gives it a name no query can spell, which is how it is told apart. Like the base
// relations' tuples, the entries assume the Source's facts are fixed; a Versioned Source whose version
// moves, or Forget, drops them (see refresh).
//
// Behind a pointer on Base, as edbCache is, so every Eval's copy shares one cache. Tuple slices are
// never changed once stored, and a query reads one capped at its length, so an append copies.
type derivedCache struct {
	mu      sync.Mutex
	gen     int64 // bumped whenever the entries are dropped, so an Eval started before can't store
	version string
	limit   int // the most tuples kept in all; 0 keeps none
	size    int
	entries map[string]*list.Element
	lru     *list.List // of *derivedEntry, most recently used first
	// store is the host's tier behind this one, or nil (see KeepDerivedIn).
	store ns.DerivedStore
}

type derivedEntry struct {
	key    string
	arity  int
	tuples []idbTuple
	stored bool // the store holds it too, so an Eval reading it from memory needn't write it there
}

// defaultDerivedLimit is how many derived tuples a Base keeps unless LimitDerivedCache says otherwise.
const defaultDerivedLimit = 1 << 20

func newDerivedCache() *derivedCache {
	return &derivedCache{limit: defaultDerivedLimit, entries: map[string]*list.Element{}, lru: list.New()}
}

// LimitDerivedCache sets how many derived tuples, across all relations, the Base keeps for later
// queries (#140); 0 keeps none. A relation larger than the limit is never kept, and when one would
// overflow it, the relations used least recently are dropped first. It applies to the Base and every
// copy of it, such as Unindexed's, and is safe to call while queries run.
func (b *Base) LimitDerivedCache(tuples int) {
	if b.derived == nil {
		return
	}
	b.versions.each(func(vb *Base) { vb.LimitDerivedCache(tuples) })
	c := b.derived
	c.mu.Lock()
	defer c.mu.Unlock()
	c.limit = max(tuples, 0)
	c.evict(0)
}

// KeepDerivedIn keeps the derived relations the Base evaluates in full in s too, behind its own
// memory, filed by the relation's rules and the Source's version (#220), so another Base over the same
// facts, in this process or another, reads them from s instead of deriving them. Only a Versioned
// Source's relations go to s, and s is read when the Base's memory misses, so LimitDerivedCache(0)
// leaves s as the only tier. A nil s stops using one. It applies to the Base and every copy of it, and
// is safe to call while queries run. See ns.DerivedStore for what s must do.
func (b *Base) KeepDerivedIn(s ns.DerivedStore) {
	if b.derived == nil {
		return
	}
	b.versions.each(func(vb *Base) { vb.KeepDerivedIn(s) })
	c := b.derived
	c.mu.Lock()
	defer c.mu.Unlock()
	c.store = s
	for el := c.lru.Front(); el != nil; el = el.Next() {
		el.Value.(*derivedEntry).stored = false
	}
}

// Forget drops everything the Base has cached: base relations' tuples and indexes, the derived
// relations kept for later queries, and what the Source's Lookup returned (#212). The next query reads the Source again. A host calls it after
// changing a Source's facts in place; a Source that reports a Version doesn't need it. A query running
// meanwhile finishes on what it already read, and keeps nothing it derived.
func (b *Base) Forget() {
	if b.edb != nil {
		b.edb.reset()
	}
	if b.derived != nil {
		b.derived.drop()
	}
	b.lookups.drop()
	b.versions.drop()
}

// refresh reads a Versioned Source's version, for an Eval about to start, and drops every cache when
// it has moved since the last Eval. It returns the cache generation the Eval may store into, and the
// version the Eval reads (versioned false for a Source that has none).
func (b *Base) refresh() (gen int64, version string, versioned bool) {
	if b.derived == nil {
		return 0, "", false
	}
	c := b.derived
	vs := b.versioned
	if vs == nil {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.gen, "", false
	}
	v := vs.Version()
	c.mu.Lock()
	defer c.mu.Unlock()
	if v != c.version {
		c.version = v
		c.dropLocked()
		if b.edb != nil {
			b.edb.reset()
		}
		b.lookups.drop()
		b.versions.drop()
	}
	return c.gen, v, true
}

func (c *derivedCache) drop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.dropLocked()
}

func (c *derivedCache) dropLocked() {
	c.gen++
	c.entries = map[string]*list.Element{}
	c.lru.Init()
	c.size = 0
}

// get returns a kept relation, marking it used.
func (c *derivedCache) get(key string) (*derivedEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	c.lru.MoveToFront(el)
	return el.Value.(*derivedEntry), true
}

// put keeps a relation an Eval derived in full, unless the entries were dropped since that Eval began
// (gen), or it is already kept, as when two queries derived it at once.
func (c *derivedCache) put(gen int64, key string, arity int, tuples []idbTuple) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.putLocked(gen, key, arity, tuples, false)
}

func (c *derivedCache) putLocked(gen int64, key string, arity int, tuples []idbTuple, stored bool) {
	if gen != c.gen || c.limit == 0 || len(tuples) > c.limit {
		return
	}
	if _, ok := c.entries[key]; ok {
		return
	}
	c.evict(len(tuples))
	c.entries[key] = c.lru.PushFront(&derivedEntry{key: key, arity: arity, tuples: tuples[:len(tuples):len(tuples)], stored: stored})
	c.size += len(tuples)
}

// storeFor is the store an Eval of generation gen may read or write, or nil when there is none or
// the entries were dropped since it began.
func (c *derivedCache) storeFor(gen int64) ns.DerivedStore {
	c.mu.Lock()
	defer c.mu.Unlock()
	if gen != c.gen {
		return nil
	}
	return c.store
}

// stored marks a kept relation as held by the store too.
func (c *derivedCache) markStored(gen int64, key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.entries[key]; ok && gen == c.gen {
		el.Value.(*derivedEntry).stored = true
	}
}

// isStored reports whether a kept relation is held by the store too.
func (c *derivedCache) isStored(e *derivedEntry) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return e.stored
}

// evict drops the least recently used relations until room more tuples fit under the limit.
func (c *derivedCache) evict(room int) {
	for c.size+room > c.limit && c.lru.Len() > 0 {
		el := c.lru.Back()
		e := el.Value.(*derivedEntry)
		c.lru.Remove(el)
		delete(c.entries, e.key)
		c.size -= len(e.tuples)
	}
}

// derivedKeys is the cache key of each relation the linked rules derive: the text of its rules and of
// every derived relation they read, directly or through others, sorted, so a change anywhere below it
// is a different key. The text rather than a hash of it, since a hash would bring crypto's packages
// into every host's build, and a library's rules are a few kilobytes. A relation whose rules reach a Volatile predicate has no key, nor does one whose name
// a rewrite or a Bind made (a NUL in it), since its rules are this query's alone.
func derivedKeys(b *Base, rules []Rule) map[string]string {
	byHead := map[string][]Rule{}
	for _, r := range rules {
		byHead[r.Head.Relation] = append(byHead[r.Head.Relation], r)
	}
	keys := map[string]string{}
	for rel := range byHead {
		if strings.Contains(rel, "\x00") {
			continue
		}
		var text []string
		volatile := false
		seen := map[string]bool{rel: true}
		stack := []string{rel}
		for len(stack) > 0 && !volatile {
			n := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			for _, r := range byHead[n] {
				text = append(text, r.String())
				for _, a := range ruleAtoms(r.Body) {
					if bi, ok := b.reg.Predicate(a.Relation); ok && bi.Volatile {
						volatile = true
					}
					for _, n := range relationsRead(a.Relation) {
						if _, derived := byHead[n]; derived && !seen[n] {
							seen[n] = true
							stack = append(stack, n)
						}
					}
				}
			}
		}
		if volatile {
			continue
		}
		sort.Strings(text)
		keys[rel] = rel + "\n" + strings.Join(text, "\n")
	}
	return keys
}

// reuseDerived is SemiNaive's first rewrite: each relation the Base already holds in full is read from
// there instead of derived (#140), or, when the Base's memory misses, from the host's DerivedStore
// (#220). Its rules are dropped, along with any only it read, and its tuples wait on the Eval to be
// installed beside what the query derives. Every other keyed relation is recorded, so the ones the
// query evaluates in full are kept once it has (see keepDerived). A relation with no rules is read in
// full wherever it is called, so demand never re-derives part of one the Base holds.
func reuseDerived(b *Base, q Query) Query {
	if b.derived == nil || b.run == nil || b.witnessing() {
		return q
	}
	keys := derivedKeys(b, q.Rules)
	held := map[string]bool{}
	var store ns.DerivedStore
	if b.run.versioned {
		store = b.derived.storeFor(b.run.gen)
	}
	for rel, key := range keys {
		e, ok := b.derived.get(key)
		if !ok && store != nil {
			e, ok = readStored(b, store, rel, key, q.Rules)
		}
		if ok {
			if b.run.preload == nil {
				b.run.preload = map[string]*derivedEntry{}
			}
			b.run.preload[rel] = e
			held[rel] = true
		}
	}
	b.run.keys = keys
	if len(held) == 0 {
		return q
	}
	kept := q.Rules[:0:0]
	for _, r := range q.Rules {
		if !held[r.Head.Relation] {
			kept = append(kept, r)
		}
	}
	q.Rules = kept
	return withoutUnreached(q)
}

// readStored reads rel from the host's store, keeping it in the Base's memory too. A failed read, or
// one holding a tuple of the wrong arity, is a miss, and the relation is derived.
func readStored(b *Base, store ns.DerivedStore, rel, key string, rules []Rule) (*derivedEntry, bool) {
	arity := -1
	for _, r := range rules {
		if r.Head.Relation == rel {
			arity = len(r.Head.Args)
			break
		}
	}
	got, ok, err := store.Derived(b.run.ctx, ns.DerivedKey{Relation: rel, Rules: key, Version: b.run.version})
	if err != nil || !ok {
		return nil, false
	}
	tuples := make([]idbTuple, len(got))
	for i, t := range got {
		if len(t.Vals) != arity {
			return nil, false
		}
		tuples[i] = idbTuple{vals: t.Vals, cites: t.Cites}
	}
	e := &derivedEntry{key: key, arity: arity, tuples: tuples, stored: true}
	c := b.derived
	c.mu.Lock()
	c.putLocked(b.run.gen, key, arity, tuples, true)
	c.mu.Unlock()
	return e, true
}

// installHeld puts the relations reuseDerived found held into a fresh Eval's derived relations.
func installHeld(b *Base) {
	for rel, e := range b.run.preload {
		b.idb[rel] = e.tuples[:len(e.tuples):len(e.tuples)]
		b.idbArity[rel] = e.arity
	}
}

// keepDerived keeps every keyed relation the query's rules evaluated in full: one still derived under
// its own name (a rewrite renames what it derives in part) and not read from the cache. With a host's
// store, it writes there each of them, and each relation read from the Base's memory that the store
// doesn't hold yet, returning the first error the store gave.
func keepDerived(b *Base, gen int64, rules []Rule) error {
	if b.run.keys == nil {
		return nil
	}
	var store ns.DerivedStore
	if b.run.versioned {
		store = b.derived.storeFor(gen)
	}
	var first error
	write := func(rel, key string, tuples []idbTuple) {
		if b.derived.storeFor(gen) == nil {
			return // the entries were dropped meanwhile, so what this Eval read is not the version's
		}
		out := make([]ns.Tuple, len(tuples))
		for i, t := range tuples {
			out[i] = ns.Tuple{Vals: t.vals, Cites: t.cites}
		}
		if err := store.KeepDerived(b.run.ctx, ns.DerivedKey{Relation: rel, Rules: key, Version: b.run.version}, out); err != nil {
			first = cmp.Or(first, err)
			return
		}
		b.derived.markStored(gen, key)
	}
	done := map[string]bool{}
	for _, r := range rules {
		rel := r.Head.Relation
		key, ok := b.run.keys[rel]
		if !ok || done[rel] {
			continue
		}
		done[rel] = true
		b.derived.put(gen, key, b.idbArity[rel], b.idb[rel])
		if store != nil {
			write(rel, key, b.idb[rel])
		}
	}
	if store == nil {
		return first
	}
	rels := make([]string, 0, len(b.run.preload))
	for rel := range b.run.preload {
		rels = append(rels, rel)
	}
	sort.Strings(rels)
	for _, rel := range rels {
		if e := b.run.preload[rel]; !b.derived.isStored(e) {
			write(rel, e.key, e.tuples)
		}
	}
	return first
}
