package datalog

import (
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/panyam/jaala/internal/gbnf"
)

// The grammar nl.Grammar gives a model (#323) is checked here, against the programs this package's
// tests and the docs write, because only this package can reach them; nl's own test checks the
// other direction, that every program the grammar writes parses. The grammar comes from nl's golden
// file, so no import runs the wrong way.
const grammarFile = "../frontends/nl/testdata/program.gbnf"

// Every program the corpus, the fuzz seeds and the docs write, printed in jaala's own spelling, is a
// program the grammar accepts, so a model decoding under it can still say what they say.
func TestTheGrammarAcceptsEveryProgramAsJaalaSpellsIt(t *testing.T) {
	g := mustGrammar(t)
	var texts []string
	for seed := int64(1); seed <= int64(envInt("JAALA_GEN_SEEDS", 300)); seed++ {
		rnd := rand.New(rand.NewSource(seed))
		texts = append(texts, newGenerator(rnd, genGraph(rnd)).program().String())
	}
	texts = append(texts, fuzzSeeds...)
	docs := demoPrograms(t)
	if len(docs) < 150 {
		t.Fatalf("control: read only %d of the docs' examples", len(docs))
	}
	texts = append(texts, docs...)
	checked := 0
	for _, text := range texts {
		q, err := Parse(text)
		if err != nil {
			continue // an example of a mistake
		}
		printed := inJaalasSpelling(q)
		if _, err := Parse(printed); err != nil {
			t.Fatalf("control: %q prints as %q, which doesn't parse: %v", text, printed, err)
		}
		checked++
		if !g.Accepts(printed) {
			t.Errorf("the grammar refuses %q", printed)
		}
	}
	if checked < 400 {
		t.Fatalf("control: only %d programs checked", checked)
	}
	t.Logf("%d programs, each accepted", checked)
}

// The grammar refuses what doesn't parse, and what it leaves out on purpose, so the test above can
// tell a grammar that accepts everything from one that accepts programs.
func TestTheGrammarRefusesWhatDoesntParse(t *testing.T) {
	g := mustGrammar(t)
	for _, text := range []string{
		`edge(?a, ?b`,
		`edge(?a, ?b) =>`,
		`edge(?X, GND) => ?X`,
		`edge(?a, ?b), => ?a`,
		`?_ = count(?a) : { edge(?a, ?b) }`,
		`edge(?a, ?b) => ?a limit 0`,
		`edge(?a, ?b) # a comment`,
		`edge(?a, ?b).`,
		`?- edge(?a, ?b)`,
		`edge(?a, ?b), !node(?a)`,
		`edge(?a, "v"0") => ?a`,
	} {
		if g.Accepts(text) {
			t.Errorf("the grammar accepts %q", text)
		}
	}
	if !g.Accepts(`edge(?a, "v0"), not node(?a) => ?a, count(distinct ?a) order by ?a desc limit 10`) {
		t.Fatal("control: the grammar refuses a plain program")
	}
}

// inJaalasSpelling writes q as jaala spells it, the printer's way, which is the spelling the grammar covers.
func inJaalasSpelling(q Query) string {
	var clauses []string
	for _, r := range q.Rules {
		clauses = append(clauses, r.String())
	}
	goal := q.Goal.String()
	if len(q.Select) > 0 {
		goal += " => " + joinTerms(q.Select)
		if len(q.Having) > 0 {
			hs := make([]string, len(q.Having))
			for i, h := range q.Having {
				hs[i] = h.Left.String() + " " + h.Op + " " + h.Right.String()
			}
			goal += " having " + strings.Join(hs, ", ")
		}
		if len(q.OrderBy) > 0 {
			os := make([]string, len(q.OrderBy))
			for i, o := range q.OrderBy {
				os[i] = o.Term.String()
				if o.Desc {
					os[i] += " desc"
				}
			}
			goal += " order by " + strings.Join(os, ", ")
		}
		if len(q.After) > 0 {
			as := make([]Term, len(q.After))
			for i := range q.After {
				as[i] = Term{Const: &q.After[i]}
			}
			goal += " after " + joinTerms(as)
		}
		if q.Limit > 0 {
			goal += " limit " + strconv.Itoa(q.Limit)
		}
		if q.Offset > 0 {
			goal += " offset " + strconv.Itoa(q.Offset)
		}
	}
	return strings.Join(append(clauses, goal), "; ")
}

func joinTerms(ts []Term) string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.String()
	}
	return strings.Join(out, ", ")
}

// demoPrograms reads every docs example's program and query (docsite/demos), as one text each.
func demoPrograms(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob("../docsite/demos/*/*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		fields := yamlTexts(string(b))
		text := strings.TrimSpace(fields["program"])
		if q := strings.TrimSpace(fields["query"]); q != "" {
			if text != "" && !strings.HasSuffix(text, ";") {
				text += ";"
			}
			text += "\n" + q
		}
		out = append(out, text)
	}
	return out
}

// yamlTexts reads a spec's top-level string fields, inline or as a "|" block.
func yamlTexts(s string) map[string]string {
	out := map[string]string{}
	lines := strings.Split(s, "\n")
	for i := 0; i < len(lines); i++ {
		key, val, ok := strings.Cut(lines[i], ":")
		if !ok || strings.HasPrefix(lines[i], " ") {
			continue
		}
		val = strings.TrimSpace(val)
		if val != "|" {
			out[key] = strings.Trim(val, `'`)
			continue
		}
		var block []string
		for i+1 < len(lines) && (strings.HasPrefix(lines[i+1], "  ") || strings.TrimSpace(lines[i+1]) == "") {
			i++
			block = append(block, strings.TrimPrefix(lines[i], "  "))
		}
		out[key] = strings.Join(block, "\n")
	}
	return out
}

func mustGrammar(t *testing.T) *gbnf.Grammar {
	t.Helper()
	b, err := os.ReadFile(grammarFile)
	if err != nil {
		t.Fatalf("%v (go test ./nl -update writes it)", err)
	}
	g, err := gbnf.Parse(string(b))
	if err != nil {
		t.Fatal(err)
	}
	return g
}
