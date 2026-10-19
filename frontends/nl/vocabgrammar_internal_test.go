package nl

import (
	"context"
	"strings"
	"testing"

	"github.com/panyam/jaala/datalog"
	"github.com/panyam/jaala/internal/gbnf"
	"github.com/panyam/jaala/ns"
	"github.com/panyam/jaala/stdlib"
)

// grammarVocabulary holds what GrammarFor narrows to: typed relations, a closed domain, the standard
// predicates, a predicate with an optional argument, and a module with a derived member.
func grammarVocabulary(t *testing.T) *ns.Vocabulary {
	t.Helper()
	node := ns.ArgType{Kind: "node"}
	src := ns.NewMemSource().
		DeclareSchema("edge", ns.Schema{Arity: 2, Labels: []string{"from", "to"}, Types: []ns.ArgType{node, node}}).
		DeclareSchema("kind", ns.Schema{Arity: 2, Labels: []string{"n", "k"}, Types: []ns.ArgType{node, {Type: ns.TypeString, Domain: []string{"function", "type"}}}})
	v, err := ns.NewVocabulary(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := v.AddLanguage(datalog.Language); err != nil {
		t.Fatal(err)
	}
	if err := stdlib.Register(v); err != nil {
		t.Fatal(err)
	}
	find := ns.Builtin{Arity: 2, MaxArity: 3, Labels: []string{"id", "name", "limit"}, Modes: [][]bool{{false, true, false}},
		Gen: func(context.Context, ns.Source, []ns.Arg, func([]ns.Value, []string) error) error { return nil }}
	if err := v.AddPredicate("name.find", find); err != nil {
		t.Fatal(err)
	}
	if err := v.AddModule("graph", datalog.LanguageName, "reach(?a, ?b) :- edge(?a, ?b);\nreach(?a, ?c) :- reach(?a, ?b), edge(?b, ?c);", ""); err != nil {
		t.Fatal(err)
	}
	return v
}

func mustGBNF(t *testing.T, text string) *gbnf.Grammar {
	t.Helper()
	g, err := gbnf.Parse(text)
	if err != nil {
		t.Fatalf("%v\n%s", err, text)
	}
	return g
}

// Every program the vocabulary's grammar writes parses, is one the plain grammar writes, and calls only
// the vocabulary's members at an arity they take, or a rule of its own.
func TestEveryProgramAVocabularysGrammarWritesCallsItsMembers(t *testing.T) {
	v := grammarVocabulary(t)
	plain := mustGBNF(t, Grammar())
	for _, goalOnly := range []bool{false, true} {
		rules, err := vocabRules(v, grammarOpts{goalOnly: goalOnly})
		if err != nil {
			t.Fatal(err)
		}
		var s *gbnf.Sampler
		failed := 0
		for i := 0; i < 8000; i++ {
			if i%1000 == 0 {
				s = gbnf.NewSampler(table(rules), int64(i), 3+i/1000%7)
			}
			text := s.Sample("root")
			problem := ""
			if q, err := datalog.Parse(text); err != nil {
				problem = err.Error()
			} else if i%10 == 0 && !plain.Accepts(text) { // the recognizer is the test's cost
				problem = "the plain grammar refuses it"
			} else {
				problem = callsMembers(v, q, goalOnly)
			}
			if problem != "" {
				failed++
				t.Errorf("goal only %v: %q: %s", goalOnly, text, problem)
				if failed >= 10 {
					t.FailNow()
				}
			}
		}
	}
}

// callsMembers says what in q calls something other than a member of v at an arity it takes, or a
// value outside a closed domain, "" when nothing does. A call to a name v doesn't hold is a rule's,
// which only a program with rules may make.
func callsMembers(v *ns.Vocabulary, q datalog.Query, goalOnly bool) string {
	if goalOnly && len(q.Rules) > 0 {
		return "a goal-only grammar wrote a rule"
	}
	for _, r := range q.Rules {
		if v.Has(r.Head.Relation) || v.IsModule(r.Head.Relation) {
			return "a rule takes the name " + r.Head.Relation
		}
	}
	check := func(rel string, args []datalog.Term) string {
		if rel == "graph.closure" { // the engine's (#281): its first argument names what it walks
			if len(args) != 6 || args[0].Const == nil {
				return "a closure without six arguments and a relation named"
			}
			if e, err := v.Lookup(args[0].Const.S); err != nil || len(e.Args) != 2 {
				return "a closure over " + args[0].Const.S + ", no relation of two arguments"
			}
			return ""
		}
		e, err := v.Lookup(rel)
		if err != nil {
			if goalOnly || strings.Contains(rel, ".") {
				return "a call to " + rel + ", which the vocabulary doesn't hold"
			}
			return ""
		}
		ok := len(args) == len(e.Args)
		if bi, isPred := v.Predicate(rel); isPred {
			ok = bi.Accepts(len(args))
		}
		if !ok {
			return "a call to " + rel + " with the wrong number of arguments"
		}
		for i, a := range args {
			if dom := e.Args[i].Domain; len(dom) > 0 && a.Const != nil && !contains(dom, a.Const.S) {
				return "a value outside " + rel + "'s domain: " + a.Const.S
			}
		}
		return ""
	}
	var body func(b datalog.Body) string
	var term func(t datalog.Term) string
	term = func(t datalog.Term) string {
		if t.Expr == nil {
			return ""
		}
		if t.Expr.Op == "call" {
			if p := check(t.Expr.Func, append(append([]datalog.Term{}, t.Expr.Args...), datalog.Term{})); p != "" {
				return p
			}
		}
		for _, a := range t.Expr.Args {
			if p := term(a); p != "" {
				return p
			}
		}
		return ""
	}
	body = func(b datalog.Body) string {
		for _, l := range b.Literals {
			var p string
			switch {
			case l.Pos != nil:
				p = check(l.Pos.Relation, l.Pos.Args)
			case l.Neg != nil:
				p = check(l.Neg.Relation, l.Neg.Args)
			case l.Agg != nil:
				p = body(l.Agg.Body)
			case l.Compare != nil:
				if p = term(l.Compare.Left); p == "" {
					p = term(l.Compare.Right)
				}
			}
			if p != "" {
				return p
			}
		}
		return ""
	}
	for _, r := range q.Rules {
		if p := body(r.Body); p != "" {
			return p
		}
		for _, a := range r.Head.Args {
			if p := term(a); p != "" {
				return p
			}
		}
	}
	for _, c := range q.Select {
		if p := term(c); p != "" {
			return p
		}
	}
	return body(q.Goal)
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// What a model would write over the vocabulary is in its grammar; a misspelled member, a wrong count,
// a value outside a domain and a rule named for a member are not.
func TestAVocabularysGrammarHoldsItsProgramsAndNoOthers(t *testing.T) {
	v := grammarVocabulary(t)
	text, err := GrammarFor(v)
	if err != nil {
		t.Fatal(err)
	}
	strict, err := GrammarFor(v, GoalOnly())
	if err != nil {
		t.Fatal(err)
	}
	g, goal := mustGBNF(t, text), mustGBNF(t, strict)
	for _, c := range []struct {
		text       string
		in, inGoal bool
	}{
		{`edge(?a, ?b) => ?a, ?b`, true, true},
		{`graph.reach("a", ?b), not edge(?b, ?b) => ?b`, true, true},
		{`kind(?n, "function"), edge(?n, ?m) => ?m, count(distinct ?n) order by ?m desc limit 5`, true, true},
		{`kind(?n, ?k), str.prefix(?k, "fun") => ?n`, true, true},
		{`name.find(?id, "Foo"), name.find(?j, "Bar", 3) => ?id, ?j`, true, true},
		{`edge(?a, ?b), ?c = num.add(?b, 1) => ?c`, true, true},
		{`?c = count(?b) : { edge(?a, ?b) }, kind(?a, "type") => ?a, ?c`, true, true},
		{`far(?a, ?c) :- edge(?a, ?b), edge(?b, ?c); far("a", ?c) => ?c`, true, false},
		{`reach2(?a) :- graph.reach("a", ?a); reach2(?x) => ?x`, true, false},
		{`egde(?a, ?b) => ?a`, true, false},                                // a call no rule defines: Validate's to refuse
		{`graph.reahc(?a, ?b) => ?a`, false, false},                        // a dotted name is only ever a member
		{`edge(?a) => ?a`, false, false},                                   // wrong count
		{`edge(?a, ?b, ?c) => ?a`, false, false},                           // wrong count
		{`name.find(?a, "x", 1, 2) => ?a`, false, false},                   // past the optional argument
		{`kind(?n, "functoin") => ?n`, false, false},                       // outside the domain
		{`edge(?a, ?b) :- kind(?a, ?b); edge(?a, ?b) => ?a`, false, false}, // a rule named for a member
		{`str(?a) :- edge(?a, ?a); str(?a) => ?a`, false, false},           // a rule named for a module
		{`edge(?a, ?b), ?c = num.add(?b) => ?c`, false, false},             // num.add takes two inputs
	} {
		if got := g.Accepts(c.text); got != c.in {
			t.Errorf("GrammarFor accepts %q: %v, want %v", c.text, got, c.in)
		}
		if got := goal.Accepts(c.text); got != c.inGoal {
			t.Errorf("GrammarFor(GoalOnly) accepts %q: %v, want %v", c.text, got, c.inGoal)
		}
		if _, err := datalog.Parse(c.text); err != nil {
			t.Errorf("control: %q doesn't parse: %v", c.text, err)
		}
	}
}

func TestAVocabularysGrammarIsBuiltOncePerState(t *testing.T) {
	v := grammarVocabulary(t)
	before := grammarBuilds.Load()
	first, err := GrammarFor(v)
	if err != nil {
		t.Fatal(err)
	}
	if again, _ := GrammarFor(v); again != first || grammarBuilds.Load()-before != 1 {
		t.Fatalf("two calls built %d grammars, want 1", grammarBuilds.Load()-before)
	}
	if goal, _ := GrammarFor(v, GoalOnly()); goal == first {
		t.Fatal("GoalOnly gave the same grammar")
	}
	if err := v.AddRelation("weight", ns.Schema{Arity: 2, Labels: []string{"n", "w"}}); err != nil {
		t.Fatal(err)
	}
	after, _ := GrammarFor(v)
	if after == first || !strings.Contains(after, `"weight("`) {
		t.Fatal("a registration didn't reach the grammar")
	}
}
