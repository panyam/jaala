package datalog

import (
	"fmt"
	"github.com/panyam/jaala/ns"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// Fact lookup by binding pattern.
//
// The evaluator used to satisfy every atom by scanning its whole relation, so a two-atom
// conjunction was an unindexed nested loop: quadratic, and the shape of nearly every rule. These
// indexes turn the bound positions of an atom into a bucket lookup.
//
// Each value has one key (valueKey), and valueEq is key equality (#162), so a bucket holds exactly
// the tuples whose bound values equal the probe's. unify (EDB) and valsEqual (IDB) still decide each
// candidate, which costs a comparison and guards the index against a key that drifts from valueEq.

// patternMask marks which positional arguments of an atom are bound at solve time — a constant, or
// a variable the binding already carries. Wildcards and unbound variables are free. Positions past
// the width of the mask are never indexed on (see maskWidth), which only ever widens a bucket.
type patternMask uint64

// maskWidth is the number of argument positions a patternMask can mark. An argument beyond it is
// treated as free for indexing purposes: its bucket is wider than it could be, and unify still
// decides every candidate, so this can cost comparisons but never a match.
const maskWidth = 64

// idxKey identifies one index: a relation and the binding pattern it is keyed on. A relation
// probed free-bound and bound-free gets two indexes, which is correct — they bucket on different
// positions — and cheap at this arity.
type idxKey struct {
	rel  string
	mask patternMask
}

// absentKey is the index bucket an ABSENT value files under. A NUL byte cannot appear in a fact
// value read from any supported source, so it is unreachable as real content, which is what makes it
// safe as a sentinel rather than merely unlikely.
const absentKey = "\x00absent"

// tupleKey is the bucket key of values: each value's key, length-prefixed so the encoding is
// injective (without it ("a|b","c") and ("a","b|c") would collide).
func tupleKey(vals []ns.Value) string {
	var sb strings.Builder
	for _, v := range vals {
		k := valueKey(v)
		sb.WriteString(strconv.Itoa(len(k)))
		sb.WriteByte(':')
		sb.WriteString(k)
	}
	return sb.String()
}

// boundArgs reads the atom's bound positions under the current binding, returning the mask and the
// values in positional order. ok is false when nothing is bound, which is the driver atom of a body
// and stays a full scan: there is nothing to look up by.
func boundArgs(args []Term, bnd *binding) (mask patternMask, vals []ns.Value, ok bool) {
	for i, a := range args {
		if i >= maskWidth {
			break
		}
		v, bound := resolve(a, bnd)
		if !bound {
			continue
		}
		// A wildcard resolves to nothing; resolve already reports it unbound, so reaching here
		// means a constant or a genuinely bound variable.
		mask |= 1 << uint(i)
		vals = append(vals, v)
	}
	return mask, vals, mask != 0
}

// edbIndex buckets a relation's tuples by the values at one binding pattern's positions.
//
// Buckets hold POSITIONS into the relation's slice, not copies. A tuple is filed under each of its
// keys, so a probe that looks in several buckets can meet the same tuple twice, and a duplicated
// candidate is not harmless: unify would yield it twice, which inflates a count() aggregate rather
// than merely repeating a row. Positions make that duplicate removable exactly.
type edbIndex map[string][]int

// edbCache holds one Base's base-relation tuples, read from the Source on first use, and the
// per-(relation, pattern) indexes over them.
//
// Behind a pointer on Base so Eval's shallow copy shares one cache instead of copying a lock, and
// so a second query over the same data reuses what the first one built.
type edbCache struct {
	mu  sync.RWMutex
	tup map[string]*edbRead
	idx map[idxKey]edbIndex
}

// edbRead is one relation's read from the Source, made once however many Evals ask for it at the
// same moment: the Source contract promises Tuples is called at most once per relation per Base, and
// a host's read may be a whole-table query it should not run twice. Others needing it wait on mu. A
// read that fails, such as one cancelled with the Eval that started it, is not kept, so the next
// Eval to need the relation reads it again under its own context.
type edbRead struct {
	mu     sync.Mutex
	done   bool
	tuples []ns.Tuple
}

// countWork records one candidate comparison. See Base.work. Atomic because Evals sharing a Base
// share the counter.
func (b *Base) countWork() error {
	if b.work != nil {
		atomic.AddInt64(b.work, 1)
	}
	return b.run.step()
}

// newEDBCache builds an empty cache.
func newEDBCache() *edbCache {
	return &edbCache{tup: map[string]*edbRead{}, idx: map[idxKey]edbIndex{}}
}

// tuples returns rel's tuples, asking the Source the first time and normalizing what it read to types
// (see normalizeTuples).
func (c *edbCache) tuples(run *evalRun, rel string, src ns.Source, units *ns.Units, types []ns.ArgType) ([]ns.Tuple, error) {
	c.mu.Lock()
	r, ok := c.tup[rel]
	if !ok {
		r = &edbRead{}
		c.tup[rel] = r
	}
	c.mu.Unlock()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.done {
		return r.tuples, nil
	}
	if err := run.context().Err(); err != nil {
		return nil, fmt.Errorf("query: evaluation stopped: %w", err)
	}
	t, err := readTuples(run, src, rel)
	if err != nil {
		return nil, err
	}
	t = normalizeTuples(units, t, types)
	r.tuples, r.done = t, true
	return t, nil
}

// holds reports whether the cache holds rel's tuples whole. A relation another Eval is reading at this
// moment counts as not held.
func (c *edbCache) holds(rel string) bool {
	c.mu.RLock()
	r, ok := c.tup[rel]
	c.mu.RUnlock()
	if !ok || !r.mu.TryLock() {
		return false
	}
	defer r.mu.Unlock()
	return r.done
}

// held lists the relations whose tuples the cache holds, and the indexes it has built, for Explain.
// A relation another Eval is reading at this moment counts as not held.
func (c *edbCache) held() (map[string]bool, map[idxKey]bool) {
	c.mu.RLock()
	reads := make(map[string]*edbRead, len(c.tup))
	for rel, r := range c.tup {
		reads[rel] = r
	}
	idx := make(map[idxKey]bool, len(c.idx))
	for k := range c.idx {
		idx[k] = true
	}
	c.mu.RUnlock()
	tup := map[string]bool{}
	for rel, r := range reads {
		if r.mu.TryLock() {
			if r.done {
				tup[rel] = true
			}
			r.mu.Unlock()
		}
	}
	return tup, idx
}

// get returns the index for a relation at a pattern, building it once on first use. Lazy because a
// rule set probes a handful of the possible patterns, and eagerly indexing every relation at every
// mask would cost more than the scans it saves on data nobody queries deeply.
func (c *edbCache) get(rel string, tuples []ns.Tuple, mask patternMask) edbIndex {
	k := idxKey{rel: rel, mask: mask}
	c.mu.RLock()
	idx, ok := c.idx[k]
	c.mu.RUnlock()
	if ok {
		return idx
	}
	built := buildEDBIndex(tuples, mask)
	c.mu.Lock()
	defer c.mu.Unlock()
	// Another goroutine may have built it while this one was working. Keep the existing entry so
	// every caller shares one map rather than racing to replace it.
	if idx, ok := c.idx[k]; ok {
		return idx
	}
	// An Eval still running over tuples read before a reset (see Base.Forget) must not file an index
	// of their positions where a later Eval would probe the relation as read again.
	if r, ok := c.tup[rel]; !ok || !sameTuples(r, tuples) {
		return built
	}
	c.idx[k] = built
	return built
}

// sameTuples reports whether tuples is the relation as r read it.
func sameTuples(r *edbRead, tuples []ns.Tuple) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.done && len(r.tuples) == len(tuples) && (len(tuples) == 0 || &r.tuples[0] == &tuples[0])
}

