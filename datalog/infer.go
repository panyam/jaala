package datalog

import (
	"fmt"
	"github.com/panyam/jaala/ns"
	"slices"
)

// typer works out what the arguments of derived relations denote, from the types base relations and
// predicates declare, through the rules that read them. It is a port of agni's column typing (agni
// issue 654), whose rules each fix a failure the obvious walk has:
//
//   - It follows rules more than one hop. A rule defined in terms of another is ordinary, and stopping
//     at the first would be a partial answer that looks complete.
//   - Rules that disagree about a head position give it no type, rather than the first rule's. A
//     column typed from whichever rule was written first is wrong for the other rule's rows.
//   - A rule that reaches back into a relation already being typed abstains rather than vetoing, so a
//     transitive closure is typed by its base case instead of losing its type to its recursive one.
//   - A kind that varies per row stays per row through a head, when the head carries the argument
//     that says the kind. agni collapsed this case to no type; a head declaring KindFrom can carry it.
type typer struct {
	reg   *ns.Vocabulary
	rules map[string][]Rule // by head relation, names resolved to paths
	// ignoreDecl is a relation whose declared head types are set aside, so its declarations can be
	// compared with what its rules alone produce.
	ignoreDecl string
	probe      *headProbe
}

// headProbe follows one head inference through aliases. Recursion keeps the previous conservative
// inference when the walk also uses numeric head constants.
type headProbe struct {
	legacy                bool
	sawNumeric, recursive bool
}

func newTyper(reg *ns.Vocabulary, rules []Rule) *typer {
	t := &typer{reg: reg, rules: map[string][]Rule{}}
	for _, r := range rules {
		t.rules[r.Head.Relation] = append(t.rules[r.Head.Relation], r)
	}
	return t
}

// varType is what a variable of body denotes, in body's own terms: a fixed kind, the variable whose
// per-row value is its kind, or a kind with the term that locates it.
type varType struct {
	ns.ArgType // Kind, Type, Unit and Domain; KindFrom and Owner are carried by the fields below
	kindVar    Var
	owner      Term
}

// ofVar types a variable by the positive atoms that bind it. An entity binding wins over a scalar
// one, so a variable used as a net in one atom and as a plain string in another is a net. Base
// relations and predicates are consulted before derived relations, so a variable a declaration can
// type keeps that answer whatever the rules say.
func (t *typer) ofVar(v Var, body Body, seen map[string]bool) varType {
	var scalar varType
	haveScalar := false
	for _, derived := range []bool{false, true} {
		for _, lit := range body.Literals {
			a := lit.Pos
			if a == nil || t.isDerived(a.Relation) != derived {
				continue
			}
			for j, arg := range a.Args {
				if arg.Var != v {
					continue
				}
				labels, types, ok := t.argTypes(a.Relation, seen)
				if !ok || j >= len(types) {
					continue
				}
				at := types[j]
				switch {
				case at.KindFrom != "":
					kt, ok := argAt(a, labels, at.KindFrom)
					if !ok {
						continue
					}
					if kt.Var != "" {
						return varType{kindVar: kt.Var}
					}
					// A constant kind types the column outright, provided it is a kind the relation
					// can hold. Anything else stays untyped rather than handing a host a kind it has
					// no meaning for.
					if kt.Const != nil {
						if i := slices.Index(labels, at.KindFrom); i >= 0 && i < len(types) && slices.Contains(types[i].Domain, kt.Const.S) {
							return varType{ArgType: ns.ArgType{Kind: kt.Const.S}}
						}
					}
				case at.Owner != "":
					if ref, ok := argAt(a, labels, at.Owner); ok {
						return varType{ArgType: ns.ArgType{Kind: at.Kind, Type: at.Type, Unit: at.Unit, Domain: at.Domain}, owner: ref}
					}
				case at.Kind != "":
					return varType{ArgType: at}
				case !haveScalar && !at.IsZero():
					scalar, haveScalar = varType{ArgType: at}, true
				}
			}
		}
	}
	return scalar
}

// isDerived reports whether rel is defined by the rules this typer holds.
func (t *typer) isDerived(rel string) bool { _, ok := t.rules[rel]; return ok }

