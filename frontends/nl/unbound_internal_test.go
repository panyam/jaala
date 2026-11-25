package nl

import (
	"reflect"
	"testing"

	"github.com/panyam/jaala/datalog"
)

// unboundAt names the variables of a literal that no positive atom written before it binds, in its
// order, each once (#571). control: a variable an earlier literal binds, a wildcard, and a literal
// q doesn't hold name none.
func TestUnboundAtNamesTheLiteralsFreeVariables(t *testing.T) {
	for _, c := range []struct {
		program, lit string
		want         []string
	}{
		{`http.request(?t, ?r), loc.home(?r, "ollama") => ?t`, `http.request(?t, ?r)`, []string{"?t", "?r"}},
		{`loc.home(?r, "ollama"), http.request(?t, ?r) => ?t`, `http.request(?t, ?r)`, []string{"?t"}},
		{`not pkg(?r, "x"), http.request(?t, ?r) => ?t`, `http.request(?t, ?r)`, []string{"?t", "?r"}},
		{`edge(?a, ?a, _) => ?a`, `edge(?a, ?a, _)`, []string{"?a"}},
		{`mine(?a) :- pkg(?a, "MIT"), lib.wide(?a); mine(?x) => ?x`, `lib.wide(?a)`, nil},
		{`edge(?a, ?b) => ?a`, `node(?a)`, nil},
	} {
		q, err := datalog.Parse(c.program)
		if err != nil {
			t.Fatal(err)
		}
		if got := unboundAt(q, c.lit); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s at %s: %q, want %q", c.program, c.lit, got, c.want)
		}
	}
}
