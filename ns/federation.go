package ns

import (
	"context"
	"fmt"
	"iter"
	"reflect"
	"strconv"
	"strings"
)

// A Member is one Source in a Federation.
type Member struct {
	// Name identifies the member in errors and, when set, in citations: each of its tuples' citations
	// reads Name + ":" + citation, so a witness says which store a fact came from. Empty leaves its
	// citations as the Source wrote them.
	Name string
	// Source serves the member's facts.
	Source Source
	// Holds reports whether the member can hold a fact of rel matching the bound arguments (positions
	// from 0, values read as the arguments' declared types, as LookupSource's are). A lookup goes only
	// to the members that can, so a member holding one repo's ids is never asked about another's. It
	// may answer true when unsure, never false for a fact the member holds, or the answer loses it.
	// Nil holds anything. Reading a relation whole always reads every member.
	Holds func(rel string, bound map[int]Value) bool
}

// HoldsPrefix is a Member.Holds for a member whose stored values at some positions all start with
// prefix, such as the ids of one repo ("github.com/hashicorp/consul-template/"). positions names, per
// relation, those positions; a call binding one of them to a value without the prefix can't match. A
// relation it doesn't name may hold anything.
func HoldsPrefix(prefix string, positions map[string][]int) func(rel string, bound map[int]Value) bool {
	return func(rel string, bound map[int]Value) bool {
		for _, p := range positions[rel] {
			if v, ok := bound[p]; ok && !strings.HasPrefix(v.S, prefix) {
				return false
			}
		}
		return true
	}
}

// A Federation is one Source over several (#211): each relation's tuples are the union of its
// members' tuples, read whole from every member serving it or looked up in only the members that can
// hold a match. A host passes it to NewBase like any other Source, so facts kept in several stores
// are queried without first being merged into one.
//
// A fact two members serve is two facts, as it would be in one Source serving it twice: a goal's
// answer rows are distinct either way, and an aggregate counting bindings counts both.
//
// It is a ContextSource, a StreamSource, a LookupSource, a BatchLookupSource, IndexedLookups,
// PushSource and Versioned whatever its members are, and pushes a conjunction only to a lone member
// (Pushes). A relation is looked
// up only when every member serving it looks it up (LooksUp), so a Federation can be a member of
// another; otherwise the engine reads it
// whole, from every member, and keeps it. Its version changes whenever a member's does, and a member
// that isn't Versioned is taken as fixed. Its methods are safe for concurrent use when its members'
// are.
type Federation struct {
	members []Member
	schemas map[string]Schema
	serving map[string][]int // relation -> the members serving it, by index
	order   []string
	lookups map[string]bool
}

// NewFederation builds a Federation over members, in the order their relations are listed and their
// tuples concatenated. A relation several members serve must have the same schema in each: the same
// arity, labels and types (Doc may differ, and the first member's is kept). It refuses one that
// doesn't, naming the relation and both members, since the engine would read the members' tuples as
// one relation.
func NewFederation(members ...Member) (*Federation, error) {
	f := &Federation{members: append([]Member(nil), members...), schemas: map[string]Schema{}, serving: map[string][]int{}, lookups: map[string]bool{}}
	for i, m := range members {
		if m.Source == nil {
			return nil, fmt.Errorf("query: federation: member %s has no Source", f.name(i))
		}
		_, looks := m.Source.(LookupSource)
		lr, partly := m.Source.(LookupRelations)
		for _, rel := range m.Source.Relations() {
			looks := looks && (!partly || lr.LooksUp(rel))
			s, ok := m.Source.Schema(rel)
			if !ok {
				return nil, fmt.Errorf("query: federation: member %s lists %s but has no schema for it", f.name(i), rel)
			}
			if prev, seen := f.serving[rel]; seen {
				if !sameShape(f.schemas[rel], s) {
					return nil, fmt.Errorf("query: federation: %s has a different schema in member %s than in member %s", rel, f.name(i), f.name(prev[0]))
				}
				f.lookups[rel] = f.lookups[rel] && looks
			} else {
				f.schemas[rel] = s
				f.order = append(f.order, rel)
				f.lookups[rel] = looks
			}
			f.serving[rel] = append(f.serving[rel], i)
		}
	}
	return f, nil
}

// name is member i's Name, or its position when it has none.
func (f *Federation) name(i int) string {
	if n := f.members[i].Name; n != "" {
		return strconv.Quote(n)
	}
	return "#" + strconv.Itoa(i)
}

func sameShape(a, b Schema) bool {
	return a.Arity == b.Arity && reflect.DeepEqual(a.Labels, b.Labels) && reflect.DeepEqual(a.Types, b.Types)
}

// Schema implements Source.
func (f *Federation) Schema(rel string) (Schema, bool) {
	s, ok := f.schemas[rel]
	return s, ok
}

