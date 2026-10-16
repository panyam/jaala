package datalog

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/panyam/jaala/ns"
)

// Language is Datalog as a module language for an ns.Vocabulary. A host registers it once, then adds
// modules written in it:
//
//	v.AddLanguage(datalog.Language)
//	v.AddModule("net", datalog.LanguageName, `has_test_point(?n: net) :- component.net(?tp, ?n), ...;`, "lib/net.dl")
//
// A module's text is rules only. Each rule head names a member of the module, written bare:
// `has_test_point(?n) :- ...` at "net" defines net.has_test_point. Inside the text a bare name is
// this module's member first, among members of every kind and from any module at the same path, and
// then the root's; a dotted name is always a full path. So in module "net", `pin_count(?n, ?c)`
// reads net.pin_count when that is registered, and `component.pin(...)` reads component.pin
// wherever it sits.
//
// A member whose name starts with "_" is private to its module: it gets no path, no query can name
// it, and another module's _helper is a different relation. A rule head may declare its arguments'
// types, `has_test_point(?n: net)`; an argument no head declares is inferred, and Check refuses a
// declaration its rules contradict. The comment lines directly above a member's first rule are its
// doc.
var Language ns.Language = language{}

// LanguageName is the name Language registers under, for ns.Vocabulary.AddModule.
const LanguageName = "datalog"

type language struct{}

func (language) Name() string { return LanguageName }

// Members parses a module and reports its public members. It refuses text holding a goal, a
// qualified rule head, and a member defined with two arities.
func (language) Members(text string) ([]ns.MemberDecl, error) {
	rules, err := ParseRules(text)
	if err != nil {
		return nil, err
	}
	docs := moduleDocs(text)
	var out []ns.MemberDecl
	arity := map[string]int{}
	at := map[string]int{} // public members only: index into out
	for _, rule := range rules {
		name := rule.Head.Relation
		if strings.Contains(name, ".") {
			return nil, checkErr(KindDefinition, fmt.Sprintf("query: rule head %q is qualified; a module defines its own members, written bare", name))
		}
		if prev, ok := arity[name]; ok && prev != len(rule.Head.Args) {
			return nil, checkErr(KindArity, fmt.Sprintf("query: the module defines %q with %d and %d args (arity must be consistent)", name, prev, len(rule.Head.Args)))
		}
		arity[name] = len(rule.Head.Args)
		if strings.HasPrefix(name, "_") {
			continue
		}
		if i, ok := at[name]; ok {
			out[i].Definition = append(out[i].Definition, rule.String())
			continue
		}
		at[name] = len(out)
		out = append(out, ns.MemberDecl{Name: name, Arity: len(rule.Head.Args), Doc: docs[name], Definition: []string{rule.String()}})
	}
	return out, nil
}

// Check validates every Datalog module of v together and returns each public member's signature.
func (language) Check(v *ns.Vocabulary) (map[string][]ns.ArgSig, error) {
	res, err := resolved(v)
	if err != nil {
		return nil, err
	}
	return res.sigs, nil
}

// A resolution is every Datalog module's rules with their names resolved to paths, and the public
// members' signatures. It depends only on the vocabulary, so it is memoized there and shared by every
// Base and query over it.
type resolution struct {
	// byHead is every module rule by the relation it defines, private members included under their
	// linked names, so Link can load exactly the relations a query reaches.
	byHead map[string][]Rule
	sigs   map[string][]ns.ArgSig
}

type resolutionKey struct{}

// resolutions counts how many times modules have been resolved, for a test asserting it happens once
// per vocabulary however many Bases share it.
var resolutions atomic.Int64

func resolved(v *ns.Vocabulary) (*resolution, error) {
	out, err := v.Memo(resolutionKey{}, func() (any, error) {
		resolutions.Add(1)
		return resolveAll(v)
	})
	if err != nil {
		return nil, err
	}
	return out.(*resolution), nil
}

