package datalog

import (
	"context"
	"errors"
	"fmt"
	"github.com/panyam/jaala/ns"
	"reflect"
	"strings"
	"testing"
)

const reachModule = `
# transitive closure over edge
reach(?a, ?b) :- edge(?a, ?b);
reach(?a, ?c) :- reach(?a, ?b), edge(?b, ?c);
`

// withModules is graph()'s standard registry plus the given modules, path then text.
func withModules(t *testing.T, mods ...string) *ns.Vocabulary {
	t.Helper()
	r := std(graph())
	for i := 0; i < len(mods); i += 2 {
		if err := r.AddModule(mods[i], LanguageName, mods[i+1], ""); err != nil {
			t.Fatalf("AddModule(%q): %v", mods[i], err)
		}
	}
	return r
}

func evalReg(t *testing.T, r *ns.Vocabulary, text string) []Row {
	t.Helper()
	rows, err := both(mustParse(t, text), baseFor(r))
	if err != nil {
		t.Fatalf("Eval(%q): %v", text, err)
	}
	return rows
}

func evalRegErr(r *ns.Vocabulary, text string) error {
	q, err := Parse(text)
	if err != nil {
		return err
	}
	_, err = both(q, baseFor(r))
	return err
}

func TestAModuleMemberIsCalledByItsPath(t *testing.T) {
	r := withModules(t, "hop", reachModule)
	if got := col(evalReg(t, r, `hop.reach("a", ?x) => ?x`), "x"); got != "b,c,d" {
		t.Errorf("hop.reach from a = %s, want b,c,d", got)
	}
}

// A query that calls a module answers exactly, rows and citations, as one with the rules pasted in.
func TestAModuleAnswersAsTheSameRulesPastedInline(t *testing.T) {
	r := withModules(t,
		"hop", reachModule,
		"shape", `sink(?n) :- node(?n), not edge(?n, _);
		          fan(?n, ?k) :- hop.reach(?n, ?k);`)
	for _, c := range []struct{ module, inline string }{{
		`hop.reach(?a, ?b) => ?a, ?b`,
		reachModule + `reach(?a, ?b) => ?a, ?b`,
	}, {
		`shape.sink(?n) => ?n`,
		`sink(?n) :- node(?n), not edge(?n, _); sink(?n) => ?n`,
	}, {
		`shape.fan(?n, ?k) => ?n, count(?k)`,
		reachModule + `fan(?n, ?k) :- reach(?n, ?k); fan(?n, ?k) => ?n, count(?k)`,
	}, {
		`node(?n), not hop.reach(?n, "d") => ?n`,
		reachModule + `node(?n), not reach(?n, "d") => ?n`,
	}} {
		got := evalReg(t, r, c.module)
		want := evalReg(t, std(graph()), c.inline)
		if len(got) == 0 || !reflect.DeepEqual(got, want) {
			t.Errorf("%s\n got  %v\n want %v", c.module, got, want)
		}
	}
}

// Inside a module a bare name is this module's member first, then the root's.
func TestABareNameInAModuleResolvesLocallyThenAtTheRoot(t *testing.T) {
	src := graph().Declare("m.node", "name")
	src.Add("m.node", ns.Tuple{Vals: []ns.Value{ns.S("b")}})
	r := std(src)
	if err := r.AddModule("m", LanguageName, `picked(?x) :- node(?x), edge(?x, _);`, ""); err != nil {
		t.Fatal(err)
	}
	if got := col(evalReg(t, r, `m.picked(?x) => ?x`), "x"); got != "b" {
		t.Errorf("m.picked = %s, want b (m.node, joined with the root's edge)", got)
	}
}

func TestModulesLoadTransitivelyAndReadsFollowThem(t *testing.T) {
	r := withModules(t,
		"top", `t(?x) :- mid.m(?x);`,
		"mid", `m(?x) :- low.l(?x);`,
		"low", `l(?x) :- weight(?x, ?w), ?w > 4;`)
	if got := col(evalReg(t, r, `top.t(?x) => ?x`), "x"); got != "x" {
		t.Errorf("top.t = %s, want x", got)
	}
	if got := strings.Join(Reads(mustParse(t, `top.t(?x), node(?x)`), r), ","); got != "node,weight" {
		t.Errorf("Reads = %s, want node,weight (weight is read two modules down)", got)
	}
}

