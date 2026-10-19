package ns

import (
	"context"
	"fmt"
	"hash/fnv"
	"iter"
	"strings"
)

// An OverlaySource is a working session's edits over a Source (#288): it serves the base's facts, minus
// those a change set names, plus those it adds, so a query reads the session's own writes before they
// are committed, and the base is never changed. Rolling the session back drops the overlay; an undo
// stack is a stack of Changes composed with Then, each state its own overlay.
//
// A fact the change set names is replaced wherever the base holds it, as Changes.Apply does, and is
// matched as a Lookup matches, each value read as its argument's declared type, so a removal names a
// fact by the values the store serves (in the store's own units). An overlay passes on every interface
// the base implements: whole reads stream when the base streams, a lookup or a batch goes to the base
// and the added facts join its answer (every one of the relation's, which a lookup may return, since
// the engine checks each), and a pushed conjunction, a walk or a summary goes to the base only when it
// reads no relation the session touched, which is otherwise solved literal by literal through lookups.
//
// An overlay is fixed once made and safe for concurrent use as its base is. Its Version is the base's
// joined with the change set's, so a Base over a session sees a new version whenever an edit makes a
// new overlay, and drops what it kept; a session's Base and the base's own keep separate caches, and
// the base's stay valid. An overlay over a TimedSource serves its plain reads, so to edit a version,
// overlay that version's Source (SourceAt) instead.
type OverlaySource struct {
	base    Source
	changes Changes
	named   map[string]map[string]bool
	added   map[string][]Tuple
	version string
}

// Overlay returns base with c applied. c is copied, so editing it afterwards doesn't change the overlay.
func Overlay(base Source, c Changes) *OverlaySource {
	o := &OverlaySource{base: base, changes: Changes{}.Then(c), named: map[string]map[string]bool{}, added: map[string][]Tuple{}}
	h := fnv.New64a()
	for _, rel := range o.changes.rels {
		r := o.changes.byRel[rel]
		named := map[string]bool{}
		for _, k := range r.keys {
			ch := r.facts[k]
			named[o.key(rel, ch.t.Vals)] = true
			fmt.Fprintf(h, "%d:%s|%d:%s|%t|%d:%s|%q;", len(rel), rel, len(k), k, ch.add, len(ch.t.ID), ch.t.ID, ch.t.Cites)
		}
		o.named[rel] = named
		o.added[rel] = o.changes.Added(rel)
	}
	if v, ok := base.(Versioned); ok {
		o.version = v.Version()
	}
	if !o.changes.Empty() {
		o.version += fmt.Sprintf("+%016x", h.Sum64())
	}
	return o
}

// Base is the Source the overlay edits.
func (o *OverlaySource) Base() Source { return o.base }

// Changes is the change set the overlay applies.
func (o *OverlaySource) Changes() Changes { return Changes{}.Then(o.changes) }

func (o *OverlaySource) key(rel string, vals []Value) string {
	s, _ := o.base.Schema(rel)
	return typedKey(s, vals)
}

// typedKey keys a fact of a relation with schema s by its values, each as its argument's declared type
// reads it (joinKey), as a Lookup matches it.
func typedKey(s Schema, vals []Value) string {
	var sb strings.Builder
	for i, v := range vals {
		var at ArgType
		if i < len(s.Types) {
			at = s.Types[i]
		}
		k := joinKey(v, at)
		fmt.Fprintf(&sb, "%d:%s", len(k), k)
	}
	return sb.String()
}

func (o *OverlaySource) touched(rel string) bool { return o.named[rel] != nil }

func (o *OverlaySource) touchesAny(c Conjunction) bool {
	for _, a := range c.Atoms {
		if o.touched(a.Relation) {
			return true
		}
	}
	return false
}

// kept drops from ts the facts the session names.
func (o *OverlaySource) kept(rel string, ts []Tuple) []Tuple {
	named := o.named[rel]
	out := make([]Tuple, 0, len(ts))
	for _, t := range ts {
		if !named[o.key(rel, t.Vals)] {
			out = append(out, t)
		}
	}
	return out
}

func (o *OverlaySource) Schema(rel string) (Schema, bool) { return o.base.Schema(rel) }
func (o *OverlaySource) Relations() []string              { return o.base.Relations() }