// argTypes returns what a relation says about its arguments: a base relation's or predicate's
// declaration, or a derived relation's declared or inferred head types. A derived relation already
// being typed further up the walk answers nothing, which is what ends a cycle.
func (t *typer) argTypes(rel string, seen map[string]bool) ([]string, []ns.ArgType, bool) {
	if s, ok := t.reg.Schema(rel); ok {
		return s.Labels, s.Types, true
	}
	if b, ok := t.reg.Predicate(rel); ok {
		return b.Labels, b.Types, true
	}
	if !t.isDerived(rel) || seen[rel] {
		return nil, nil, false
	}
	labels := t.headLabels(rel)
	declared, _ := t.declared(rel)
	types := make([]ns.ArgType, len(labels))
	for j := range labels {
		if rel != t.ignoreDecl && j < len(declared) && !declared[j].IsZero() {
			types[j] = declared[j]
		} else {
			types[j] = t.ofHead(rel, j, seen)
		}
	}
	return labels, types, true
}

// ofHead infers argument j of a derived relation from every rule defining it, in the relation's own
// labels. It is agni's headKind, generalized to carry a per-row kind and an owner when the head
// holds the argument they point at.
func (t *typer) ofHead(rel string, j int, seen map[string]bool) ns.ArgType {
	if t.probe != nil {
		return t.inferHead(rel, j, seen)
	}
	probe := headProbe{}
	nested := *t
	nested.probe = &probe
	got := nested.inferHead(rel, j, seen)
	if probe.sawNumeric && probe.recursive {
		probe.legacy = true
		got = nested.inferHead(rel, j, seen)
	}
	return got
}

func (t *typer) inferHead(rel string, j int, seen map[string]bool) ns.ArgType {
	next := make(map[string]bool, len(seen)+1)
	for k := range seen {
		next[k] = true
	}
	next[rel] = true
	labels := t.headLabels(rel)

	var got ns.ArgType
	found, untyped := false, false
	var domain []string
	closed := true
	for _, r := range t.rules[rel] {
		if j >= len(r.Head.Args) {
			continue
		}
		hv := r.Head.Args[j]
		if hv.Agg != nil {
			got, found = aggregateType(*hv.Agg, t.ofVar(hv.Agg.Var, r.Body, next)), true
			closed = false
			continue // an aggregating relation has only this rule (see checkRules)
		}
		var at ns.ArgType
		if hv.Const != nil {
			domain = unionSorted(domain, []string{hv.Const.S})
			if hv.Const.Num == nil || t.probe.legacy {
				untyped = true // text, or a constant under recursive inference, does not fix a type
				continue
			}
			t.probe.sawNumeric = true
			at = ns.ArgType{Type: ns.TypeNumber, Unit: hv.Const.BaseUnit}
		} else {
			if hv.Var == "" || hv.Var == "_" {
				closed = false
				continue
			}
			if bodyTouches(r.Body, next) {
				t.probe.recursive = true
				closed = false
				continue // recursive: no new information, so it abstains rather than vetoing
			}
			vt := t.ofVar(hv.Var, r.Body, next)
			if len(vt.Domain) == 0 {
				closed = false
			} else {
				domain = unionSorted(domain, vt.Domain)
			}
			at = ns.ArgType{Kind: vt.Kind, Type: vt.Type, Unit: vt.Unit}
			if vt.kindVar != "" {
				if m := headIndex(r.Head, vt.kindVar); m >= 0 {
					at.KindFrom = labels[m]
				}
			}
			if vt.owner != (Term{}) {
				if m := headIndex(r.Head, vt.owner.Var); vt.owner.Var != "" && m >= 0 {
					at.Owner = labels[m]
				} else {
					at = ns.ArgType{} // an entity the head cannot locate names nothing a reader can act on
				}
			}
		}
		if found && !sameShape(got, at) {
			untyped = true
		}
		got, found = at, true
	}
	if untyped || !found {
		got = ns.ArgType{}
	}
	if closed && len(domain) > 0 {
		got.Domain = domain
	}
	return got
}

// aggregateType is what an aggregate yields over a variable of type vt: list a string, count a
// number of no unit, and sum, min and max a number in the unit the variable carries. It names no
// entity, whatever it reduces: count(?ref) counts parts, it doesn't name one.
func aggregateType(a Aggregate, vt varType) ns.ArgType {
	switch a.Func {
	case "list":
		return ns.ArgType{Type: ns.TypeString}
	case "sum", "min", "max":
		if vt.Type == ns.TypeNumber {
			return ns.ArgType{Type: ns.TypeNumber, Unit: vt.Unit}
		}
	}
	return ns.ArgType{Type: ns.TypeNumber}
}

