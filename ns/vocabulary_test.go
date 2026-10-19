package ns

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// stub is a module language for testing the vocabulary without an engine. A module's text is one
// member per line, `name/arity`; a name starting with "_" is private. Check types every argument as
// a "thing" and counts how often it runs.
type stub struct{ checks *int }

func (stub) Name() string { return "stub" }

func (stub) Members(text string) ([]MemberDecl, error) {
	var out []MemberDecl
	for _, line := range strings.Fields(text) {
		name, ar, ok := strings.Cut(line, "/")
		n, err := strconv.Atoi(ar)
		if !ok || err != nil {
			return nil, fmt.Errorf("query: bad stub member %q", line)
		}
		if !strings.HasPrefix(name, "_") {
			out = append(out, MemberDecl{Name: name, Arity: n, Doc: "the " + name, Definition: []string{line}})
		}
	}
	return out, nil
}

func (s stub) Check(v *Vocabulary) (map[string][]ArgSig, error) {
	*s.checks++
	sigs := map[string][]ArgSig{}
	for _, m := range v.Modules() {
		for _, d := range m.Members {
			sig := make([]ArgSig, d.Arity)
			for i := range sig {
				sig[i] = ArgSig{Name: fmt.Sprintf("a%d", i), ArgType: ArgType{Kind: "thing"}, Inferred: true}
			}
			sigs[joinPath(m.Path, d.Name)] = sig
		}
	}
	return sigs, nil
}

func newTestVocabulary(t *testing.T) (*Vocabulary, *int) {
	t.Helper()
	checks := new(int)
	v := MustVocabulary(NewMemSource().Declare("edge", "from", "to").Declare("net.pin_count", "net", "n"))
	if err := v.AddLanguage(stub{checks}); err != nil {
		t.Fatal(err)
	}
	if err := v.AddPredicate("str.contains", Filter(2, func([]Value) (bool, error) { return true, nil })); err != nil {
		t.Fatal(err)
	}
	return v, checks
}

func wantErr(t *testing.T, err error, frag string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), frag) {
		t.Errorf("err = %v, want %q", err, frag)
	}
}

func TestTheTreesRulesHoldAcrossAllThreeKinds(t *testing.T) {
	v, _ := newTestVocabulary(t)
	if err := v.AddModule("net", "stub", "has_tp/1", ""); err != nil {
		t.Fatal(err)
	}
	pred := Filter(1, func([]Value) (bool, error) { return true, nil })
	// One definer per path, whatever the kinds.
	wantErr(t, v.AddPredicate("edge", pred), `"edge" is defined twice, as a base relation and as a predicate`)
	wantErr(t, v.AddRelation("str.contains", Schema{Arity: 2}), `"str.contains" is defined twice, as a predicate and as a base relation`)
	wantErr(t, v.AddModule("", "stub", "edge/2", ""), `"edge" is defined twice, as a base relation and as a derived relation`)
	wantErr(t, v.AddModule("net", "stub", "has_tp/1", ""), `"net.has_tp" is defined twice, as a derived relation and as a derived relation`)
	wantErr(t, v.AddPredicate("net.has_tp", pred), `as a derived relation and as a predicate`)
	// A segment is a module or a member.
	wantErr(t, v.AddPredicate("edge.x", pred), `"edge.x" needs "edge" to be a module, but it is a base relation`)
	wantErr(t, v.AddModule("str.contains", "stub", "x/1", ""), `needs "str.contains" to be a module, but it is a predicate`)
	wantErr(t, v.AddModule("", "stub", "net/1", ""), `"net" cannot be a derived relation: it is already a module holding has_tp, pin_count`)
	wantErr(t, v.AddRelation("str", Schema{Arity: 1}), `"str" cannot be a base relation: it is already a module holding contains`)
	_, err := NewVocabulary(NewMemSource().Declare("pin", "ref").Declare("pin.net", "pin", "net"))
	wantErr(t, err, `"pin.net" needs "pin" to be a module`)
	// A path must be spellable.
	for _, p := range []string{"a..b", ".a", "a b", "a~b"} {
		if err := v.AddPredicate(p, pred); err == nil {
			t.Errorf("AddPredicate(%q) was accepted", p)
		}
	}
}

func TestAModuleInAnUnregisteredLanguageIsRefused(t *testing.T) {
	v, _ := newTestVocabulary(t)
	wantErr(t, v.AddModule("net", "prolog", "x/1", ""), `module "net" is written in "prolog", and no language of that name is registered`)
	wantErr(t, v.AddLanguage(stub{new(int)}), `language "stub" is already registered`)
	if v.Has("net.x") {
		t.Error("a refused module registered a member")
	}
}

func TestAFailedModuleRegistersNothing(t *testing.T) {
	v, _ := newTestVocabulary(t)
	wantErr(t, v.AddModule("net", "stub", "fresh/1 pin_count/2", ""), `"net.pin_count" is defined twice`)
	wantErr(t, v.AddModule("net", "stub", "fresh/1 bad", ""), `bad stub member "bad"`)
	if v.Has("net.fresh") || len(v.Modules()) != 0 {
		t.Errorf("a refused module left net.fresh=%v and %d modules behind", v.Has("net.fresh"), len(v.Modules()))
	}
}

