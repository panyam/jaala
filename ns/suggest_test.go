package ns

import (
	"strings"
	"testing"
)

// moved is a vocabulary just after its names moved into modules, which is when people type the old
// spellings.
func moved(t *testing.T) *Vocabulary {
	t.Helper()
	src := NewMemSource().
		Declare("edge", "from", "to").
		Declare("rails", "n").
		Declare("net_total", "n").
		Declare("net.total", "n").
		Declare("net.rail", "net").
		Declare("board.rail", "net").
		Declare("net.pin_count", "net", "n").
		Declare("component.net", "ref", "net").
		Declare("component.pin", "ref", "pin").
		Declare("pin.name", "ref", "pin", "name").
		Declare("pin.net", "ref", "pin", "net").
		Declare("x.a", "v").Declare("y.a", "v").Declare("z.a", "v")
	v := MustVocabulary(src)
	if err := v.AddLanguage(stub{new(int)}); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestABareNameSuggestsTheMembersItEnds(t *testing.T) {
	v := moved(t)
	for name, want := range map[string]string{
		"pin_count": `; did you mean "net.pin_count"?`,
		// rails at the root is one edit away, but members ending in rail are likelier meant.
		"rail":  `; "board.rail" and "net.rail" both end in "rail"`,
		"a":     `; "x.a", "y.a" and "z.a" all end in "a"`,
		"total": `; did you mean "net.total"?`,
		// Nothing ends in egde, so the root typo stands.
		"egde": `; did you mean "edge"?`,
	} {
		if got := v.Hint(name); got != want {
			t.Errorf("Hint(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestAModuleNameSuggestsTheMemberItEnds(t *testing.T) {
	want := `"pin" is a module, not a relation; it holds name, net; did you mean "component.pin"?`
	if got := moved(t).Unknown("pin"); got != want {
		t.Errorf("Unknown(pin) = %q, want %q", got, want)
	}
}

func TestAPrivateMemberIsNeverSuggested(t *testing.T) {
	v := moved(t)
	if err := v.AddModule("m", "stub", "_secret/1", ""); err != nil {
		t.Fatal(err)
	}
	if got := v.Hint("_secret"); strings.Contains(got, "m._secret") {
		t.Errorf("Hint(_secret) = %q, which names a private member", got)
	}
}

func TestAPathSpelledWithAnotherSeparatorIsSuggested(t *testing.T) {
	v := moved(t)
	for name, want := range map[string]string{
		"net_pin_count":    `; did you mean "net.pin_count"?`,
		"net-pin-count":    `; did you mean "net.pin_count"?`,
		"component_on_net": `; did you mean "component.net"?`,
		// A close root name is the likelier typo and keeps its suggestion.
		"net_totl": `; did you mean "net_total"?`,
		"zzz_qqq":  "",
	} {
		if got := v.Hint(name); got != want {
			t.Errorf("Hint(%q) = %q, want %q", name, got, want)
		}
	}
}

// countingDistance swaps in an edit distance that adds up the cells it fills, for the test's duration.
func countingDistance(t *testing.T) *int {
	t.Helper()
	cells := 0
	distance = func(a, b string) int {
		cells += (len([]rune(a)) + 1) * (len([]rune(b)) + 1)
		return levenshtein(a, b)
	}
	t.Cleanup(func() { distance = levenshtein })
	return &cells
}

// A long unknown name costs a suggestion about its length, not its length times itself: one
// separator respelling per hyphen, each compared in full against every module path, took most of a
// second per Eval at 2,000 hyphens (#89, found by FuzzEval). control: a short typo still compares,
// and still gets its suggestion.
func TestALongUnknownNameIsCheapToSuggestFor(t *testing.T) {
	v := moved(t)
	cells := countingDistance(t)
	const n = 2000
	for name, want := range map[string]string{
		strings.Repeat("-", n):          "",
		strings.Repeat("a_", n/2):       "",
		"net." + strings.Repeat("x", n): `; "net" holds pin_count, rail, total`,
		strings.Repeat("q.", n/2) + "r": "",
	} {
		*cells = 0
		if got := v.Hint(name); got != want {
			t.Errorf("Hint(%.20q...) = %q, want %q", name, got, want)
		}
		if *cells > 100*n {
			t.Errorf("Hint(%.20q...) filled %d edit-distance cells, want at most %d", name, *cells, 100*n)
		}
	}
	*cells = 0
	if got := DidYouMeanValue([]string{"resistor", "capacitor"}, strings.Repeat("r", n)); got != "" || *cells > 100*n {
		t.Errorf("DidYouMeanValue on a long value: %q after %d cells", got, *cells)
	}
	*cells = 0
	if got := v.Hint("net_pin_cout"); got != `; did you mean "net.pin_count"?` || *cells == 0 {
		t.Errorf("control: Hint(net_pin_cout) = %q after %d cells, want net.pin_count found by comparing", got, *cells)
	}
}

// A member spelled with a word another library adds (`str.has_suffix`, #276) names the member whose
// last segment is one of its words; two such members would be a guess, so the module's members are
// listed instead, as they are for any unknown member of a module that exists.
func TestAMemberWithAnExtraWordSuggestsTheMemberOrListsTheModule(t *testing.T) {
	v := moved(t)
	for name, want := range map[string]string{
		"net.is_rail":    `; did you mean "net.rail"?`,
		"net.get-total":  `; did you mean "net.total"?`,
		"net.rail_total": `; "net" holds pin_count, rail, total`,
		"net.voltage":    `; "net" holds pin_count, rail, total`,
		"net.pin_cout":   `; did you mean "net.pin_count"?`, // control: a typo still wins
		"nett.is_rail":   `; did you mean "net"?`,           // control: a missing module is repaired first
		"is_rail":        ``,                                // control: a bare name never lists
	} {
		if got := v.Hint(name); got != want {
			t.Errorf("Hint(%q) = %q, want %q", name, got, want)
		}
	}
}

// A name nothing misspells is offered the members it might have meant: at its arity, those sharing a
// word with it first, relations before predicates, at most four.
func TestRelatedOffersMembersAtTheCallsArity(t *testing.T) {
	src := NewMemSource().
		DeclareSchema("pkg", Schema{Arity: 2, Labels: []string{"name", "license"}, Doc: "A package and its license."}).
		DeclareSchema("owns", Schema{Arity: 2, Labels: []string{"team", "pkg"}, Doc: "Which team maintains a package."}).
		DeclareSchema("node", Schema{Arity: 1, Labels: []string{"name"}})
	v := MustVocabulary(src)
	for path, arity := range map[string]int{"str.prefix": 2, "pkg2": 3} {
		if err := v.AddPredicate(path, Filter(arity, func([]Value) (bool, error) { return true, nil })); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct {
		name  string
		arity int
		want  []string
	}{
		{"zzz", 2, []string{"owns", "pkg", "str.prefix"}},        // control: the Source's order (sorted), then predicates
		{"license_of", 2, []string{"pkg", "owns", "str.prefix"}}, // "license" is in pkg's doc, so it moves first
		{"has_prefix", 2, []string{"str.prefix", "owns", "pkg"}}, // a predicate sharing a word beats relations that don't
		{"zzz", 1, []string{"node"}},
		{"zzz", 5, nil},
		{"zzz", -1, []string{"node", "owns", "pkg", "pkg2"}}, // any arity, cut at four
	} {
		if got := v.Related(c.name, c.arity); !equalStrings(got, c.want) {
			t.Errorf("Related(%q, %d) = %v, want %v", c.name, c.arity, got, c.want)
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