// resolveAll is the semantic pass over every Datalog module: it resolves each name the modules'
// rules read to a path, and validates all module rules together, as one program, against the
// vocabulary. That catches a name no one registered, a wrong arity, an unbound head variable, unsafe
// negation and recursion through negation, including across modules, and a declared type the rules
// contradict. A vocabulary holding no base relation cannot call a name unknown, so that one check
// stands down until it has some.
func resolveAll(v *ns.Vocabulary) (*resolution, error) {
	mods := v.Modules()
	res := &resolution{byHead: map[string][]Rule{}}
	perModule := make([][]Rule, len(mods)) // nil for a module in another language
	privates := map[string]bool{}
	var all []Rule
	// fail attributes a failure to the module responsible, so a host can name its file (#30).
	fail := func(i int, err error) error {
		return &ns.ModuleError{Module: i, Path: mods[i].Path, Origin: mods[i].Origin, Err: err}
	}
	for i, m := range mods {
		if m.Language != LanguageName {
			continue
		}
		rules, err := ParseRules(m.Text)
		if err == nil {
			rules, err = lowerRules(rules, "m"+strconv.Itoa(i))
		}
		if err == nil {
			rules, err = lowerExprRules(rules, v)
		}
		if err == nil {
			// Folded before its signature is inferred, so a head's 1kV types and bounds the column
			// as the 1000 V it answers (#224).
			var q Query
			q, err = foldUnits(Query{Rules: rules}, v)
			rules = q.Rules
		}
		if err != nil {
			return nil, fail(i, fmt.Errorf("%w (in module %q)", err, m.Path))
		}
		own := map[string]bool{}
		for _, r := range rules {
			if strings.HasPrefix(r.Head.Relation, "_") {
				own[r.Head.Relation] = true
				privates[privateName(m.Path, r.Head.Relation, i)] = true
			}
		}
		for _, r := range rules {
			rr := resolveRule(v, m.Path, own, i, r)
			perModule[i] = append(perModule[i], rr)
			res.byHead[rr.Head.Relation] = append(res.byHead[rr.Head.Relation], rr)
		}
		all = append(all, perModule[i]...)
	}
	if len(all) == 0 {
		return res, nil
	}
	hasBase := len(v.BaseRelations()) > 0
	if hasBase {
		for i, rules := range perModule {
			for _, rule := range rules {
				for _, a := range ruleAtoms(rule.Body) {
					if !privates[a.Relation] && !isAggHelper(a.Relation) && !v.Has(a.Relation) {
						return nil, fail(i, checkErr(KindUnknown, fmt.Sprintf("query: module %q rule %q reads %s", mods[i].Path, displayName(rule.Head.Relation), v.Unknown(a.Relation))))
					}
				}
			}
		}
	}
	// Each rule is checked within its module first, so its failure names the module; what is left
	// for the whole program, such as recursion through negation across modules, belongs to no one
	// module and stays a plain error.
	b := newValidationBase(v)
	b.inputRels = inputRelations(all, v)
	for _, r := range all {
		b.idbArity[r.Head.Relation] = len(r.Head.Args)
	}
	for i, rules := range perModule {
		for _, r := range rules {
			if err := b.validateRule(r); err != nil {
				return nil, fail(i, err)
			}
		}
	}
	if _, _, err := b.checkRules(all); err != nil {
		return nil, err
	}
	// Every call to a rule taking an input binds it, so a library's mistake fails at Check (#200).
	if _, err := lowerInputs(Query{Rules: all}, nil, v); err != nil {
		return nil, err
	}
	t := newTyper(v, all)
	res.sigs = map[string][]ns.ArgSig{}
	for i, m := range mods {
		if m.Language != LanguageName {
			continue
		}
		for _, d := range m.Members {
			path := joinPath(m.Path, d.Name)
			if id, ok := v.DefiningModule(path); !ok || id != i {
				continue
			}
			sig, err := t.signature(path)
			if err != nil {
				return nil, fail(i, err)
			}
			res.sigs[path] = sig
		}
	}
	if hasBase {
		b.sigs = res.sigs
		for i, rules := range perModule {
			for _, rule := range rules {
				if err := b.checkLiterals(rule.Body.Literals); err != nil {
					return nil, fail(i, fmt.Errorf("%w (in module rule %q)", err, displayName(rule.Head.Relation)))
				}
				_, negs := splitNegations(rule.Body.Literals)
				if err := b.validateNegationsFrom(rule.Body, negs, inputsBound(rule, b.inputRels, b.reg)); err != nil {
					return nil, fail(i, fmt.Errorf("%w (in module rule %q)", err, displayName(rule.Head.Relation)))
				}
				if err := t.checkUnits(whereRule(rule), rule.Head.Relation, rule.Body); err != nil {
					return nil, fail(i, err)
				}
			}
		}
	}
	return res, nil
}

