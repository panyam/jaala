package nl

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/panyam/jaala/ns"
)

// A GrammarOption narrows GrammarFor.
type GrammarOption func(*grammarOpts)

type grammarOpts struct{ goalOnly bool }

// GoalOnly leaves rules out of the grammar, so every call names a member of the vocabulary: the
// strictest form, for a small model asking one question.
func GoalOnly() GrammarOption { return func(o *grammarOpts) { o.goalOnly = true } }

// GrammarFor is Grammar narrowed to v (#324): a call names one of v's members at an arity it takes,
// and an argument whose type is a closed domain is a variable or one of the domain's values, so a model
// decoding under it can't write an unknown member, a wrong argument count or a misspelled constant.
// A program's own rules are named apart from every name at v's root, so a call that isn't a member's
// is a call to one of them; the grammar can't tell whether that rule exists, which Validate still
// checks. A domain value a program can't spell (holding a quote, a backslash or a control character)
// is left out. Like the card, it is built once per state of v, and it returns v's Check error when
// its modules don't check.
func GrammarFor(v *ns.Vocabulary, opts ...GrammarOption) (string, error) {
	var o grammarOpts
	for _, opt := range opts {
		opt(&o)
	}
	g, err := v.Memo(vocabGrammarKey{o}, func() (any, error) {
		grammarBuilds.Add(1)
		rules, err := vocabRules(v, o)
		if err != nil {
			return nil, err
		}
		return render(rules), nil
	})
	if err != nil {
		return "", err
	}
	return g.(string), nil
}

type vocabGrammarKey struct{ o grammarOpts }

// grammarBuilds counts the grammars built, for the test that a second call builds none.
var grammarBuilds atomic.Int64

// callee is a member a program can call: its path, the argument counts it takes, and each argument's
// closed domain, if any.
type callee struct {
	path    string
	arities []int
	domains [][]string
}

func vocabRules(v *ns.Vocabulary, o grammarOpts) ([]rule, error) {
	members, roots, err := callees(v)
	if err != nil {
		return nil, err
	}
	if len(members) == 0 && o.goalOnly {
		return nil, errors.New("nl: the vocabulary holds nothing a goal can call")
	}
	var extra []rule
	var atoms, calls, dcalls alt
	for i, m := range members {
		name := fmt.Sprintf("m%d", i)
		var forms alt
		for _, n := range m.arities {
			forms = append(forms, seq{lit(m.path + "("), m.args(name, n, &extra), lit(")")})
			if n >= 2 { // a function relation: the call writes all but the result
				c := seq{lit(m.path + "("), ref("sum")}
				for k := 2; k < n; k++ {
					c = append(c, lit(", "), ref("sum"))
				}
				c = append(c, lit(")"))
				calls = append(calls, c)
				if strings.Contains(m.path, ".") {
					dcalls = append(dcalls, c)
				}
			}
		}
		extra = append(extra, rule{name, forms})
		atoms = append(atoms, ref(name))
	}
	if !o.goalOnly {
		local := seq{ref("local"), lit("(")}
		atoms = append(atoms, append(local, opt(list(ref("term"), ", ")), lit(")")))
		calls = append(calls, append(seq{ref("local"), lit("(")}, list(ref("sum"), ", "), lit(")")))
		extra = append(extra, localRules(roots)...)
	}
	replace := map[string]gexpr{"atom": atoms, "call": calls}
	if !o.goalOnly {
		replace["head"] = seq{ref("local"), lit("("), opt(list(ref("harg"), ", ")), lit(")")}
	} else {
		replace["root"] = ref("goal")
	}
	var out []rule
	for _, r := range programRules() {
		e := r.e
		if x, ok := replace[r.name]; ok {
			e = x
		}
		switch {
		case r.name == "call" && len(calls) == 0, r.name == "dcall" && len(dcalls) == 0:
			continue
		case r.name == "dcall":
			e = dcalls
		case r.name == "unary" && len(calls) == 0:
			e = without(e.(alt), "call")
		case r.name == "arith":
			if len(calls) == 0 {
				e = without(e.(alt), "call")
			}
			if len(dcalls) == 0 {
				e = without(e.(alt), "dcall")
			}
		}
		out = append(out, rule{r.name, e})
	}
	return reachable(append(out, extra...)), nil
}

// args are the arguments of a call taking n: a term each, or for a closed domain a variable or one of
// its values, through a rule of its own.
func (m callee) args(name string, n int, extra *[]rule) gexpr {
	if n == 0 {
		return seq{}
	}
	arg := func(i int) gexpr {
		if i >= len(m.domains) || len(m.domains[i]) == 0 {
			return ref("term")
		}
		dname := fmt.Sprintf("%sa%d", name, i)
		for _, r := range *extra {
			if r.name == dname {
				return ref(dname)
			}
		}
		vals := alt{ref("variable")}
		for _, d := range m.domains[i] {
			vals = append(vals, lit(`"`+d+`"`))
		}
		*extra = append(*extra, rule{dname, vals})
		return ref(dname)
	}
	s := seq{arg(0)}
	for i := 1; i < n; i++ {
		s = append(s, lit(", "), arg(i))
	}
	return s
}