// NoVocabularyHint is the base's, when it gives one.
func (o *OverlaySource) NoVocabularyHint() string {
	if h, ok := o.base.(NoVocabularyHinter); ok {
		return h.NoVocabularyHint()
	}
	return "no relations are installed"
}

// Version is the base's version, when it has one, joined with a fingerprint of the change set, which
// covers each fact's values, ID and citations.
func (o *OverlaySource) Version() string { return o.version }

// Tuples serves rel with the session's edits applied. A base read failing reads as no facts there,
// as through the base's own Tuples; the engine reads through TuplesSeq, which reports it.
func (o *OverlaySource) Tuples(rel string) []Tuple {
	if !o.touched(rel) {
		return o.base.Tuples(rel)
	}
	t, _ := o.TuplesContext(context.Background(), rel)
	return t
}

// TuplesContext reads rel from the base as the engine would, and applies the session's edits.
func (o *OverlaySource) TuplesContext(ctx context.Context, rel string) ([]Tuple, error) {
	base, err := readWhole(ctx, o.base, rel)
	if err != nil || !o.touched(rel) {
		return base, err
	}
	return append(o.kept(rel, base), o.added[rel]...), nil
}

// TuplesSeq streams rel's facts with the session's edits applied: the base's as it hands them over,
// when it streams, then the facts the session adds.
func (o *OverlaySource) TuplesSeq(ctx context.Context, rel string) iter.Seq2[Tuple, error] {
	return func(yield func(Tuple, error) bool) {
		ss, ok := o.base.(StreamSource)
		if !ok {
			ts, err := o.TuplesContext(ctx, rel)
			if err != nil {
				yield(Tuple{}, err)
				return
			}
			for _, t := range ts {
				if !yield(t, nil) {
					return
				}
			}
			return
		}
		named := o.named[rel]
		for t, err := range ss.TuplesSeq(ctx, rel) {
			if err != nil {
				yield(Tuple{}, err)
				return
			}
			if named != nil && named[o.key(rel, t.Vals)] {
				continue
			}
			if !yield(t, nil) {
				return
			}
		}
		for _, t := range o.added[rel] {
			if !yield(t, nil) {
				return
			}
		}
	}
}

// readWhole reads rel from src through the most capable read it offers, as the engine does.
func readWhole(ctx context.Context, src Source, rel string) ([]Tuple, error) {
	switch s := src.(type) {
	case StreamSource:
		var out []Tuple
		for t, err := range s.TuplesSeq(ctx, rel) {
			if err != nil {
				return nil, err
			}
			out = append(out, t)
		}
		return out, nil
	case ContextSource:
		return s.TuplesContext(ctx, rel)
	}
	return src.Tuples(rel), nil
}

// LooksUp is whether the base looks rel up.
func (o *OverlaySource) LooksUp(rel string) bool {
	if lr, ok := o.base.(LookupRelations); ok {
		return lr.LooksUp(rel)
	}
	_, ok := o.base.(LookupSource)
	return ok
}

// Indexed is the base's answer, and true when the base doesn't say.
func (o *OverlaySource) Indexed(rel string, bound []int) bool {
	if il, ok := o.base.(IndexedLookups); ok {
		return il.Indexed(rel, bound)
	}
	return true
}

// Lookup asks the base, drops the facts the session names, and adds every fact it adds to rel. A base
// that doesn't look rel up is read whole, which Lookup's contract allows.
func (o *OverlaySource) Lookup(ctx context.Context, rel string, bound map[int]Value) ([]Tuple, error) {
	var ts []Tuple
	var err error
	if ls, ok := o.base.(LookupSource); ok && o.LooksUp(rel) {
		ts, err = ls.Lookup(ctx, rel, bound)
	} else {
		ts, err = readWhole(ctx, o.base, rel)
	}
	if err != nil || !o.touched(rel) {
		return ts, err
	}
	return append(o.kept(rel, ts), o.added[rel]...), nil
}

// LookupBatch asks the base in one batch when it batches, and one value at a time otherwise, then edits
// each result as Lookup does.
func (o *OverlaySource) LookupBatch(ctx context.Context, rel string, positions []int, values [][]Value) ([][]Tuple, error) {
	bs, ok := o.base.(BatchLookupSource)
	if !ok || !o.LooksUp(rel) {
		out := make([][]Tuple, len(values))
		for i, vals := range values {
			ts, err := o.Lookup(ctx, rel, boundAt(positions, vals))
			if err != nil {
				return nil, err
			}
			out[i] = ts
		}
		return out, nil
	}
	out, err := bs.LookupBatch(ctx, rel, positions, values)
	if err != nil || !o.touched(rel) {
		return out, err
	}
	for i, ts := range out {
		out[i] = append(o.kept(rel, ts), o.added[rel]...)
	}
	return out, nil
}

