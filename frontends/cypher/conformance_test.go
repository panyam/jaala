package cypher_test

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/panyam/jaala/frontends/cypher"
	"github.com/panyam/jaala/internal/guide"
)

// conformance reads testdata/conformance.txt: its queries by section, each a block of lines between
// blank lines, notes (# ...) left out.
func conformance(t *testing.T) map[string][]string {
	t.Helper()
	data, err := os.ReadFile("testdata/conformance.txt")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]string{}
	section, block := "", []string{}
	flush := func() {
		if len(block) > 0 {
			out[section] = append(out[section], strings.Join(block, "\n"))
			block = nil
		}
	}
	for _, line := range strings.Split(string(data), "\n") {
		switch {
		case strings.HasPrefix(line, "## "):
			flush()
			section = strings.TrimPrefix(line, "## ")
		case strings.HasPrefix(line, "#"):
		case strings.TrimSpace(line) == "":
			flush()
		default:
			block = append(block, line)
		}
	}
	flush()
	return out
}

// Every valid Neo4j 5 query in the corpus parses (#382): it compiles or is refused naming the gap that
// would let it, and what isn't a query, or answers nothing, is refused with no gap. A plain parse
// error on valid Cypher tells a model its query is wrong when it isn't. The golden records each
// query's outcome, which makes it the list of what a gap's backfill would compile.
func TestEveryValidQueryParses(t *testing.T) {
	v, _ := guide.Deps(t)
	sections := conformance(t)
	var b strings.Builder
	for _, section := range []string{"compiles or names a gap", "refused plainly"} {
		qs := sections[section]
		if len(qs) == 0 {
			t.Fatalf("no queries under %q", section)
		}
		fmt.Fprintf(&b, "## %s\n\n", section)
		for _, q := range qs {
			p, err := cypher.Compile(v, depsGraph, q)
			var ce *cypher.Error
			if err != nil && !errors.As(err, &ce) {
				t.Errorf("%s: %v is not a *cypher.Error", q, err)
				continue
			}
			switch {
			case section == "refused plainly" && (ce == nil || ce.Gap != 0):
				t.Errorf("%s: %v, want a refusal naming no gap", q, err)
			case section != "refused plainly" && ce != nil && ce.Gap == 0:
				t.Errorf("%s: %v, want it compiled or a refusal naming a gap", q, err)
			}
			fmt.Fprintf(&b, "%s\n", q)
			if err != nil {
				fmt.Fprintf(&b, "  -> at %d:%d %v\n\n", ce.Pos.Line, ce.Pos.Column, err)
			} else {
				fmt.Fprintf(&b, "  -> %s\n\n", p.Text)
			}
		}
	}
	got := b.String()
	const golden = "testdata/conformance.golden"
	if *update {
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("outcomes changed (go test -run TestEveryValidQueryParses -update writes them):\n%s", got)
	}
}

// A refusal waits until the whole text has parsed, so a mistake after a gap is what's reported:
// otherwise a model fixes the gap and meets the typo on its next try.
func TestAMistakeAfterAGapIsReportedFirst(t *testing.T) {
	v, _ := guide.Deps(t)
	for _, c := range []struct{ cypher, want string }{
		{`OPTIONAL MATCH (a:Package) RETURN a.name AS`, "expected a column name after AS"},
		{`MATCH (a:Package) UNWIND [1, 2] AS x RETURN x x`, `unexpected "x" after RETURN`},
		{`MATCH (a:Package) RETURN CASE WHEN a.name THEN 1 END, a.`, "expected a property name after ."},
		{`MATCH ((a)-[:IMPORTS]->(b)){1,3} RETURN b AS`, "expected a column name after AS"},
	} {
		_, err := cypher.Compile(v, depsGraph, c.cypher)
		var ce *cypher.Error
		if !errors.As(err, &ce) || ce.Gap != 0 || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want the mistake: %s", c.cypher, err, c.want)
		}
	}
	// control: the same queries without the mistake are refused for their gap
	for _, q := range []string{`OPTIONAL MATCH (a:Package) RETURN a.name AS n`, `MATCH (a:Package) UNWIND [1, 2] AS x RETURN x`,
		`MATCH (a:Package) RETURN CASE WHEN a.name THEN 1 END, a.name`, `MATCH ((a)-[:IMPORTS]->(b)){1,3} RETURN b AS c`} {
		_, err := cypher.Compile(v, depsGraph, q)
		var ce *cypher.Error
		if !errors.As(err, &ce) || ce.Gap == 0 {
			t.Errorf("control: %s: %v, want a refusal naming a gap", q, err)
		}
	}
}
