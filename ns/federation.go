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
// PushSource and Versioned whatever its members are, and pushes a conjunction to a lone member, or
// across partitioned ones (NewPartitionedFederation). A relation is looked
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
	// partitioned pushes a conjunction across the members (NewPartitionedFederation, #238).
	partitioned bool
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

// NewPartitionedFederation is NewFederation over members that partition the facts and evaluate
// conjunctions (PushSource), such as one SQLite file per repo, whose Holds routes each value to the
// member storing it (#238). It pushes a conjunction across them: to each member Holds allows for the
// first atom, asking for every prefix of its matches too (Conjunction.Partial), and continues each
// prefix in the other members Holds allows for the next atom, so a join whose facts span members
// costs a call per member asked rather than a call per literal.
//
// The answer never depends on how precisely Holds routes: a prefix is continued in every other member
// whose Holds allows the next atom, so a Holds that answers true when unsure costs calls, never
// matches. What the partition promises is that pushing pays off.
func NewPartitionedFederation(members ...Member) (*Federation, error) {
	f, err := NewFederation(members...)
	if err != nil {
		return nil, err
	}
	f.partitioned = true
	return f, nil
}

// Pushes implements PushSource. A Federation of one member pushes what that member pushes. One of
// several pushes only when it is partitioned (NewPartitionedFederation), every member serves every
// relation of c, and each pushes c with its prefixes (Partial). A join of a federation's members
// otherwise could pair facts no one member sees, and is asked literal by literal.
func (f *Federation) Pushes(c Conjunction) bool {
	if len(f.members) == 1 {
		ps, ok := f.members[0].Source.(PushSource)
		return ok && ps.Pushes(c)
	}
	if !f.partitioned || c.Partial {
		return false
	}
	p := c
	p.Partial = true
	for _, a := range c.Atoms {
		if len(f.serving[a.Relation]) != len(f.members) {
			return false
		}
	}
	for _, m := range f.members {
		ps, ok := m.Source.(PushSource)
		if !ok || !ps.Pushes(p) {
			return false
		}
	}
	return true
}

// Push implements PushSource, citing each tuple to the member it came from. With one member it asks
// that member. Partitioned, it asks each member whose Holds allows c's first atom for c with its
// prefixes, yields that member's whole rows, and continues each prefix in the other members whose
// Holds allows the next atom, by pushing the rest of c with the prefix's values as constants. A
// match is found once, through the member holding its first fact and then, at each fact held
// elsewhere, the member holding it. A member's failure is wrapped with its name.
func (f *Federation) Push(ctx context.Context, c Conjunction) iter.Seq2[[]Tuple, error] {
	return func(yield func([]Tuple, error) bool) {
		if len(f.members) == 1 {
			f.pushMember(ctx, 0, c, nil, yield)
			return
		}
		f.pushAcross(ctx, c, -1, nil, yield)
	}
}

// pushMember yields member i's rows for c, each after prefix and cited to i.
func (f *Federation) pushMember(ctx context.Context, i int, c Conjunction, prefix []Tuple, yield func([]Tuple, error) bool) bool {
	ps, ok := f.members[i].Source.(PushSource)
	if !ok {
		return yield(nil, fmt.Errorf("member %s doesn't evaluate conjunctions", f.name(i)))
	}
	for row, err := range ps.Push(ctx, c) {
		if err != nil {
			return yield(nil, fmt.Errorf("member %s: %w", f.name(i), err))
		}
		if !yield(f.citedRow(i, prefix, row), nil) {
			return false
		}
	}
	return true
}

// pushAcross yields the rows for c, after prefix, from every member but except whose Holds allows c's
// first atom, continuing their prefixes in the others (see Push).
func (f *Federation) pushAcross(ctx context.Context, c Conjunction, except int, prefix []Tuple, yield func([]Tuple, error) bool) bool {
	p := c
	p.Partial = true
	first := c.Atoms[0]
	for _, i := range f.serving[first.Relation] {
		if i == except || !f.holds(i, first) {
			continue
		}
		var partial [][]Tuple
		more := f.pushMember(ctx, i, p, nil, func(row []Tuple, err error) bool {
			switch {
			case err != nil:
				return yield(nil, err)
			case len(row) == 0 || len(row) > len(c.Atoms):
				return yield(nil, fmt.Errorf("member %s: a row of %d tuples for %d atoms", f.name(i), len(row), len(c.Atoms)))
			case len(row) < len(c.Atoms):
				partial = append(partial, row)
				return true
			}
			return yield(append(prefix[:len(prefix):len(prefix)], row...), nil)
		})
		if !more {
			return false
		}
		for _, row := range partial {
			next := append(prefix[:len(prefix):len(prefix)], row...)
			if !f.pushAcross(ctx, c.after(row), i, next, yield) {
				return false
			}
		}
	}
	return true
}

// holds reports whether member i's Holds allows atom a, its constants bound.
func (f *Federation) holds(i int, a PushAtom) bool {
	h := f.members[i].Holds
	if h == nil {
		return true
	}
	bound := map[int]Value{}
	for pos, x := range a.Args {
		if x.Const != nil {
			bound[pos] = *x.Const
		}
	}
	return h(a.Relation, bound)
}

// citedRow is prefix, then row's tuples cited to member i.
func (f *Federation) citedRow(i int, prefix, row []Tuple) []Tuple {
	out := make([]Tuple, 0, len(prefix)+len(row))
	out = append(out, prefix...)
	for _, t := range row {
		out = append(out, f.cite(i, t))
	}
	return out
}

// after is the rest of c once a prefix of its atoms matched the tuples in row: the atoms and
// comparisons after them, each variable the prefix bound replaced by its value.
func (c Conjunction) after(row []Tuple) Conjunction {
	vals := map[int]Value{}
	for k, t := range row {
		for pos, x := range c.Atoms[k].Args {
			if _, ok := vals[x.Var]; x.Var > 0 && !ok && pos < len(t.Vals) {
				vals[x.Var] = t.Vals[pos]
			}
		}
	}
	arg := func(x PushArg) PushArg {
		if v, ok := vals[x.Var]; x.Var > 0 && ok {
			return PushArg{Const: &v}
		}
		return x
	}
	var rest Conjunction
	for _, a := range c.Atoms[len(row):] {
		na := PushAtom{Relation: a.Relation, Args: make([]PushArg, len(a.Args))}
		for pos, x := range a.Args {
			na.Args[pos] = arg(x)
		}
		rest.Atoms = append(rest.Atoms, na)
	}
	for _, cmp := range c.Compare {
		rest.Compare = append(rest.Compare, PushComparison{Op: cmp.Op, Left: arg(cmp.Left), Right: arg(cmp.Right)})
	}
	return rest
}