// Relations implements Source: every member's relations, each once, in the order the members list
// them.
func (f *Federation) Relations() []string { return append([]string(nil), f.order...) }

// Tuples implements Source: the members' tuples for rel, concatenated in member order.
func (f *Federation) Tuples(rel string) []Tuple {
	if s := f.serving[rel]; len(s) == 1 {
		return f.cited(s[0], f.members[s[0]].Source.Tuples(rel))
	}
	var out []Tuple
	for _, i := range f.serving[rel] {
		out = append(out, f.cited(i, f.members[i].Source.Tuples(rel))...)
	}
	return out
}

// TuplesContext implements ContextSource, reading each member through its TuplesContext when it has
// one. A member's failure fails the read, wrapped with the member's name.
func (f *Federation) TuplesContext(ctx context.Context, rel string) ([]Tuple, error) {
	var out []Tuple
	for _, i := range f.serving[rel] {
		src := f.members[i].Source
		var ts []Tuple
		if cs, ok := src.(ContextSource); ok {
			var err error
			if ts, err = cs.TuplesContext(ctx, rel); err != nil {
				return nil, fmt.Errorf("member %s: %w", f.name(i), err)
			}
		} else {
			ts = src.Tuples(rel)
		}
		out = append(out, f.cited(i, ts)...)
	}
	return out, nil
}

// TuplesSeq implements StreamSource: the members' tuples for rel in member order, a member that
// streams one at a time and any other as it reads them whole, so the engine charges each to the
// budget and stops a large read before the next member is read. A member's failure ends the
// sequence with it, wrapped with the member's name.
func (f *Federation) TuplesSeq(ctx context.Context, rel string) iter.Seq2[Tuple, error] {
	return func(yield func(Tuple, error) bool) {
		for _, i := range f.serving[rel] {
			src := f.members[i].Source
			if ss, ok := src.(StreamSource); ok {
				for t, err := range ss.TuplesSeq(ctx, rel) {
					if err != nil {
						yield(Tuple{}, fmt.Errorf("member %s: %w", f.name(i), err))
						return
					}
					if !yield(f.cite(i, t), nil) {
						return
					}
				}
				continue
			}
			var ts []Tuple
			if cs, ok := src.(ContextSource); ok {
				var err error
				if ts, err = cs.TuplesContext(ctx, rel); err != nil {
					yield(Tuple{}, fmt.Errorf("member %s: %w", f.name(i), err))
					return
				}
			} else {
				ts = src.Tuples(rel)
			}
			for _, t := range ts {
				if !yield(f.cite(i, t), nil) {
					return
				}
			}
		}
	}
}

// LooksUp implements LookupRelations: rel is looked up when every member serving it answers lookups.
func (f *Federation) LooksUp(rel string) bool { return f.lookups[rel] }

// Indexed implements IndexedLookups: a pattern is indexed when every member serving rel indexes it, a
// member that doesn't say counting as indexing everything, since one member's scan makes the call a
// scan.
func (f *Federation) Indexed(rel string, bound []int) bool {
	for _, i := range f.serving[rel] {
		if il, ok := f.members[i].Source.(IndexedLookups); ok && !il.Indexed(rel, bound) {
			return false
		}
	}
	return true
}

// Lookup implements LookupSource, asking each member serving rel whose Holds allows the bound values,
// in member order, and returning what they answer together. A member that doesn't answer lookups
// (only asked when LooksUp is false, which the engine doesn't do) returns its whole relation, as the
// contract allows. A member's failure fails the lookup, wrapped with the member's name.
func (f *Federation) Lookup(ctx context.Context, rel string, bound map[int]Value) ([]Tuple, error) {
	var out []Tuple
	for _, i := range f.serving[rel] {
		m := f.members[i]
		if m.Holds != nil && !m.Holds(rel, bound) {
			continue
		}
		ls, ok := m.Source.(LookupSource)
		if !ok {
			out = append(out, f.cited(i, m.Source.Tuples(rel))...)
			continue
		}
		ts, err := ls.Lookup(ctx, rel, bound)
		if err != nil {
			return nil, fmt.Errorf("member %s: %w", f.name(i), err)
		}
		out = append(out, f.cited(i, ts)...)
	}
	return out, nil
}