// reset drops every relation read and every index, so the next Eval reads the Source again.
func (c *edbCache) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tup = map[string]*edbRead{}
	c.idx = map[idxKey]edbIndex{}
}

// buildEDBIndex indexes every tuple of a relation at the given mask. Tuples are immutable for the
// life of a Base, so this is built once per (relation, pattern) and reused across queries.
func buildEDBIndex(tuples []ns.Tuple, mask patternMask) edbIndex {
	idx := edbIndex{}
	vals := make([]ns.Value, 0, 4)
	for pos, t := range tuples {
		vals = vals[:0]
		for i, v := range t.Vals {
			if i < maskWidth && mask&(1<<uint(i)) != 0 {
				vals = append(vals, v)
			}
		}
		k := tupleKey(vals)
		idx[k] = append(idx[k], pos)
	}
	return idx
}

// IndexMinTuples is the relation size below which scanning beats indexing, exported so a host's
// equivalence test can build data large enough to exercise the indexed path.
const IndexMinTuples = indexMinFacts

// indexMinFacts is the relation size below which scanning beats indexing. Building a map over a
// handful of tuples costs more than the loop it replaces. The threshold is deliberately low: the
// quadratic behaviour indexing fixes needs both a large relation AND repeated probes, and neither
// happens under it.
const indexMinFacts = 16

