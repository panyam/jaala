package ns

import (
	"fmt"
	"strings"
)

// Changes is a change set: per base relation, the facts to add and the facts to remove (#287). The
// engine never writes to a Source, so a host edits its graph by building a Changes (from a UI's edit,
// or from Base.Diff between two versions) and applying it to its own store, or by reading it through
// an overlay over the store until it commits.
//
// A fact is its values, as Base.Diff compares them: a number by its value (-0 is 0, units aside), text
// by its text, and absent apart from both, so ns.N(1) and ns.S("1") are two facts. Build each value as
// its argument's declared type, as the engine reads it. Its ID and Cites travel with it.
//
// Operations apply in the order they are made, so the last one on a fact wins: adding a fact and then
// removing it removes it, and removing it then adding it adds it, whatever a store holds. The zero
// Changes is empty and ready to use. A Changes is not safe for concurrent use.
type Changes struct {
	rels  []string
	byRel map[string]*relChanges
}

// relChanges is one relation's operations, each fact's last, in the order the facts were first named.
type relChanges struct {
	keys  []string
	facts map[string]change
}

type change struct {
	t   Tuple
	add bool
}

// Add records that rel holds t.
func (c *Changes) Add(rel string, t Tuple) *Changes { return c.put(rel, t, true) }

// Remove records that rel doesn't hold t, under whatever ID or citations it was stored with.
func (c *Changes) Remove(rel string, t Tuple) *Changes { return c.put(rel, t, false) }

func (c *Changes) put(rel string, t Tuple, add bool) *Changes {
	if c.byRel == nil {
		c.byRel = map[string]*relChanges{}
	}
	r := c.byRel[rel]
	if r == nil {
		r = &relChanges{facts: map[string]change{}}
		c.byRel[rel] = r
		c.rels = append(c.rels, rel)
	}
	k := factKey(t.Vals)
	if _, ok := r.facts[k]; !ok {
		r.keys = append(r.keys, k)
	}
	r.facts[k] = change{t: t, add: add}
	return c
}

// Then is c followed by next, as one change set: what next does to a fact replaces what c did. A
// sequence of edits, each made over the last, composes this way, so a stack of change sets can be
// undone by dropping the top and composing the rest. Neither c nor next is changed.
func (c Changes) Then(next Changes) Changes {
	var out Changes
	for _, cs := range []Changes{c, next} {
		for _, rel := range cs.rels {
			r := cs.byRel[rel]
			for _, k := range r.keys {
				ch := r.facts[k]
				out.put(rel, ch.t, ch.add)
			}
		}
	}
	return out
}

// Relations lists the relations c names, in the order they were first named. A relation whose
// operations all cancel is still listed: its facts are named, as removals.
func (c Changes) Relations() []string { return append([]string(nil), c.rels...) }

// Added lists the facts c adds to rel, in the order they were first named.
func (c Changes) Added(rel string) []Tuple { return c.facts(rel, true) }

// Removed lists the facts c removes from rel, in the order they were first named.
func (c Changes) Removed(rel string) []Tuple { return c.facts(rel, false) }

func (c Changes) facts(rel string, add bool) []Tuple {
	r := c.byRel[rel]
	if r == nil {
		return nil
	}
	var out []Tuple
	for _, k := range r.keys {
		if ch := r.facts[k]; ch.add == add {
			out = append(out, ch.t)
		}
	}
	return out
}

// Empty reports whether c names no fact.
func (c Changes) Empty() bool { return len(c.rels) == 0 }

// Apply is rel's facts once c is applied to base: base's facts, in its order, but those c names, then
// the facts c adds. A fact c names is replaced wherever base holds it, every copy, so an added fact
// base already held is held once, with the ID and citations c gives it.
func (c Changes) Apply(rel string, base []Tuple) []Tuple {
	r := c.byRel[rel]
	if r == nil {
		return base
	}
	out := make([]Tuple, 0, len(base))
	for _, t := range base {
		if _, ok := r.facts[factKey(t.Vals)]; !ok {
			out = append(out, t)
		}
	}
	return append(out, c.Added(rel)...)
}

// Check refuses a change set v couldn't hold: a relation that isn't one of v's base relations
// (a derived relation's facts follow from its rules, so they never change directly), or a fact
// whose arity isn't its relation's.
func (c Changes) Check(v *Vocabulary) error {
	for _, rel := range c.rels {
		s, ok := v.Schema(rel)
		if !ok {
			if _, derived := v.DefiningModule(rel); derived {
				return fmt.Errorf("query: change set: %s is a derived relation, and only a base relation's facts change", rel)
			}
			return fmt.Errorf("query: change set: %s", v.Unknown(rel))
		}
		r := c.byRel[rel]
		for _, k := range r.keys {
			ch := r.facts[k]
			if len(ch.t.Vals) != s.Arity {
				verb := "remove"
				if ch.add {
					verb = "add"
				}
				return fmt.Errorf("query: change set: %s takes %d args, and a fact to %s has %d", rel, s.Arity, verb, len(ch.t.Vals))
			}
		}
	}
	return nil
}

// factKey keys a fact by its values, each as joinKey keys a value no type is declared for.
func factKey(vals []Value) string {
	var sb strings.Builder
	for _, v := range vals {
		k := joinKey(v, ArgType{})
		fmt.Fprintf(&sb, "%d:%s", len(k), k)
	}
	return sb.String()
}