// headLabels names a derived relation's arguments by the variables of the first rule that declares a
// type, or else of its first rule. A position holding a constant, a wildcard or a repeated variable
// is named argN.
func (t *typer) headLabels(rel string) []string {
	rules := t.rules[rel]
	src := rules[0]
	for _, r := range rules {
		if len(r.HeadTypes) > 0 {
			src = r
			break
		}
	}
	out := make([]string, len(src.Head.Args))
	used := map[string]bool{}
	for i, a := range src.Head.Args {
		name := string(a.Var)
		if name == "" || name == "_" || used[name] {
			name = fmt.Sprintf("arg%d", i)
		}
		used[name] = true
		out[i] = name
	}
	return out
}

// declared merges what a derived relation's rule heads declare, by position, in the relation's own
// labels. Two rules declaring different types for one argument is an error.
func (t *typer) declared(rel string) ([]ns.ArgType, error) {
	labels := t.headLabels(rel)
	var out []ns.ArgType
	for _, r := range t.rules[rel] {
		for i, d := range r.HeadTypes {
			if d.IsZero() || i >= len(labels) {
				continue
			}
			// A declaration names other arguments by ITS rule's variables; restate it in the labels.
			for _, ref := range []*string{&d.KindFrom, &d.Owner} {
				if *ref == "" {
					continue
				}
				m := headIndex(r.Head, Var(*ref))
				if m < 0 {
					return nil, fmt.Errorf("query: %s's type %q names ?%s, which is not in its head", displayName(rel), d, *ref)
				}
				*ref = labels[m]
			}
			if out == nil {
				out = make([]ns.ArgType, len(labels))
			}
			if !out[i].IsZero() && (!sameShape(out[i], d) || !slices.Equal(out[i].Domain, d.Domain)) {
				return nil, fmt.Errorf("query: %s declares its %q argument as both %q and %q", displayName(rel), labels[i], out[i], d)
			}
			out[i] = d
		}
	}
	return out, nil
}

// signature is a derived relation's full signature: each argument declared where a head declares it
// and inferred elsewhere. It fails when two heads declare an argument differently, or when a
// declaration says something the relation's rules cannot produce.
func (t *typer) signature(rel string) ([]ns.ArgSig, error) {
	labels := t.headLabels(rel)
	declared, err := t.declared(rel)
	if err != nil {
		return nil, err
	}
	t.ignoreDecl = rel
	defer func() { t.ignoreDecl = "" }()
	out := make([]ns.ArgSig, len(labels))
	for j, name := range labels {
		inferred := t.ofHead(rel, j, nil)
		if j < len(declared) && !declared[j].IsZero() {
			if why := contradicts(declared[j], inferred); why != "" {
				return nil, fmt.Errorf("query: %s declares ?%s: %s, but its rules make it %s", displayName(rel), name, declared[j], why)
			}
			out[j] = ns.ArgSig{Name: name, ArgType: declared[j]}
			continue
		}
		fixed := t.aggregateFixes(rel, j) || t.numericConstantFixes(rel, j, inferred)
		out[j] = ns.ArgSig{Name: name, ArgType: inferred, Inferred: !fixed}
	}
	return out, nil
}

// numericConstantFixes reports whether a numeric head constant fixes the column's agreed number
// type. A column that stays untyped despite a numeric constant is still inferred.
func (t *typer) numericConstantFixes(rel string, j int, inferred ns.ArgType) bool {
	if inferred.Type != ns.TypeNumber {
		return false
	}
	for _, r := range t.rules[rel] {
		if j < len(r.Head.Args) && r.Head.Args[j].Const != nil && r.Head.Args[j].Const.Num != nil {
			return true
		}
	}
	return false
}

// aggregateFixes reports whether a head aggregate at position j decides its column's type the way a
// declaration would (#78): count and list always, sum, min and max only over a typed number, whose
// unit they carry. Over an untyped column those three still yield numbers, but in a unit nobody stated.
func (t *typer) aggregateFixes(rel string, j int) bool {
	for _, r := range t.rules[rel] {
		if j >= len(r.Head.Args) || r.Head.Args[j].Agg == nil {
			continue
		}
		a := *r.Head.Args[j].Agg
		return a.Func == "count" || a.Func == "list" || t.ofVar(a.Var, r.Body, map[string]bool{rel: true}).Type == ns.TypeNumber
	}
	return false
}