// edbCandidates narrows an atom's tuples to the positions that can match under the current binding.
// all is true when the caller should scan the whole relation instead: nothing is bound (the driver
// atom of a body, which has nothing to look up by) or the relation is too small to index.
func (b *Base) edbCandidates(atom *Atom, tuples []ns.Tuple, bnd *binding) (pos []int, all bool) {
	// No cache means no indexing: a Base built by struct literal rather than NewBase. Unindexed asks
	// for the scan path on purpose, as the oracle an equivalence test compares against.
	if b.edb == nil || b.noIndex || len(tuples) < indexMinFacts {
		return nil, true
	}
	mask, vals, ok := boundArgs(atom.Args, bnd)
	if !ok {
		return nil, true
	}
	idx := b.edb.get(atom.Relation, tuples, mask)
	return idx[tupleKey(vals)], false // the bucket is the candidate list, no copy
}

// idbIndex is the derived-relation analogue, and it must be INCREMENTAL: an IDB relation grows
// during the fixpoint, so an index built at one point goes stale as the next round derives more.
// Rather than rebuilding (which would make the fixpoint quadratic again, in a new place), it tracks
// how many tuples it has seen and indexes only the tail on the next probe.
//
// Positions are stored rather than tuples so the index stays valid as the backing slice grows.
type idbIndex struct {
	buckets map[string][]int
	n       int // tuples of the relation already indexed
}

// sync indexes any tuples appended since the last call.
func (x *idbIndex) sync(tuples []idbTuple, mask patternMask) {
	if x.buckets == nil {
		x.buckets = map[string][]int{}
	}
	vals := make([]ns.Value, 0, 4)
	for ; x.n < len(tuples); x.n++ {
		vals = vals[:0]
		for i, v := range tuples[x.n].vals {
			if i < maskWidth && mask&(1<<uint(i)) != 0 {
				vals = append(vals, v)
			}
		}
		k := tupleKey(vals)
		x.buckets[k] = append(x.buckets[k], x.n)
	}
}

// fullMask is every position of an arity-n tuple, the pattern the dedup set probes on: "has this
// exact tuple been derived". It is the same machinery as a join lookup, which is why the join index and
// the dedup set are one structure rather than two.
func fullMask(arity int) patternMask {
	if arity >= maskWidth {
		return ^patternMask(0)
	}
	return patternMask(1)<<uint(arity) - 1
}

// idbCandidates narrows a derived relation to the tuples that can match under the current binding,
// or returns all of them when nothing is bound or the relation is small.
//
// Unlike the EDB, the relation may have grown since the index was last touched, so the index syncs
// its tail first. Tuples are returned by value to match the previous iteration, and the caller's
// own binding check still decides.
func (b *Base) idbCandidates(atom *Atom, bnd *binding) []idbTuple {
	tuples := b.idb[atom.Relation]
	if len(tuples) < indexMinFacts {
		return tuples
	}
	mask, vals, ok := boundArgs(atom.Args, bnd)
	if !ok {
		return tuples
	}
	x := b.idbIndexFor(atom.Relation, mask)
	x.sync(tuples, mask)
	return gatherTuples(tuples, x.buckets[tupleKey(vals)])
}

// gatherTuples materializes the tuples at the given positions.
func gatherTuples(tuples []idbTuple, pos []int) []idbTuple {
	out := make([]idbTuple, 0, len(pos))
	for _, p := range pos {
		out = append(out, tuples[p])
	}
	return out
}