// LookupBatch implements BatchLookupSource: each member serving rel is asked once for the values its
// Holds allows, through its own LookupBatch, or one Lookup per value when it has none, so a batch
// costs a member that batches one round trip however many values it carries. A value's result is its
// members' results in member order, as Lookup's is. A member that doesn't answer lookups returns its
// whole relation, read once, as in Lookup. A member's failure fails the batch, wrapped with the
// member's name.
func (f *Federation) LookupBatch(ctx context.Context, rel string, positions []int, values [][]Value) ([][]Tuple, error) {
	out := make([][]Tuple, len(values))
	bounds := make([]map[int]Value, len(values))
	for k, vals := range values {
		bounds[k] = boundAt(positions, vals)
	}
	for _, i := range f.serving[rel] {
		m := f.members[i]
		var held []int
		for k := range values {
			if m.Holds == nil || m.Holds(rel, bounds[k]) {
				held = append(held, k)
			}
		}
		if len(held) == 0 {
			continue
		}
		res, err := lookupEach(ctx, m.Source, rel, positions, values, held)
		if err != nil {
			return nil, fmt.Errorf("member %s: %w", f.name(i), err)
		}
		for j, k := range held {
			out[k] = append(out[k], f.cited(i, res[j])...)
		}
	}
	return out, nil
}

// lookupEach asks src for rel's facts matching values[k] at positions, for each k in held, returning a
// result per k: in one call when it batches, one Lookup per value when it only looks up, and its whole
// relation, read once, otherwise.
func lookupEach(ctx context.Context, src Source, rel string, positions []int, values [][]Value, held []int) ([][]Tuple, error) {
	res := make([][]Tuple, len(held))
	if bs, ok := src.(BatchLookupSource); ok {
		asked := make([][]Value, len(held))
		for j, k := range held {
			asked[j] = values[k]
		}
		got, err := bs.LookupBatch(ctx, rel, positions, asked)
		if err != nil {
			return nil, err
		}
		if len(got) != len(asked) {
			return nil, fmt.Errorf("a batch of %d lookups of %s answered %d", len(asked), rel, len(got))
		}
		return got, nil
	}
	ls, ok := src.(LookupSource)
	if !ok {
		all := src.Tuples(rel)
		for j := range res {
			res[j] = all
		}
		return res, nil
	}
	for j, k := range held {
		ts, err := ls.Lookup(ctx, rel, boundAt(positions, values[k]))
		if err != nil {
			return nil, err
		}
		res[j] = ts
	}
	return res, nil
}

// boundAt is one batched lookup's values as Lookup's bound map.
func boundAt(positions []int, vals []Value) map[int]Value {
	bound := make(map[int]Value, len(positions))
	for j, p := range positions {
		bound[p] = vals[j]
	}
	return bound
}

// Version implements Versioned: the members' versions together, each length-prefixed so no two lists
// of versions read alike. A member that isn't Versioned contributes nothing, as fixed.
func (f *Federation) Version() string {
	var sb strings.Builder
	for _, m := range f.members {
		v := ""
		if vs, ok := m.Source.(Versioned); ok {
			v = vs.Version()
		}
		sb.WriteString(strconv.Itoa(len(v)))
		sb.WriteByte(':')
		sb.WriteString(v)
	}
	return sb.String()
}

// cited is member i's tuples with their citations named for it, or ts itself when it has no Name.
func (f *Federation) cited(i int, ts []Tuple) []Tuple {
	if f.members[i].Name == "" || len(ts) == 0 {
		return ts
	}
	out := make([]Tuple, len(ts))
	for k, t := range ts {
		out[k] = f.cite(i, t)
	}
	return out
}

// cite is t with member i's Name before each citation, as cited writes them.
func (f *Federation) cite(i int, t Tuple) Tuple {
	name := f.members[i].Name
	if name == "" {
		return t
	}
	cites := make([]string, len(t.Cites))
	for j, c := range t.Cites {
		cites[j] = name + ":" + c
	}
	return Tuple{Vals: t.Vals, Cites: cites}
}

// Pushes implements PushSource: a Federation evaluates a conjunction only when it has one member, and
// that member pushes it. With several, a join could pair facts from different members, which no one
// member sees (#238).
func (f *Federation) Pushes(c Conjunction) bool {
	if len(f.members) != 1 {
		return false
	}
	ps, ok := f.members[0].Source.(PushSource)
	return ok && ps.Pushes(c)
}

// Push implements PushSource through the one member Pushes allows, citing each tuple to it as a read
// or a lookup does. A member's failure is wrapped with its name.
func (f *Federation) Push(ctx context.Context, c Conjunction) iter.Seq2[[]Tuple, error] {
	return func(yield func([]Tuple, error) bool) {
		ps, ok := f.members[0].Source.(PushSource)
		if len(f.members) != 1 || !ok {
			yield(nil, fmt.Errorf("federation: %d members can't evaluate a conjunction together", len(f.members)))
			return
		}
		for row, err := range ps.Push(ctx, c) {
			if err != nil {
				yield(nil, fmt.Errorf("member %s: %w", f.name(0), err))
				return
			}
			cited := make([]Tuple, len(row))
			for k, t := range row {
				cited[k] = f.cite(0, t)
			}
			if !yield(cited, nil) {
				return
			}
		}
	}
}