// contradicts reports how an inferred type rules out a declared one, or "". Only what inference
// determined can contradict: a declaration may say more than the rules show, such as a kind for an
// argument read from an untyped column, but not something else.
func contradicts(d, i ns.ArgType) string {
	switch {
	case (i.Kind != "" || i.KindFrom != "") && (i.Kind != d.Kind || i.KindFrom != d.KindFrom || i.Owner != d.Owner):
		return describe(i)
	case i.Type != "" && d.Type != "" && i.Type != d.Type, i.Unit != "" && d.Unit != "" && i.Unit != d.Unit:
		return describe(i)
	case len(i.Domain) > 0 && len(d.Domain) > 0:
		for _, v := range i.Domain {
			if !slices.Contains(d.Domain, v) {
				return fmt.Sprintf("able to hold %q", v)
			}
		}
	}
	return ""
}

func describe(t ns.ArgType) string {
	if t.KindFrom != "" {
		return "a kind per row, from ?" + t.KindFrom
	}
	return t.String()
}

// sameShape reports whether two types say the same thing, ignoring Domain, which inference merges
// separately.
func sameShape(t, o ns.ArgType) bool {
	return t.Kind == o.Kind && t.KindFrom == o.KindFrom && t.Owner == o.Owner && t.Type == o.Type && t.Unit == o.Unit
}

// bodyTouches reports whether any positive literal names a relation being typed, which is how a
// cycle is recognised without walking into it.
func bodyTouches(body Body, seen map[string]bool) bool {
	for _, lit := range body.Literals {
		if lit.Pos != nil && seen[lit.Pos.Relation] {
			return true
		}
	}
	return false
}

// argAt returns the atom's argument at the position labelled label.
func argAt(a *Atom, labels []string, label string) (Term, bool) {
	for j, l := range labels {
		if l == label && j < len(a.Args) {
			return a.Args[j], true
		}
	}
	return Term{}, false
}

// headIndex is the position of variable v in a head, or -1.
func headIndex(head Atom, v Var) int {
	for i, a := range head.Args {
		if a.Var == v {
			return i
		}
	}
	return -1
}

// A ColumnKind is what one answer column denotes, for a host deciding what a cell is, such as
// whether it can be clicked. Exactly one of the three shapes holds, or none for a plain value.
type ColumnKind struct {
	// Kind is the entity kind of every cell in the column.
	Kind string
	// KindFrom is the variable whose value in each row is that row's kind. It must be read from the
	// solution rather than the projection when it is not itself a column.
	KindFrom Var
	// Owner is the term that locates a Kind entity found only through another, such as the component
	// a pin belongs to: a variable to read from each row, or a constant.
	Owner Term
	// Type and Unit describe a column that is a plain value.
	Type, Unit string
}

// ColumnKinds reports what each answer column of q denotes, in Select order (or goal-variable order
// when Select is empty), read from what the vocabulary's relations declare and inferred through the
// query's rules and the modules it links. An aggregate is a number whatever it reduces: count(?ref)
// counts parts, it does not name one.
func ColumnKinds(q Query, reg *ns.Vocabulary) ([]ColumnKind, error) {
	q, err := Link(q, reg)
	if err != nil {
		return nil, err
	}
	t := newTyper(reg, q.Rules)
	sel := q.Select
	if len(sel) == 0 {
		sel = defaultSelect(q.Goal)
	}
	out := make([]ColumnKind, len(sel))
	for i, s := range sel {
		if s.Agg != nil {
			out[i] = ColumnKind{Type: ns.TypeNumber}
			if s.Agg.Func == "list" {
				out[i] = ColumnKind{Type: ns.TypeString}
			}
			continue
		}
		if s.Var == "" {
			continue
		}
		vt := t.ofVar(s.Var, q.Goal, nil)
		out[i] = ColumnKind{Kind: vt.Kind, KindFrom: vt.kindVar, Owner: vt.owner, Type: vt.Type, Unit: vt.Unit}
	}
	return out, nil
}
