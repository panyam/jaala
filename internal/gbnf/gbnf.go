// Package gbnf holds the front ends' grammars as tables (Rule, Render, Sampler: nl's program syntax,
// #323, and the Cypher subset, #354), and reads the GBNF they render to recognize text against it,
// for the tests that check a grammar against its parser (#324). Only the front ends import it; the
// engine doesn't.
package gbnf

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Grammar is a GBNF grammar compiled to plain productions over single characters, recognized by Earley's
// algorithm. It reads the subset nl renders: rules, alternatives, string literals, character classes
// with ranges, negation and \xNN escapes, groups, and ? * + {m,n}.
type Grammar struct {
	prods map[string][][]gsym
	fresh int
}

// gsym is a nonterminal (rule set) or one character matching match.
type gsym struct {
	rule  string
	match func(r rune) bool
}

// Parse reads a grammar in the subset nl renders, refusing anything else.
func Parse(text string) (*Grammar, error) {
	g := &Grammar{prods: map[string][][]gsym{}}
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
	g *Grammar
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
		} else if p.i < len(p.s) && p.s[p.i] == '{' {
			end := strings.IndexByte(p.s[p.i:], '}')
			lo, hi, ok := strings.Cut(p.s[p.i+1:p.i+max(end, 1)], ",")
			m, err1 := strconv.Atoi(lo)
			n, err2 := strconv.Atoi(hi)
			if end < 0 || !ok || err1 != nil || err2 != nil || m < 0 || n < m {
				return nil, fmt.Errorf("gbnf: a repetition other than {m,n} at %d in %q", p.i, p.s)
			}
			item = p.g.bounded(item, m, n)
			p.i += end + 1
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
func (g *Grammar) rule(alts [][]gsym) string {
	g.fresh++
	name := fmt.Sprintf("\x00%d", g.fresh)
	g.prods[name] = alts
	return name
}

func (g *Grammar) repeat(item []gsym, op byte) []gsym {
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

// bounded is item m to n times: m copies, then n-m optional ones nested, (x (x)?)?, so Earley finds
// one parse of a run rather than one per way of spreading it over flat x? x?.
func (g *Grammar) bounded(item []gsym, m, n int) []gsym {
	var tail []gsym
	for i := m; i < n; i++ {
		tail = []gsym{{rule: g.rule([][]gsym{nil, append(append([]gsym{}, item...), tail...)})}}
	}
	var out []gsym
	for i := 0; i < m; i++ {
		out = append(out, item...)
	}
	return append(out, tail...)
}

type eitem struct {
	rule           string
	alt, dot, from int
}

// Accepts runs Earley's recognizer from root over text.
func (g *Grammar) Accepts(text string) bool {
	in, sets := g.earley(text)
	for _, it := range sets[len(in)] {
		if it.rule == "root" && it.from == 0 && it.dot == len(g.prods["root"][it.alt]) {
			return true
		}
	}
	return false
}

// AcceptsPrefix reports whether text starts some text the grammar accepts, as a reply a decoder cut
// off at its token limit does when it kept to the grammar.
func (g *Grammar) AcceptsPrefix(text string) bool {
	in, sets := g.earley(text)
	return len(sets[len(in)]) > 0
}

func (g *Grammar) earley(text string) ([]rune, [][]eitem) {
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
	return in, sets
}

func (g *Grammar) nullable() map[string]bool {
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
