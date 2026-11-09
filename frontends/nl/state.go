package nl

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"github.com/panyam/jaala/datalog"
	"github.com/panyam/jaala/ns"
)

// A memory is what one question's actions have shown (#393): the values the data was seen to hold,
// which the harness repairs a guessed value from, and, for Actions.Compact, the facts so far, which
// stand in for the conversation.
type memory struct {
	seen   map[string][]string // a value's spelling key to the values with that key
	facts  []string
	drafts []bool // whether each fact is a draft (a try, or a refused answer) rather than a finding
	last   string // the last refusal, whole, with what to do about it

	found, refused string // Prompts.CompactFound and CompactRefused, heading the compact turn's lists
}

// see records values the data was seen to hold.
func (m *memory) see(vals []string) {
	if m.seen == nil {
		m.seen = map[string][]string{}
	}
	for _, v := range vals {
		k := spelling(v)
		if k != "" && !slices.Contains(m.seen[k], v) {
			m.seen[k] = append(m.seen[k], v)
		}
	}
}

// spelling is what two spellings of one value share: its letters and digits, lower-cased, so "mit"
// and "MIT", or "Apache 2.0" and "Apache-2.0", meet.
func spelling(v string) string {
	var b strings.Builder
	for _, r := range v {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}

// quotedText finds a draft's quoted strings, in either quote Cypher takes.
var quotedText = regexp.MustCompile(`"([^"\\]*)"|'([^'\\]*)'`)

// repair rewrites each quoted value in text that the data doesn't hold as written but holds in
// exactly one other spelling, among the values seen and those of domain, and says what it changed;
// ok is false when nothing changed. It never guesses between two spellings.
func (m *memory) repair(text string, domain []string) (string, string, bool) {
	known := map[string][]string{}
	for k, vs := range m.seen {
		known[k] = append(known[k], vs...)
	}
	for _, d := range domain {
		if k := spelling(d); !slices.Contains(known[k], d) {
			known[k] = append(known[k], d)
		}
	}
	var changed []string
	out := quotedText.ReplaceAllStringFunc(text, func(q string) string {
		quote, val := q[:1], q[1:len(q)-1]
		vs := known[spelling(val)]
		if len(vs) != 1 || vs[0] == val || strings.Contains(vs[0], quote) {
			return q
		}
		changed = append(changed, fmt.Sprintf("%s%s%s to %s%s%s", quote, val, quote, quote, vs[0], quote))
		return quote + vs[0] + quote
	})
	if len(changed) == 0 {
		return text, "", false
	}
	return out, strings.Join(changed, ", "), true
}

// domainOf is the closed Domain of the argument a check error refused a value at, nil when it isn't
// one.
func domainOf(err error, v *ns.Vocabulary) []string {
	var ce *datalog.CheckError
	if !errors.As(err, &ce) || ce.Arg < 0 || ce.Relation == "" {
		return nil
	}
	e, lerr := v.Lookup(ce.Relation)
	if lerr != nil || ce.Arg >= len(e.Args) {
		return nil
	}
	return e.Args[ce.Arg].Domain
}

// repairable reports whether a refusal is one a value's spelling could cause: an empty answer, or a
// check refusing a constant at an argument.
func repairable(try Try) bool {
	var ce *datalog.CheckError
	return errors.Is(try.Err, errEmpty) || errors.As(try.Err, &ce) && ce.Arg >= 0
}

// factLimit caps one fact of the compact state, so a long look doesn't crowd out the rest.
const factLimit = 600

// fact is one action or refusal as the compact state lists it: the reply on one line, then what came
// back, capped at factLimit.
func fact(reply, result string) string {
	r := strings.TrimSpace(result)
	if len(r) > factLimit {
		r = r[:factLimit] + " …"
	}
	return fmt.Sprintf("- %s\n  %s", strings.Join(strings.Fields(reply), " "), strings.ReplaceAll(r, "\n", "\n  "))
}

// add records a fact, a draft's or a finding's.
func (m *memory) add(draft bool, f string) {
	m.facts = append(m.facts, f)
	m.drafts = append(m.drafts, draft)
}

// compact is the one turn Actions.Compact sends in place of the conversation: the question, every
// fact so far when facts is set (the Ledger shows them otherwise), and the last refusal whole.
func (m *memory) compact(ask string, facts bool) []Turn {
	if (!facts || len(m.facts) == 0) && m.last == "" {
		return []Turn{{RoleUser, ask}}
	}
	var b strings.Builder
	b.WriteString(ask)
	if facts && len(m.facts) > 0 {
		b.WriteString("\n\n" + m.found + "\n" + strings.Join(m.facts, "\n"))
	}
	if m.last != "" {
		b.WriteString("\n\n" + m.refused + "\n" + m.last)
	}
	return []Turn{{RoleUser, b.String()}}
}

// ledger is the facts split into findings and drafts, each list under its heading, "" when there
// are none.
func (m *memory) ledger(pr Prompts) string {
	var found, tried []string
	for i, f := range m.facts {
		if m.drafts[i] {
			tried = append(tried, f)
		} else {
			found = append(found, f)
		}
	}
	var parts []string
	if len(found) > 0 {
		parts = append(parts, pr.LedgerFound+"\n"+strings.Join(found, "\n"))
	}
	if len(tried) > 0 {
		parts = append(parts, pr.LedgerTried+"\n"+strings.Join(tried, "\n"))
	}
	return strings.Join(parts, "\n\n")
}
