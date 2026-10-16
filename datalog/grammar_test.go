package datalog

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

// The grammar nl.Grammar gives a model (#323) is checked here, against the programs this package's
// tests and the docs write, because only this package can reach them; nl's own test checks the
// other direction, that every program the grammar writes parses. The grammar comes from nl's golden
// file, so no import runs the wrong way.
const grammarFile = "../nl/testdata/program.gbnf"

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
		if !g.accepts(printed) {
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
		if g.accepts(text) {
			t.Errorf("the grammar accepts %q", text)
		}
	}
	if !g.accepts(`edge(?a, "v0"), not node(?a) => ?a, count(distinct ?a) order by ?a desc limit 10`) {
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

func mustGrammar(t *testing.T) *gbnf {
	t.Helper()
	b, err := os.ReadFile(grammarFile)
	if err != nil {
		t.Fatalf("%v (go test ./nl -update writes it)", err)
	}
	g, err := parseGBNF(string(b))
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// gbnf is a GBNF grammar compiled to plain productions over single characters, recognized by Earley's
// algorithm. It reads the subset nl renders: rules, alternatives, string literals, character classes
// with ranges, negation and \xNN escapes, groups, and ? * +.
type gbnf struct {
	prods map[string][][]gsym
	fresh int
}

// gsym is a nonterminal (rule set) or one character matching match.
type gsym struct {
	rule  string
	match func(r rune) bool
}

func parseGBNF(text string) (*gbnf, error) {
	g := &gbnf{prods: map[string][][]gsym{}}
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		name, body, ok := strings.Cut(line, " ::= ")
		if !ok {
			return nil, fmt.Errorf("gbnf: no ::= in %q", line)
		}
		p := &gparser{g: g, s: body}
		alts, err := p.alts()
		if err != nil {
			return nil, fmt.Errorf("gbnf: rule %s: %v", name, err)
		}
		if p.i != len(p.s) {
			return nil, fmt.Errorf("gbnf: rule %s: unread %q", name, p.s[p.i:])
		}
		g.prods[name] = append(g.prods[name], alts...)
	}
	if _, ok := g.prods["root"]; !ok {
		return nil, fmt.Errorf("gbnf: no root rule")
	}
	return g, nil
}

type gparser struct {
	g *gbnf
	s string
	i int
}

func (p *gparser) space() {
	for p.i < len(p.s) && p.s[p.i] == ' ' {
		p.i++
	}
}

func (p *gparser) alts() ([][]gsym, error) {
	var out [][]gsym
	for {
		seq, err := p.seq()
		if err != nil {
			return nil, err
		}
		out = append(out, seq)
		p.space()
		if p.i < len(p.s) && p.s[p.i] == '|' {
			p.i++
			continue
		}
		return out, nil
	}
}

func (p *gparser) seq() ([]gsym, error) {
	var out []gsym
	for {
		p.space()
		if p.i >= len(p.s) || p.s[p.i] == '|' || p.s[p.i] == ')' {
			return out, nil
		}
		item, err := p.item()
		if err != nil {
			return nil, err
		}
		if p.i < len(p.s) && strings.IndexByte("?*+", p.s[p.i]) >= 0 {
			item = p.g.repeat(item, p.s[p.i])
			p.i++
		}
		out = append(out, item...)
	}
}

// item reads one literal, class, rule name or group, as the symbols it stands for.
func (p *gparser) item() ([]gsym, error) {
	switch c := p.s[p.i]; {
	case c == '"':
		var out []gsym
		for p.i++; p.i < len(p.s) && p.s[p.i] != '"'; {
			r, err := p.char()
			if err != nil {
				return nil, err
			}
			out = append(out, gsym{match: func(x rune) bool { return x == r }})
		}
		p.i++
		return out, nil
	case c == '[':
		p.i++
		neg := p.i < len(p.s) && p.s[p.i] == '^'
		if neg {
			p.i++
		}
		type span struct{ lo, hi rune }
		var spans []span
		for p.i < len(p.s) && p.s[p.i] != ']' {
			lo, err := p.char()
			if err != nil {
				return nil, err
			}
			hi := lo
			if p.i+1 < len(p.s) && p.s[p.i] == '-' && p.s[p.i+1] != ']' {
				p.i++
				if hi, err = p.char(); err != nil {
					return nil, err
				}
			}
			spans = append(spans, span{lo, hi})
		}
		p.i++
		return []gsym{{match: func(r rune) bool {
			in := false
			for _, s := range spans {
				in = in || s.lo <= r && r <= s.hi
			}
			return in != neg
		}}}, nil
	case c == '(':
		p.i++
		alts, err := p.alts()
		if err != nil {
			return nil, err
		}
		if p.i >= len(p.s) || p.s[p.i] != ')' {
			return nil, fmt.Errorf("unclosed group")
		}
		p.i++
		return []gsym{{rule: p.g.rule(alts)}}, nil
	default:
		j := p.i
		for j < len(p.s) && (p.s[j] == '-' || p.s[j] >= 'a' && p.s[j] <= 'z' || p.s[j] >= 'A' && p.s[j] <= 'Z' || p.s[j] >= '0' && p.s[j] <= '9') {
			j++
		}
		if j == p.i {
			return nil, fmt.Errorf("unexpected %q", p.s[p.i:])
		}
		name := p.s[p.i:j]
		p.i = j
		return []gsym{{rule: name}}, nil
	}
}

// char reads one character of a literal or class, an escape included.
func (p *gparser) char() (rune, error) {
	if p.s[p.i] != '\\' {
		r, w := utf8.DecodeRuneInString(p.s[p.i:])
		p.i += w
		return r, nil
	}
	if p.i+1 >= len(p.s) {
		return 0, fmt.Errorf("a \\ ends the rule")
	}
	p.i += 2
	switch c := p.s[p.i-1]; c {
	case 'n':
		return '\n', nil
	case 't':
		return '\t', nil
	case 'x':
		n, err := strconv.ParseUint(p.s[p.i:p.i+2], 16, 8)
		p.i += 2
		return rune(n), err
	default:
		return rune(c), nil
	}
}

// rule names a fresh rule with the given alternatives.
func (g *gbnf) rule(alts [][]gsym) string {
	g.fresh++
	name := fmt.Sprintf("\x00%d", g.fresh)
	g.prods[name] = alts
	return name
}

func (g *gbnf) repeat(item []gsym, op byte) []gsym {
	switch op {
	case '?':
		return []gsym{{rule: g.rule([][]gsym{nil, item})}}
	case '*':
		name := g.rule(nil)
		g.prods[name] = [][]gsym{nil, append(append([]gsym{}, item...), gsym{rule: name})}
		return []gsym{{rule: name}}
	default:
		name := g.rule(nil)
		g.prods[name] = [][]gsym{item, append(append([]gsym{}, item...), gsym{rule: name})}
		return []gsym{{rule: name}}
	}
}

type eitem struct {
	rule           string
	alt, dot, from int
}

// accepts runs Earley's recognizer from root over text.
func (g *gbnf) accepts(text string) bool {
	in := []rune(text)
	nullable := g.nullable()
	sets := make([][]eitem, len(in)+1)
	seen := make([]map[eitem]bool, len(in)+1)
	add := func(k int, it eitem) {
		if seen[k] == nil {
			seen[k] = map[eitem]bool{}
		}
		if !seen[k][it] {
			seen[k][it] = true
			sets[k] = append(sets[k], it)
		}
	}
	for a := range g.prods["root"] {
		add(0, eitem{"root", a, 0, 0})
	}
	for k := 0; k <= len(in); k++ {
		for n := 0; n < len(sets[k]); n++ {
			it := sets[k][n]
			body := g.prods[it.rule][it.alt]
			if it.dot == len(body) { // complete: advance whatever waited on it
				for _, w := range sets[it.from] {
					if wb := g.prods[w.rule][w.alt]; w.dot < len(wb) && wb[w.dot].rule == it.rule {
						add(k, eitem{w.rule, w.alt, w.dot + 1, w.from})
					}
				}
				continue
			}
			sym := body[it.dot]
			if sym.rule == "" {
				if k < len(in) && sym.match(in[k]) {
					add(k+1, eitem{it.rule, it.alt, it.dot + 1, it.from})
				}
				continue
			}
			for a := range g.prods[sym.rule] {
				add(k, eitem{sym.rule, a, 0, k})
			}
			if nullable[sym.rule] {
				add(k, eitem{it.rule, it.alt, it.dot + 1, it.from})
			}
		}
	}
	for _, it := range sets[len(in)] {
		if it.rule == "root" && it.from == 0 && it.dot == len(g.prods["root"][it.alt]) {
			return true
		}
	}
	return false
}

func (g *gbnf) nullable() map[string]bool {
	out := map[string]bool{}
	for changed := true; changed; {
		changed = false
		for name, alts := range g.prods {
			if out[name] {
				continue
			}
			for _, body := range alts {
				all := true
				for _, s := range body {
					all = all && s.rule != "" && out[s.rule]
				}
				if all {
					out[name], changed = true, true
					break
				}
			}
		}
	}
	return out
}

// An empty rule another rule reads twice in a row is the case Earley's recognizer misses without the
// nullable step (Aycock and Horspool): the second read is predicted after the first completed.
func TestTheRecognizerReadsEmptyRules(t *testing.T) {
	g, err := parseGBNF("root ::= a a \"x\"\na ::= \"\"\n")
	if err != nil {
		t.Fatal(err)
	}
	if !g.accepts("x") || g.accepts("") || g.accepts("xx") {
		t.Fatal(`root ::= a a "x" with a empty doesn't read exactly "x"`)
	}
}