func TestADiamondLoadsItsBaseModuleOnce(t *testing.T) {
	r := withModules(t,
		"left", `l(?x) :- low.b(?x);`,
		"right", `r(?x) :- low.b(?x);`,
		"low", `b(?x) :- node(?x);`)
	q, err := Link(mustParse(t, `left.l(?x), right.r(?x)`), r)
	if err != nil {
		t.Fatal(err)
	}
	heads := map[string]int{}
	for _, rule := range q.Rules {
		heads[rule.Head.Relation]++
	}
	if heads["low.b"] != 1 || heads["left.l"] != 1 || heads["right.r"] != 1 {
		t.Errorf("linked heads = %v, want each module's one rule once", heads)
	}
}

func TestPrivateMembersAreScopedToTheirModule(t *testing.T) {
	r := withModules(t,
		"a", `_helper(?x) :- edge(?x, _); out(?x) :- _helper(?x);`,
		"b", `_helper(?x) :- edge(_, ?x); out(?x) :- _helper(?x);`)
	if got := col(evalReg(t, r, `a.out(?x) => ?x`), "x"); got != "a,b,c" {
		t.Errorf("a.out = %s, want a,b,c (nodes with an out-edge)", got)
	}
	if got := col(evalReg(t, r, `b.out(?x) => ?x`), "x"); got != "b,c,d" {
		t.Errorf("b.out = %s, want b,c,d (nodes with an in-edge)", got)
	}
	err := evalRegErr(r, `a._helper(?x)`)
	if err == nil || !strings.Contains(err.Error(), `unknown relation "a._helper"`) {
		t.Errorf("err = %v, want a._helper unknown to a query", err)
	}
	if err := evalRegErr(r, `node(?x), not _helper(?x)`); err == nil || !strings.Contains(err.Error(), "unknown relation") {
		t.Errorf("err = %v, want a bare _helper unknown to a query", err)
	}
	// A private member defined by several rules is one relation.
	r = withModules(t, "c", `_h(?x) :- edge(?x, _); _h(?x) :- edge(_, ?x); all(?x) :- _h(?x);`)
	if got := col(evalReg(t, r, `c.all(?x) => ?x`), "x"); got != "a,b,c,d" {
		t.Errorf("c.all = %s, want a,b,c,d (both of _h's rules)", got)
	}
	// Two calls at one module path are two scopes too.
	r = withModules(t,
		"m", `_helper(?x) :- edge(?x, _); from(?x) :- _helper(?x);`,
		"m", `_helper(?x) :- edge(_, ?x); to(?x) :- _helper(?x);`)
	if got := col(evalReg(t, r, `m.from(?x), m.to(?x) => ?x`), "x"); got != "b,c" {
		t.Errorf("m.from and m.to = %s, want b,c (each call's own _helper)", got)
	}
}

func TestAddModuleRefusals(t *testing.T) {
	for _, c := range []struct{ path, text, want string }{
		{"m", `x(?a) :- node(?a); x(?a)`, `rules-only text defines relations and asks nothing`},
		{"m", `n.x(?a) :- node(?a);`, `rule head "n.x" is qualified`},
		{"m", `x(?a) :- node(?a); x(?a, ?b) :- edge(?a, ?b);`, `defines "x" with 1 and 2 args`},
		{"m", `_h(?a) :- node(?a); _h(?a, ?b) :- edge(?a, ?b); x(?a) :- _h(?a);`, `defines "_h" with 1 and 2 args`},
		{"", `edge(?a, ?b) :- node(?a), node(?b);`, `"edge" is defined twice, as a base relation and as a derived relation`},
		{"str", `contains(?a) :- node(?a);`, `"str.contains" is defined twice`},
		{"edge", `x(?a) :- node(?a);`, `needs "edge" to be a module`},
	} {
		err := std(graph()).AddModule(c.path, LanguageName, c.text, "")
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("AddModule(%q, %q): err = %v, want %q", c.path, c.text, err, c.want)
		}
	}
}

func TestAFailedAddModuleRegistersNothing(t *testing.T) {
	r := withModules(t, "m", `taken(?x) :- node(?x);`)
	if err := r.AddModule("m", LanguageName, `fresh(?x) :- node(?x); taken(?x) :- node(?x);`, ""); err == nil {
		t.Fatal("a second definer of m.taken was accepted")
	}
	if err := evalRegErr(r, `m.fresh(?x)`); err == nil || !strings.Contains(err.Error(), `unknown relation "m.fresh"`) {
		t.Errorf("err = %v, want m.fresh left unregistered", err)
	}
}