func TestCheckRunsOncePerStateAndFeedsLookup(t *testing.T) {
	v, checks := newTestVocabulary(t)
	if err := v.AddModule("net", "stub", "has_tp/1", ""); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := v.Check(); err != nil {
			t.Fatal(err)
		}
	}
	e, err := v.Lookup("net.has_tp")
	if err != nil {
		t.Fatal(err)
	}
	if *checks != 1 {
		t.Errorf("language checked %d times, want once", *checks)
	}
	want := Entry{Path: "net.has_tp", Kind: EntryDerived, Args: []ArgSig{{Name: "a0", ArgType: ArgType{Kind: "thing"}, Inferred: true}},
		Doc: "the has_tp", Module: "net", Definition: []string{"has_tp/1"}}
	if !reflect.DeepEqual(e, want) {
		t.Errorf("Lookup = %+v\nwant     %+v", e, want)
	}
	if err := v.AddPredicate("str.prefix", Filter(2, func([]Value) (bool, error) { return true, nil })); err != nil {
		t.Fatal(err)
	}
	if err := v.Check(); err != nil || *checks != 2 {
		t.Errorf("after a registration: checks = %d, err = %v; want the cached result dropped", *checks, err)
	}
}

func TestACheckFailureIsLookupsFailure(t *testing.T) {
	v := MustVocabulary(nil)
	failing := failingLanguage{}
	if err := v.AddLanguage(failing); err != nil {
		t.Fatal(err)
	}
	if err := v.AddModule("m", "failing", "x", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Lookup("m.x"); !errors.Is(err, errBroken) {
		t.Errorf("Lookup err = %v, want the language's Check error", err)
	}
	if _, ok := v.Signature("m.x"); ok {
		t.Error("Signature reported a signature for a vocabulary that does not check")
	}
}

var errBroken = errors.New("query: broken module")

type failingLanguage struct{}

func (failingLanguage) Name() string { return "failing" }
func (failingLanguage) Members(text string) ([]MemberDecl, error) {
	return []MemberDecl{{Name: text, Arity: 1}}, nil
}
func (failingLanguage) Check(*Vocabulary) (map[string][]ArgSig, error) { return nil, errBroken }

func TestAGeneratorMustDeclareItsModes(t *testing.T) {
	gen := func(_ context.Context, src Source, args []Arg, emit func([]Value, []string) error) error { return nil }
	v := MustVocabulary(nil)
	wantErr(t, v.AddPredicate("walk", Builtin{Arity: 2, Gen: gen}), `generator "walk" declares no Modes`)
	wantErr(t, v.AddPredicate("walk", Builtin{Arity: 2, Modes: [][]bool{{true}}, Gen: gen}), `generator "walk" has a mode of 1 positions, want 2`)
	wantErr(t, v.AddPredicate("walk", Builtin{Arity: 2, MaxArity: 3, Modes: [][]bool{{true, false}}, Gen: gen}), `has a mode of 2 positions, want 3`)
	if err := v.AddPredicate("walk", Builtin{Arity: 2, MaxArity: 3, Modes: [][]bool{{true, false, false}}, Gen: gen}); err != nil {
		t.Error(err)
	}
	if err := v.AddPredicate("pos", Filter(1, func([]Value) (bool, error) { return true, nil })); err != nil {
		t.Errorf("a filter needs no modes: %v", err)
	}
}

func TestSatisfiedReadsEachMode(t *testing.T) {
	gen := Builtin{Arity: 2, MaxArity: 3, Modes: [][]bool{{true, false, false}, {false, true, true}}, Gen: func(context.Context, Source, []Arg, func([]Value, []string) error) error { return nil }}
	for _, c := range []struct {
		bound []bool
		want  bool
	}{
		{[]bool{true, false}, true},         // the first mode
		{[]bool{false, true, true}, true},   // the second
		{[]bool{false, true}, true},         // the second, its third position past this call's arity
		{[]bool{false, true, false}, false}, // the second needs its third position bound
		{[]bool{false, false}, false},
	} {
		if got := gen.Satisfied(c.bound); got != c.want {
			t.Errorf("Satisfied(%v) = %v, want %v", c.bound, got, c.want)
		}
	}
	filter := Filter(2, func([]Value) (bool, error) { return true, nil })
	if filter.Satisfied([]bool{true, false}) || !filter.Satisfied([]bool{true, true}) {
		t.Error("a filter is satisfied exactly when every argument is bound")
	}
}

// A name the engine answers itself is refused, so a host's predicate under it isn't silently never
// called; its module's other names are free.
func TestAnEngineNameIsReserved(t *testing.T) {
	v := MustVocabulary(NewMemSource())
	for _, name := range []string{"graph.closure", "graph.paths"} {
		err := v.AddPredicate(name, Filter(1, func([]Value) (bool, error) { return true, nil }))
		if err == nil || !strings.Contains(err.Error(), `"`+name+`" is the engine's own`) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if err := v.AddPredicate("graph.reach", Filter(1, func([]Value) (bool, error) { return true, nil })); err != nil {
		t.Errorf("control: graph.reach: %v", err)
	}
}
