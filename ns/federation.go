package ns

import (
	"context"
	"fmt"
	"iter"
	"reflect"
	"slices"
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
// PushSource, SummarySource, WalkSource, ConjunctionWalkSource and Versioned whatever its members are, and pushes a conjunction to a lone member, or
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
// costs a call per member asked rather than a call per literal, or per prefix leaving a member.
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

// Pushes implements PushSource. A Federation of one member pushes what that member pushes. A page
// (Conjunction.Page) of one atom is pushed when every member serving it pushes it, since one fact is in
// one member and the federation's first rows are among theirs. Otherwise one of
// several pushes only when it is partitioned (NewPartitionedFederation), every member serves every
// relation of c, and each pushes c with its prefixes (Partial), with c's Inputs when it has them
// (#268). A join of a federation's members otherwise could pair facts no one member sees, and is
// asked literal by literal.
func (f *Federation) Pushes(c Conjunction) bool {
	if len(f.members) == 1 {
		ps, ok := f.members[0].Source.(PushSource)
		return ok && ps.Pushes(c)
	}
	if len(c.Inputs) > 0 && c.Page != nil {
		return false // a goal's page has no inputs
	}
	if c.Page != nil {
		if f.single(c, func(src Source) bool { ps, ok := src.(PushSource); return ok && ps.Pushes(c) }) {
			return true
		}
		return f.across(c, func(src Source) bool { ps, ok := src.(PushSource); return ok && ps.Pushes(c) })
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
	return f.membersPush(p)
}

// Push implements PushSource, citing each tuple to the member it came from. With one member it asks
// that member. A page of one atom goes to each member whose Holds allows it, each answering its own
// first rows, which hold the federation's (see Page). Partitioned, it asks each member whose Holds allows c's first atom for c with its
// prefixes, yields that member's whole rows, and continues its prefixes in the other members whose
// Holds allows the next atom: the rest of c once per member for all prefixes of as many atoms, their
// values as its Bindings (continuePrefixes), or once per prefix with its values as constants to a
// member that doesn't take Inputs. With Bindings, each goes only to the members whose Holds allows the
// first atom under its values, and each of those is asked once for all of its bindings (#268). A
// match is found once, through the member holding its first fact and then, at each fact held
// elsewhere, the member holding it. A member's failure is wrapped with its name.
func (f *Federation) Push(ctx context.Context, c Conjunction) iter.Seq2[[]Tuple, error] {
	return func(yield func([]Tuple, error) bool) {
		if len(f.members) == 1 {
			f.pushMember(ctx, 0, c, nil, yield)
			return
		}
		if c.Page != nil {
			for _, i := range f.serving[c.Atoms[0].Relation] {
				if f.holds(i, c.Atoms[0]) && !f.pushMember(ctx, i, c, nil, yield) {
					return
				}
			}
			if len(c.Atoms) > 1 {
				f.spanning(ctx, c, func(row []Tuple) bool { return yield(row, nil) }, func(err error) { yield(nil, err) })
			}
			return
		}
		f.pushAcross(ctx, c, -1, yield)
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

// pushAcross yields the rows for c from every member but except whose Holds allows c's first atom
// (under one of c's Bindings, when it has Inputs), continuing their prefixes in the others (see Push
// and continuePrefixes).
func (f *Federation) pushAcross(ctx context.Context, c Conjunction, except int, yield func([]Tuple, error) bool) bool {
	p := c
	p.Partial = true
	for _, i := range f.serving[c.Atoms[0].Relation] {
		if i == except {
			continue
		}
		q, ok := f.routed(i, p)
		if !ok {
			continue
		}
		var prefixes [][]Tuple
		more := f.pushMember(ctx, i, q, nil, func(row []Tuple, err error) bool {
			switch {
			case err != nil:
				return yield(nil, err)
			case len(row) == 0 || len(row) > len(c.Atoms):
				return yield(nil, fmt.Errorf("member %s: a row of %d tuples for %d atoms", f.name(i), len(row), len(c.Atoms)))
			case len(row) < len(c.Atoms):
				prefixes = append(prefixes, row)
				return true
			}
			return yield(row, nil)
		})
		if !more || !f.continuePrefixes(ctx, c, i, prefixes, yield) {
			return false
		}
	}
	return true
}

// continuePrefixes continues prefixes, prefixes of c's matches member from returned, in the other
// members, yielding each prefix and its continuation as one row. Prefixes of as many atoms share one
// push of the rest of c (#268), whose Inputs are the variables they bind (and c's own Inputs still
// free) and whose Bindings are their values, so a member is asked once for all of them; each row it
// returns is joined back to every prefix with its values. A rest the members don't push with Inputs
// is pushed once per prefix, with the prefix's values as constants.
func (f *Federation) continuePrefixes(ctx context.Context, c Conjunction, from int, prefixes [][]Tuple, yield func([]Tuple, error) bool) bool {
	byLen := map[int][][]Tuple{}
	var lens []int
	for _, p := range prefixes {
		if _, ok := byLen[len(p)]; !ok {
			lens = append(lens, len(p))
		}
		byLen[len(p)] = append(byLen[len(p)], p)
	}
	slices.Sort(lens)
	for _, k := range lens {
		group := byLen[k]
		if rest, joined := f.restAfter(c, k, group); rest != nil {
			more := f.pushAcross(ctx, *rest, from, func(row []Tuple, err error) bool {
				if err != nil {
					return yield(nil, err)
				}
				for _, p := range joined[f.keyOf(*rest, row)] {
					if !yield(append(p[:len(p):len(p)], row...), nil) {
						return false
					}
				}
				return true
			})
			if !more {
				return false
			}
			continue
		}
		for _, p := range group {
			more := f.pushAcross(ctx, c.after(p), from, func(row []Tuple, err error) bool {
				if err != nil {
					return yield(nil, err)
				}
				return yield(append(p[:len(p):len(p)], row...), nil)
			})
			if !more {
				return false
			}
		}
	}
	return true
}

// restAfter is c's atoms from k on, for prefixes of k atoms, as one conjunction with Inputs, and the
// prefixes by the key (keyOf) of the values each binds those Inputs to, or nil when the members don't
// push it so. A comparison reading a variable the rest's atoms don't goes no further: the engine checks
// every row again. A prefix agreeing with none of c's Bindings is dropped, and one agreeing with
// several is joined under each.
func (f *Federation) restAfter(c Conjunction, k int, prefixes [][]Tuple) (*Conjunction, map[string][][]Tuple) {
	type place struct{ atom, pos int }
	first := func(atoms []PushAtom) map[int]place {
		at := map[int]place{}
		for ai, a := range atoms {
			for pos, x := range a.Args {
				if _, ok := at[x.Var]; x.Var > 0 && !ok {
					at[x.Var] = place{ai, pos}
				}
			}
		}
		return at
	}
	rest := Conjunction{Atoms: slices.Clone(c.Atoms[k:])}
	inPrefix, inRest := first(c.Atoms[:k]), first(rest.Atoms)
	for n := range inRest {
		if _, bound := inPrefix[n]; bound || slices.Contains(c.Inputs, n) {
			rest.Inputs = append(rest.Inputs, n)
		}
	}
	slices.Sort(rest.Inputs)
	for _, cmp := range c.Compare {
		_, l := inRest[cmp.Left.Var]
		_, r := inRest[cmp.Right.Var]
		if (cmp.Left.Var == 0 || l) && (cmp.Right.Var == 0 || r) {
			rest.Compare = append(rest.Compare, cmp)
		}
	}
	offered := rest
	offered.Partial = true
	if len(rest.Inputs) == 0 || !f.membersPush(offered) {
		return nil, nil
	}
	typeOf := func(atoms []PushAtom, p place) ArgType {
		s, _ := f.Schema(atoms[p.atom].Relation)
		if p.pos < len(s.Types) {
			return s.Types[p.pos]
		}
		return ArgType{}
	}
	joined := map[string][][]Tuple{}
	for _, p := range prefixes {
		var origs [][]Value // the values of c's Bindings agreeing with p, or one empty set when c has none
		if len(c.Inputs) == 0 {
			origs = [][]Value{nil}
		}
		for _, b := range c.Bindings {
			agrees := true
			for j, n := range c.Inputs {
				if at, ok := inPrefix[n]; ok && j < len(b) {
					t := typeOf(c.Atoms, at)
					agrees = agrees && joinKey(p[at.atom].Vals[at.pos], t) == joinKey(b[j], t)
				}
			}
			if agrees {
				origs = append(origs, b)
			}
		}
		seen := map[string]bool{}
		for _, b := range origs {
			vals := make([]Value, len(rest.Inputs))
			for j, n := range rest.Inputs {
				if at, ok := inPrefix[n]; ok {
					vals[j] = p[at.atom].Vals[at.pos]
				} else {
					vals[j] = b[slices.Index(c.Inputs, n)]
				}
			}
			key := f.bindingKey(rest, vals)
			if seen[key] {
				continue
			}
			seen[key] = true
			if _, ok := joined[key]; !ok {
				rest.Bindings = append(rest.Bindings, vals)
			}
			joined[key] = append(joined[key], p)
		}
	}
	return &rest, joined
}

// membersPush reports whether every member pushes c.
func (f *Federation) membersPush(c Conjunction) bool {
	for _, m := range f.members {
		ps, ok := m.Source.(PushSource)
		if !ok || !ps.Pushes(c) {
			return false
		}
	}
	return true
}

// keyOf is the key of a row of c's matches at c's Inputs, each value read where the input first
// appears in c's atoms, as bindingKey keys a binding.
func (f *Federation) keyOf(c Conjunction, row []Tuple) string {
	vals := make([]Value, len(c.Inputs))
	for j, n := range c.Inputs {
	find:
		for ai, a := range c.Atoms {
			for pos, x := range a.Args {
				if x.Var == n {
					vals[j] = row[ai].Vals[pos]
					break find
				}
			}
		}
	}
	return f.bindingKey(c, vals)
}

// bindingKey keys vals, a binding of c's Inputs, each as the argument where it first appears in c's
// atoms reads it (joinKey).
func (f *Federation) bindingKey(c Conjunction, vals []Value) string {
	var sb strings.Builder
	for j, n := range c.Inputs {
		var at ArgType
	find:
		for _, a := range c.Atoms {
			for pos, x := range a.Args {
				if x.Var == n {
					if s, _ := f.Schema(a.Relation); pos < len(s.Types) {
						at = s.Types[pos]
					}
					break find
				}
			}
		}
		k := joinKey(vals[j], at)
		fmt.Fprintf(&sb, "%d:%s", len(k), k)
	}
	return sb.String()
}

// joinKey keys v as an argument of type at reads it, the way a member compares it (see LookupSource):
// a number by its value, text in a number argument by the number it spells, a number in a text
// argument by its text, and -0 as 0. Units don't count, as they don't in equality.
func joinKey(v Value, at ArgType) string {
	num := func(n float64) string {
		if n == 0 {
			n = 0
		}
		return "\x00n" + ftoa(n)
	}
	switch {
	case v.Absent:
		return "\x00a"
	case at.Type == TypeNumber:
		if v.Num != nil {
			return num(*v.Num)
		}
		if n, err := strconv.ParseFloat(v.S, 64); err == nil {
			return num(n)
		}
	case at.Type == TypeString || at.Kind != "" || at.KindFrom != "":
	case v.Num != nil:
		return num(*v.Num)
	}
	return v.S
}

// routed is c for member i: c itself when its Holds allows c's first atom, or with Bindings, c with
// only the bindings under whose values Holds allows it. ok is false when it allows none.
func (f *Federation) routed(i int, c Conjunction) (Conjunction, bool) {
	if len(c.Inputs) == 0 || f.members[i].Holds == nil {
		return c, f.holds(i, c.Atoms[0])
	}
	var kept [][]Value
	for _, b := range c.Bindings {
		if f.holds(i, c.Atoms[0].under(c.Inputs, b)) {
			kept = append(kept, b)
		}
	}
	if len(kept) == 0 {
		return c, false
	}
	c.Bindings = kept
	return c, true
}

// under is a with each variable in inputs replaced by its value in vals, in inputs' order.
func (a PushAtom) under(inputs []int, vals []Value) PushAtom {
	out := PushAtom{Relation: a.Relation, Args: make([]PushArg, len(a.Args))}
	for pos, x := range a.Args {
		out.Args[pos] = x
		if j := slices.Index(inputs, x.Var); x.Var > 0 && j >= 0 && j < len(vals) {
			v := vals[j]
			out.Args[pos] = PushArg{Const: &v}
		}
	}
	return out
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

// Walks implements WalkSource: a Federation walks rel when every member serving it does.
func (f *Federation) Walks(rel string, from, to int, bound []int) bool {
	serving := f.serving[rel]
	for _, i := range serving {
		ws, ok := f.members[i].Source.(WalkSource)
		if !ok || !ws.Walks(rel, from, to, bound) {
			return false
		}
	}
	return len(serving) > 0
}

// Walk implements WalkSource across the members (#239): each start value goes to the members whose
// Holds allows it, each member walks from its values in one call, and a value a member reached goes on
// to the other members whose Holds allows it, until no member has a value it hasn't walked. Each
// member walks a value once, so a walk staying in one store costs one call, and one crossing stores a
// call per store per crossing. Every member serving the relation is asked when Holds is nil. Each
// tuple is cited to its member, and a member's failure is wrapped with its name.
func (f *Federation) Walk(ctx context.Context, w Walk) iter.Seq2[Tuple, error] {
	return func(yield func(Tuple, error) bool) {
		serving := f.serving[w.Relation]
		walked := make([]map[string]bool, len(f.members))
		for _, i := range serving {
			walked[i] = map[string]bool{}
		}
		pending := map[int][]Value{}
		route := func(v Value, from int) {
			bound := make(map[int]Value, len(w.Bound)+1)
			for p, c := range w.Bound {
				bound[p] = c
			}
			bound[w.From] = v
			for _, i := range serving {
				k := walkKey(v)
				if i == from || walked[i][k] {
					continue
				}
				if h := f.members[i].Holds; h == nil || h(w.Relation, bound) {
					walked[i][k] = true
					pending[i] = append(pending[i], v)
				}
			}
		}
		for _, v := range w.Start {
			route(v, -1)
		}
		for len(pending) > 0 {
			for _, i := range serving {
				vals, ok := pending[i]
				if !ok {
					continue
				}
				delete(pending, i)
				ws := f.members[i].Source.(WalkSource)
				mw := w
				mw.Start = vals
				for t, err := range ws.Walk(ctx, mw) {
					if err != nil {
						yield(Tuple{}, fmt.Errorf("member %s: %w", f.name(i), err))
						return
					}
					if w.From < len(t.Vals) {
						walked[i][walkKey(t.Vals[w.From])] = true
					}
					if w.To < len(t.Vals) {
						walked[i][walkKey(t.Vals[w.To])] = true
						route(t.Vals[w.To], i)
					}
					if !yield(f.cite(i, t), nil) {
						return
					}
				}
			}
		}
	}
}

// walkKey keys a value a member has walked, a number by its value.
func walkKey(v Value) string {
	switch {
	case v.Absent:
		return "\x00a"
	case v.Num != nil:
		return "\x00n" + ftoa(*v.Num)
	}
	return v.S
}

// Summarizes implements SummarySource: a Federation of one member summarizes what it does, and one of
// several a conjunction of one atom, every member serving it summarizing too. A match of one atom is
// one fact, held by one member, so the members' rows together hold every match once. A join could
// pair facts across members, and is pushed (Pushes) or solved instead.
func (f *Federation) Summarizes(c Conjunction, s Summary) bool {
	ok := func(src Source) bool { ss, ok := src.(SummarySource); return ok && ss.Summarizes(c, s) }
	if len(f.members) == 1 {
		return ok(f.members[0].Source)
	}
	if f.single(c, ok) {
		return true
	}
	// A match spanning members comes back as a row of its own, whose numbers the Federation can't read
	// as their types to reduce, so a join's summary asks for values, not extremes.
	return len(s.Extremes) == 0 && f.across(c, ok)
}

// Summarize implements SummarySource: each member whose Holds allows c's first atom summarizes its
// own matches, and its rows' citations name it. A join of a partitioned Federation's members adds the
// matches spanning members (see spanning), each a row of one match.
func (f *Federation) Summarize(ctx context.Context, c Conjunction, s Summary) iter.Seq2[SummaryRow, error] {
	return func(yield func(SummaryRow, error) bool) {
		for _, i := range f.serving[c.Atoms[0].Relation] {
			if len(f.members) > 1 && !f.holds(i, c.Atoms[0]) {
				continue
			}
			ss, ok := f.members[i].Source.(SummarySource)
			if !ok {
				yield(SummaryRow{}, fmt.Errorf("member %s doesn't summarize", f.name(i)))
				return
			}
			for row, err := range ss.Summarize(ctx, c, s) {
				if err != nil {
					yield(SummaryRow{}, fmt.Errorf("member %s: %w", f.name(i), err))
					return
				}
				if name := f.members[i].Name; name != "" {
					cites := make([]string, len(row.Cites))
					for k, ct := range row.Cites {
						cites[k] = name + ":" + ct
					}
					row.Cites = cites
				}
				if !yield(row, nil) {
					return
				}
			}
		}
		if len(f.members) > 1 && len(c.Atoms) > 1 {
			f.spanning(ctx, c, func(row []Tuple) bool { return yield(summaryRowOf(c, s, row), nil) }, func(err error) { yield(SummaryRow{}, err) })
		}
	}
}

// single reports whether c is one atom every member serving it takes, as ok says.
func (f *Federation) single(c Conjunction, ok func(Source) bool) bool {
	if len(c.Atoms) != 1 {
		return false
	}
	serving := f.serving[c.Atoms[0].Relation]
	for _, i := range serving {
		if !ok(f.members[i].Source) {
			return false
		}
	}
	return len(serving) > 0
}

// across reports whether a partitioned Federation answers a summary or page of c, a join, across its
// members: every member serves every relation of c, takes it as ok says, and pushes its prefixes alone
// (Conjunction.PrefixesOnly), from which spanning finds the matches no one member holds.
func (f *Federation) across(c Conjunction, ok func(Source) bool) bool {
	if !f.partitioned || len(c.Atoms) < 2 {
		return false
	}
	for _, a := range c.Atoms {
		if len(f.serving[a.Relation]) != len(f.members) {
			return false
		}
	}
	p := prefixesOf(c)
	for _, m := range f.members {
		ps, isPush := m.Source.(PushSource)
		if !ok(m.Source) || !isPush || !ps.Pushes(p) {
			return false
		}
	}
	return true
}

// prefixesOf is c asking for its prefixes alone, with no page.
func prefixesOf(c Conjunction) Conjunction {
	return Conjunction{Atoms: c.Atoms, Compare: c.Compare, Partial: true, PrefixesOnly: true}
}

// spanning yields every match of c whose facts lie in more than one member (#253): each member whose
// Holds allows c's first atom sends its prefixes alone, and each is continued in the other members, as
// a partitioned push does (pushAcross). A member's own whole matches it summarizes or pages itself.
func (f *Federation) spanning(ctx context.Context, c Conjunction, yield func([]Tuple) bool, fail func(error)) {
	p := prefixesOf(c)
	first := c.Atoms[0]
	for _, i := range f.serving[first.Relation] {
		if !f.holds(i, first) {
			continue
		}
		var prefixes [][]Tuple
		ok := f.pushMember(ctx, i, p, nil, func(row []Tuple, err error) bool {
			switch {
			case err != nil:
				fail(err)
				return false
			case len(row) == 0 || len(row) >= len(c.Atoms):
				fail(fmt.Errorf("member %s: a prefix of %d tuples for %d atoms", f.name(i), len(row), len(c.Atoms)))
				return false
			}
			prefixes = append(prefixes, row)
			return true
		})
		if !ok {
			return
		}
		more := f.continuePrefixes(ctx, c, i, prefixes, func(whole []Tuple, err error) bool {
			if err != nil {
				fail(err)
				return false
			}
			return yield(whole)
		})
		if !more {
			return
		}
	}
}

// summaryRowOf is a match spanning members as a summary row of one match: its values at s's Group and
// Values as stored, and every fact's citations.
func summaryRowOf(c Conjunction, s Summary, row []Tuple) SummaryRow {
	vals := map[int]Value{}
	for k, a := range c.Atoms {
		for pos, x := range a.Args {
			if _, ok := vals[x.Var]; x.Var > 0 && !ok && pos < len(row[k].Vals) {
				vals[x.Var] = row[k].Vals[pos]
			}
		}
	}
	r := SummaryRow{Matches: 1}
	for _, v := range s.Group {
		r.Group = append(r.Group, vals[v])
	}
	for _, v := range s.Values {
		r.Values = append(r.Values, vals[v])
	}
	for _, t := range row {
		r.Cites = append(r.Cites, t.Cites...)
	}
	return r
}

// WalksConjunction implements ConjunctionWalkSource for a Federation of one member that walks it. Over
// several, a step's facts could lie in different members, and the step is pushed per round instead.
func (f *Federation) WalksConjunction(c Conjunction, in, out int) bool {
	cw, ok := f.members[0].Source.(ConjunctionWalkSource)
	return len(f.members) == 1 && ok && cw.WalksConjunction(c, in, out)
}

// WalkConjunction implements ConjunctionWalkSource through the lone member, citing each tuple to it.
func (f *Federation) WalkConjunction(ctx context.Context, c Conjunction, in, out int, start []Value) iter.Seq2[[]Tuple, error] {
	return func(yield func([]Tuple, error) bool) {
		cw, ok := f.members[0].Source.(ConjunctionWalkSource)
		if len(f.members) != 1 || !ok {
			yield(nil, fmt.Errorf("federation: %d members can't walk a conjunction together", len(f.members)))
			return
		}
		for row, err := range cw.WalkConjunction(ctx, c, in, out, start) {
			if err != nil {
				yield(nil, fmt.Errorf("member %s: %w", f.name(0), err))
				return
			}
			if !yield(f.citedRow(0, nil, row), nil) {
				return
			}
		}
	}
}