func TestAQueryCannotDefineASharedPath(t *testing.T) {
	r := withModules(t, "hop", reachModule, "", `top(?x) :- node(?x);`)
	for q, want := range map[string]string{
		`hop.extra(?a) :- node(?a); hop.extra(?a)`: `rule head "hop.extra" is a qualified path`,
		`hop(?a) :- node(?a); hop(?a)`:             `rule head "hop" is a module`,
		`top(?a) :- edge(?a, _); top(?a)`:          `rule head "top" redefines a derived relation registered in module ""`,
	} {
		if err := evalRegErr(r, q); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", q, err, want)
		}
	}
}

func TestModulesMayRecurseThroughEachOther(t *testing.T) {
	r := withModules(t,
		"p", `even(?x) :- node(?x), ?x = "a"; even(?y) :- q.odd(?x), edge(?x, ?y);`,
		"q", `odd(?y) :- p.even(?x), edge(?x, ?y);`)
	if got := col(evalReg(t, r, `p.even(?x) => ?x`), "x"); got != "a,c" {
		t.Errorf("p.even = %s, want a,c", got)
	}
}

func TestRecursionThroughNegationAcrossModulesNamesTheRelations(t *testing.T) {
	r := withModules(t,
		"p", `x(?n) :- node(?n), not q.y(?n);`,
		"q", `y(?n) :- node(?n), not p.x(?n);`)
	err := r.Check()
	if err == nil || !strings.Contains(err.Error(), "not stratifiable (recursion through negation: p.x, q.y)") {
		t.Errorf("Check: err = %v, want the cycle named", err)
	}
	if err := evalRegErr(r, `node(?n)`); err == nil {
		t.Error("a query ran over a registry whose modules do not check")
	}
}

func TestCheckResolvesInAnyRegistrationOrderAndReportsUnknownNames(t *testing.T) {
	r := withModules(t, "a", `x(?n) :- b.y(?n);`)
	err := r.Check()
	if err == nil || !strings.Contains(err.Error(), `module "a" rule "a.x" reads unknown module "b" in "b.y"`) {
		t.Errorf("before b: err = %v, want b.y unknown", err)
	}
	if err := r.AddRelation("b.y", ns.Schema{Arity: 1}); err != nil {
		t.Fatal(err)
	}
	if err := r.Check(); err != nil {
		t.Errorf("after b.y: err = %v, want the earlier failure forgotten", err)
	}
	// A call registering only private members changes no path, and still has to be checked.
	if err := r.AddModule("z", LanguageName, `_bad(?x, ?y) :- node(?x);`, ""); err != nil {
		t.Fatal(err)
	}
	if err := r.Check(); err == nil || !strings.Contains(err.Error(), "head variable ?y is not bound") {
		t.Errorf("after z: err = %v, want the new call checked rather than the cached pass", err)
	}
	r = withModules(t, "m", `x(?n) :- egde(?n, _);`)
	if err := r.Check(); err == nil || !strings.Contains(err.Error(), `reads unknown relation "egde"; did you mean "edge"?`) {
		t.Errorf("typo: err = %v, want a suggestion", err)
	}
	r = withModules(t, "m", `_x(?n, ?m) :- node(?n);`)
	if err := r.Check(); err == nil || !strings.Contains(err.Error(), `head variable ?m is not bound`) {
		t.Errorf("unbound head: err = %v, want range restriction checked at Check", err)
	}
}

// Check runs on first use, and the first use may be several Evals at once.
func TestConcurrentEvalsShareOneCheck(t *testing.T) {
	b := baseFor(withModules(t, "hop", reachModule))
	q := mustParse(t, `hop.reach("a", ?x) => ?x`)
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		go func() {
			rows, err := Naive{}.Eval(bg, q, b)
			if err == nil && col(rows, "x") != "b,c,d" {
				err = fmt.Errorf("rows = %v", rows)
			}
			errs <- err
		}()
	}
	for i := 0; i < 8; i++ {
		if err := <-errs; err != nil {
			t.Error(err)
		}
	}
}

// countingWalk registers a generator that counts its calls, standing in for an expensive walk such as
// Declaire's graph.reach over SQLite.
func countingWalk(t *testing.T, v *ns.Vocabulary) *int {
	t.Helper()
	calls := new(int)
	if err := v.AddPredicate("walk", ns.Builtin{Arity: 2, Modes: [][]bool{{false, false}}, Gen: func(_ context.Context, src ns.Source, args []ns.Arg, emit func([]ns.Value, []string) error) error {
		*calls++
		return nil
	}}); err != nil {
		t.Fatal(err)
	}
	return calls
}