// Pushes is the base's answer for a conjunction reading no relation the session touched, and false
// otherwise or when the base doesn't push.
func (o *OverlaySource) Pushes(c Conjunction) bool {
	ps, ok := o.base.(PushSource)
	return ok && !o.touchesAny(c) && ps.Pushes(c)
}

// Push is the base's, for a conjunction Pushes accepted.
func (o *OverlaySource) Push(ctx context.Context, c Conjunction) iter.Seq2[[]Tuple, error] {
	return o.base.(PushSource).Push(ctx, c)
}

// PushesFilter is the base's answer, and false when it doesn't say.
func (o *OverlaySource) PushesFilter(name string) bool {
	fs, ok := o.base.(FilterSource)
	return ok && fs.PushesFilter(name)
}

// Summarizes is the base's answer for a conjunction reading no relation the session touched.
func (o *OverlaySource) Summarizes(c Conjunction, s Summary) bool {
	ss, ok := o.base.(SummarySource)
	return ok && !o.touchesAny(c) && ss.Summarizes(c, s)
}

// Summarize is the base's, for a conjunction Summarizes accepted.
func (o *OverlaySource) Summarize(ctx context.Context, c Conjunction, s Summary) iter.Seq2[SummaryRow, error] {
	return o.base.(SummarySource).Summarize(ctx, c, s)
}

// Walks is the base's answer for a relation the session didn't touch.
func (o *OverlaySource) Walks(rel string, from, to int, bound []int) bool {
	ws, ok := o.base.(WalkSource)
	return ok && !o.touched(rel) && ws.Walks(rel, from, to, bound)
}

// Walk is the base's, for a walk Walks accepted.
func (o *OverlaySource) Walk(ctx context.Context, w Walk) iter.Seq2[Tuple, error] {
	return o.base.(WalkSource).Walk(ctx, w)
}

// WalksConjunction is the base's answer for a conjunction reading no relation the session touched.
func (o *OverlaySource) WalksConjunction(c Conjunction, in, out int) bool {
	ws, ok := o.base.(ConjunctionWalkSource)
	return ok && !o.touchesAny(c) && ws.WalksConjunction(c, in, out)
}

// WalkConjunction is the base's, for a walk WalksConjunction accepted.
func (o *OverlaySource) WalkConjunction(ctx context.Context, c Conjunction, in, out int, start []Value) iter.Seq2[[]Tuple, error] {
	return o.base.(ConjunctionWalkSource).WalkConjunction(ctx, c, in, out, start)
}

// Effective is the session's changes that change the base: a removal of a fact the base holds and an
// addition of one it doesn't, in the order the change set names them. A fact added that the base holds
// already, with other citations or another ID, isn't a change, as Base.Diff doesn't count one. It is
// what a host rechecks after an edit, and what a commit adds as a new version. Each fact is looked up
// with every argument bound when the base looks its relation up, and the relation is read whole once
// otherwise.
func (o *OverlaySource) Effective(ctx context.Context) (Changes, error) {
	var out Changes
	for _, rel := range o.changes.rels {
		r := o.changes.byRel[rel]
		var whole map[string]bool
		ls, looks := o.base.(LookupSource)
		looks = looks && o.LooksUp(rel)
		if !looks {
			ts, err := readWhole(ctx, o.base, rel)
			if err != nil {
				return Changes{}, err
			}
			whole = map[string]bool{}
			for _, t := range ts {
				whole[o.key(rel, t.Vals)] = true
			}
		}
		for _, k := range r.keys {
			ch := r.facts[k]
			want := o.key(rel, ch.t.Vals)
			held := whole[want]
			if looks {
				bound := map[int]Value{}
				for i, v := range ch.t.Vals {
					bound[i] = v
				}
				ts, err := ls.Lookup(ctx, rel, bound)
				if err != nil {
					return Changes{}, err
				}
				for _, t := range ts {
					if o.key(rel, t.Vals) == want {
						held = true
						break
					}
				}
			}
			if ch.add != held {
				out.put(rel, ch.t, ch.add)
			}
		}
	}
	return out, nil
}