// callees lists v's members in path order, and the names at v's root, which a program's own rule
// can't take.
func callees(v *ns.Vocabulary) ([]callee, []string, error) {
	var out []callee
	var walk func(module string) error
	var roots []string
	walk = func(module string) error {
		members, err := v.Members(module)
		if err != nil {
			return err
		}
		for _, m := range members {
			if module == "" {
				roots = append(roots, m.Path)
			}
			if m.Kind == ns.EntryModule {
				if err := walk(m.Path); err != nil {
					return err
				}
				continue
			}
			c := callee{path: m.Path, arities: []int{len(m.Args)}}
			if bi, ok := v.Predicate(m.Path); ok && bi.MaxArity > 0 {
				c.arities = nil
				for n := bi.Arity; n <= bi.MaxArity; n++ {
					c.arities = append(c.arities, n)
				}
			}
			for _, a := range m.Args {
				var dom []string
				for _, d := range a.Domain {
					if !strings.ContainsAny(d, "\"\\") && !strings.ContainsFunc(d, func(r rune) bool { return r < 0x20 }) {
						dom = append(dom, d)
					}
				}
				c.domains = append(c.domains, dom)
			}
			out = append(out, c)
		}
		return nil
	}
	if err := walk(""); err != nil {
		return nil, nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out, roots, nil
}

// localRules make local: an ident none of the root names spells. They walk the names' characters as a
// trie, a rule per prefix, leaving it at any other character, so the grammar keeps a program's rule
// from taking a member's or a module's name rather than trusting a model to.
func localRules(names []string) []rule {
	const start = letters
	const rest = letters + digits + "_"
	taken, prefixes := map[string]bool{}, map[string]bool{}
	for _, n := range names {
		if n == "" || !strings.ContainsRune(start, rune(n[0])) || strings.Trim(n, rest) != "" {
			continue // a name no ident spells needs no exclusion
		}
		taken[n] = true
		for i := 1; i <= len(n); i++ {
			prefixes[n[:i]] = true
		}
	}
	var ps []string
	for p := range prefixes {
		ps = append(ps, p)
	}
	sort.Strings(ps)
	id := map[string]string{}
	for i, p := range ps {
		id[p] = fmt.Sprintf("l%d", i)
	}
	tail := star(class{"a-zA-Z0-9_", "abenrt0_"})
	branches := func(p, allowed string) alt {
		var a alt
		var next []byte
		for _, c := range []byte(allowed) {
			if prefixes[p+string(c)] {
				next = append(next, c)
				a = append(a, seq{lit(string(c)), ref(id[p+string(c)])})
			}
		}
		if others := dropChars(allowed, next); others != "" {
			a = append(a, seq{class{ranges(others), others}, tail})
		}
		return a
	}
	out := []rule{{"local", branches("", start)}}
	for _, p := range ps {
		var e gexpr = branches(p, rest)
		if !taken[p] {
			e = opt(e) // p itself is a name a rule may take
		}
		out = append(out, rule{id[p], e})
	}
	return out
}

func dropChars(chars string, drop []byte) string {
	return strings.Map(func(r rune) rune {
		if strings.IndexRune(string(drop), r) >= 0 {
			return -1
		}
		return r
	}, chars)
}

// ranges writes ASCII characters as a GBNF class body, runs as ranges: abcdx is a-dx.
func ranges(chars string) string {
	b := []byte(chars)
	sort.Slice(b, func(i, j int) bool { return b[i] < b[j] })
	var sb strings.Builder
	for i := 0; i < len(b); {
		j := i
		for j+1 < len(b) && b[j+1] == b[j]+1 {
			j++
		}
		switch {
		case j-i >= 2:
			fmt.Fprintf(&sb, "%c-%c", b[i], b[j])
		case j == i+1:
			sb.WriteByte(b[i])
			sb.WriteByte(b[j])
		default:
			sb.WriteByte(b[i])
		}
		i = j + 1
	}
	return sb.String()
}

// without drops the alternatives that are, or start with, a reference to name.
func without(a alt, name string) alt {
	var out alt
	for _, e := range a {
		if mentions(e, name) {
			continue
		}
		out = append(out, e)
	}
	return out
}

func mentions(e gexpr, name string) bool {
	switch e := e.(type) {
	case ref:
		return string(e) == name
	case seq:
		for _, x := range e {
			if mentions(x, name) {
				return true
			}
		}
	case alt:
		for _, x := range e {
			if mentions(x, name) {
				return true
			}
		}
	case rep:
		return mentions(e.e, name)
	}
	return false
}

// reachable keeps the rules root reaches, in order.
func reachable(rules []rule) []rule {
	byName := map[string]gexpr{}
	for _, r := range rules {
		byName[r.name] = r.e
	}
	seen := map[string]bool{}
	var visit func(e gexpr)
	visit = func(e gexpr) {
		switch e := e.(type) {
		case ref:
			if !seen[string(e)] {
				seen[string(e)] = true
				visit(byName[string(e)])
			}
		case seq:
			for _, x := range e {
				visit(x)
			}
		case alt:
			for _, x := range e {
				visit(x)
			}
		case rep:
			visit(e.e)
		}
	}
	visit(ref("root"))
	var out []rule
	for _, r := range rules {
		if seen[r.name] {
			out = append(out, r)
		}
	}
	return out
}