// A query pays for the relations it names, not for their neighbours in the same module (#7, Declaire's
// go.test beside go.covers: 568s when naming the cheap one ran the expensive one).
func TestOnlyReachedModuleRelationsRun(t *testing.T) {
	v := std(graph())
	calls := countingWalk(t, v)
	if err := v.AddModule("go", LanguageName, `
_tested(?f) :- node(?f);
test(?f) :- _tested(?f);
_reached(?t, ?f) :- walk(?t, ?f);
covers(?t, ?f) :- test(?t), _reached(?t, ?f);
`, ""); err != nil {
		t.Fatal(err)
	}
	q, err := Link(mustParse(t, `go.test(?f) => ?f`), v)
	if err != nil {
		t.Fatal(err)
	}
	var heads []string
	for _, r := range q.Rules {
		heads = append(heads, displayName(r.Head.Relation))
	}
	if got := strings.Join(heads, ","); got != "go.test,go._tested" {
		t.Errorf("linked %s, want go.test and the private member it reads, not go.covers or its helper", got)
	}
	if got := col(evalReg(t, v, `go.test(?f) => ?f`), "f"); got != "a,b,c,d,x" || *calls != 0 {
		t.Errorf("go.test = %s with %d walk calls, want a,b,c,d,x and none", got, *calls)
	}
	evalReg(t, v, `go.covers(?t, ?f) => ?t`)
	if *calls == 0 {
		t.Error("positive control: naming go.covers made no walk calls, so the counter sees nothing")
	}
}

// Reads reports the base relations a query depends on, so a relation only an unreached neighbour
// reads is not among them.
func TestReadsFollowOnlyReachedRelations(t *testing.T) {
	v := withModules(t, "m", `cheap(?x) :- node(?x); pricey(?x, ?w) :- weight(?x, ?w);`)
	if got := strings.Join(Reads(mustParse(t, `m.cheap(?x)`), v), ","); got != "node" {
		t.Errorf("Reads(m.cheap) = %s, want node only", got)
	}
}

// Each failure Check can pin on one module says which module, where it came from, through errors.As;
// two modules share net and only the second is broken. The message reads as it always has.
func TestCheckFailuresNameTheirModule(t *testing.T) {
	for _, c := range []struct {
		why, text, msg string
	}{
		{"an unknown read", `y(?n) :- nope(?n);`, `query: module "net" rule "net.y" reads unknown relation "nope"`},
		{"an unbound head variable", `y(?n, ?m) :- node(?n);`, `query: rule "net.y" head variable ?m is not bound by a positive body relation`},
		{"a contradicted type", `y(?n: component) :- net.x(?n);`, `query: net.y declares ?n: component, but its rules make it net`},
		{"an unanchored negation", `y(?n) :- node(?n), not edge(?a, ?b);`, `shares no variable`},
		{"a wrong arity", `y(?n) :- edge(?n);`, `query: relation "edge" takes 2 args, got 1 (in module rule "net.y")`},
	} {
		v := std(graph())
		if err := v.AddModule("net", LanguageName, `x(?n: net) :- node(?n);`, "shipped/net.dl"); err != nil {
			t.Fatal(err)
		}
		if err := v.AddModule("net", LanguageName, c.text, "project/lib/net.dl"); err != nil {
			t.Fatalf("%s: %v", c.why, err)
		}
		err := v.Check()
		var me *ns.ModuleError
		if !errors.As(err, &me) {
			t.Errorf("%s: err = %v (%T), want a *ns.ModuleError", c.why, err, err)
			continue
		}
		if me.Module != 1 || me.Path != "net" || me.Origin != "project/lib/net.dl" || !strings.Contains(err.Error(), c.msg) {
			t.Errorf("%s: %+v; want module 1, net, project/lib/net.dl, and %q", c.why, me, c.msg)
		}
	}
}

// Recursion through negation across two modules is no one module's fault, so it stays a plain error.
func TestACycleAcrossModulesNamesNoModule(t *testing.T) {
	v := withModules(t, "p", `x(?n) :- node(?n), not q.y(?n);`, "q", `y(?n) :- node(?n), not p.x(?n);`)
	err := v.Check()
	var me *ns.ModuleError
	if err == nil || errors.As(err, &me) {
		t.Errorf("err = %v (module error: %v), want a plain stratification error", err, me)
	}
}
