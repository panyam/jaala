package cypher_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/panyam/jaala/datalog"
	"github.com/panyam/jaala/frontends/cypher"
	"github.com/panyam/jaala/internal/guide"
)

// FuzzCompile: any text compiles or is refused with a *cypher.Error (never a panic), and what it
// compiles to parses, which Compile checks, and validates against the vocabulary with its parameters
// bound, or fails only for a type the query itself gets wrong.
func FuzzCompile(f *testing.F) {
	for _, c := range depsCases {
		f.Add(c.cypher)
	}
	for _, d := range drafts {
		f.Add(d)
	}
	for _, s := range []string{`MATCH (a)-[:IMPORTS*1..3]-(b) WHERE NOT (a)-->(b) XOR a.license IN ['x', $y] RETURN a, b`,
		`MATCH (a:Package)<-[:IMPORTS|:DEPENDS_ON*]-(b {license: -1.5e3}) RETURN max(a.license + 1) AS m ORDER BY m`} {
		f.Add(s)
	}
	v, _ := guide.Deps(f)
	f.Fuzz(func(t *testing.T, text string) {
		p, err := cypher.Compile(v, depsGraph, text)
		if err != nil {
			var ce *cypher.Error
			if !errors.As(err, &ce) {
				t.Fatalf("%q: %v is not a *cypher.Error", text, err)
			}
			if ce.Pos.Offset < 0 || ce.Pos.Offset > len(text) {
				t.Fatalf("%q: error at %d, outside the text", text, ce.Pos.Offset)
			}
			return
		}
		for _, r := range p.Text {
			if r < 0x20 && r != '\t' && r != '\n' && r != '\r' && !strings.ContainsRune(text, r) {
				t.Fatalf("%q compiled to text holding a control character the query doesn't, one of the compiler's markers: %q", text, p.Text)
			}
		}
		var params []datalog.Var
		for _, jv := range p.Params {
			params = append(params, jv)
		}
		err = datalog.ValidateBound(p.Query, v, params...)
		var ce *datalog.CheckError
		if errors.As(err, &ce) && ce.Kind == datalog.KindType {
			// the query's own mistake, as negating a string is, which is located in the query
			if pos, ok := p.Locate(err); !ok || pos.Offset < 0 || pos.Offset > len(text) {
				t.Fatalf("%q compiled to %s, whose type error %v Locate places at %+v %v, not in the query", text, p.Text, err, pos, ok)
			}
			return
		}
		if err != nil {
			t.Fatalf("%q compiled to %s, which doesn't check: %v", text, p.Text, err)
		}
	})
}