// resolveRule rewrites one rule of the module at path so every relation it names is a full path: its
// head the member it defines, and each body name by the lookup Language describes.
func resolveRule(v *ns.Vocabulary, path string, privates map[string]bool, id int, rule Rule) Rule {
	name := func(n string) string {
		switch {
		case strings.Contains(n, "."):
			return n
		case privates[n]:
			return privateName(path, n, id)
		}
		if p := joinPath(path, n); path != "" && v.Has(p) {
			return p
		}
		return n
	}
	out := Rule{Head: renameAtom(rule.Head, name), Hops: rule.Hops, HeadTypes: rule.HeadTypes}
	for _, lit := range rule.Body.Literals {
		switch {
		case lit.Pos != nil:
			a := renameAtom(*lit.Pos, name)
			out.Body.Literals = append(out.Body.Literals, Literal{Pos: &a})
		case lit.Neg != nil:
			a := renameAtom(*lit.Neg, name)
			out.Body.Literals = append(out.Body.Literals, Literal{Neg: &a})
		default:
			out.Body.Literals = append(out.Body.Literals, lit)
		}
	}
	return out
}

func renameAtom(a Atom, name func(string) string) Atom {
	return Atom{Relation: name(a.Relation), Args: a.Args}
}

// ruleAtoms returns the atoms of a body, positive and negated.
func ruleAtoms(b Body) []*Atom {
	var out []*Atom
	for _, l := range b.Literals {
		if l.Pos != nil {
			out = append(out, l.Pos)
		}
		if l.Neg != nil {
			out = append(out, l.Neg)
		}
	}
	return out
}

// privateSep marks a private member's linked name. The parser never accepts it in a relation name,
// so no query text can spell a private member, and the unit index keeps two units' privates apart.
const privateSep = "~"

func privateName(module, name string, id int) string {
	return joinPath(module, name) + privateSep + strconv.Itoa(id)
}

// displayName is a linked relation name as its author wrote it: a private member loses its unit
// suffix, so an error or a lint names `net._probe_count` rather than the linker's spelling.
func displayName(rel string) string {
	if i := strings.Index(rel, aggSep); i >= 0 {
		return rel[:i] // a body aggregate's relation, shown as its aggregate (see lowerBodyAggregates)
	}
	if i := strings.Index(rel, privateSep); i >= 0 {
		return rel[:i]
	}
	return rel
}

func joinPath(module, name string) string {
	if module == "" {
		return name
	}
	return module + "." + name
}

// displayNames maps displayName over names, sorted and deduped.
func displayNames(names []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, n := range names {
		if d := displayName(n); !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	sort.Strings(out)
	return out
}

// moduleDocs collects, for each member, the comment lines directly above the first rule that defines
// it. A blank line ends a comment block, so a file header is not mistaken for its first member's doc.
func moduleDocs(text string) map[string]string {
	docs := map[string]string{}
	var pending []string
	for _, line := range strings.Split(text, "\n") {
		tl := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(tl, "#"):
			pending = append(pending, strings.TrimSpace(tl[1:]))
			continue
		case tl == "":
			pending = nil
			continue
		}
		if open := strings.IndexByte(tl, '('); open > 0 && len(pending) > 0 {
			if name := strings.TrimSpace(tl[:open]); isRelation(name) {
				if _, ok := docs[name]; !ok {
					docs[name] = strings.Join(pending, " ")
				}
			}
		}
		pending = nil
	}
	return docs
}
