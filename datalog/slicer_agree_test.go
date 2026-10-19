package datalog

import (
	"math/rand"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The tokenizer (#167) holds the parser it replaced (slicer_test.go) to its answers: whatever the
// slicer read, the new parser reads as the same Query, and whatever it refused is refused, unless the
// slicer refused a spelling of a query it reads (readsAsWritten).

// slicerDiffs compares the parsers on text: diff when they accept or read it differently, or, when
// both refuse it, msg when their messages differ.
func slicerDiffs(text string) (diff, msg string) {
	q, err := Parse(text)
	sq, serr := slParse(text)
	switch {
	case serr != nil && namesFacts.MatchString(text):
		return "", "" // the slicer never read `atom as ?r` (#279), so it refuses it in its own words
	case serr != nil && optionalBlock.MatchString(text):
		return "", "" // nor `optional { ... }` (#282)
	case err == nil && serr != nil && (readsAsWritten(q) || agreesWithoutExponents(text)):
		return "", "" // the slicer refused a spelling of a query it reads, as #276 allows
	case err == nil && serr == nil && unknownAggregate(sq) && readsAsWritten(q):
		return "", "" // the slicer read an expression with whitespace in it as an aggregate nothing evaluates
	case (err == nil) != (serr == nil):
		diff = "accepts differently: " + errText(err) + " | slicer: " + errText(serr)
	case err == nil && !reflect.DeepEqual(q, sq):
		diff = "reads differently:\n  " + queryText(q) + "\n  slicer: " + queryText(sq)
	case err != nil && err.Error() != serr.Error():
		return "", err.Error() + " | slicer: " + serr.Error()
	default:
		return "", ""
	}
	return diff, ""
}

// readsAsWritten reports whether the slicer reads q, written out, as q: the text the tokenizer accepted
// spelled a query the slicer knew, with whitespace it didn't skip (?a *\n2), and means what that query
// means.
func readsAsWritten(q Query) bool {
	sq, err := slParse(queryString(q))
	return err == nil && reflect.DeepEqual(q, sq)
}

// agreesWithoutExponents reports whether the parsers agree on text with every exponent written as a
// plain decimal, the other allowances included (0(1e-2\r*3) has whitespace in it too). The slicer cut
// a number like 1e-3 inside arithmetic at its sign, and refused it in whichever words the piece it was
// reading led to (`bare identifier "1e"`, the whole head argument, `malformed aggregate` for one in
// parentheses).
func agreesWithoutExponents(text string) bool {
	plain := exponent.ReplaceAllStringFunc(text, func(n string) string {
		f, err := strconv.ParseFloat(n, 64)
		if err != nil {
			return n
		}
		return strconv.FormatFloat(f, 'f', -1, 64)
	})
	if plain == text {
		return false
	}
	diff, _ := slicerDiffs(plain)
	return diff == ""
}

// namesFacts matches an `as` after a closing parenthesis, where the parser reads `atom as ?r` (#279).
var namesFacts = regexp.MustCompile(`\)\s*as\b`)

// optionalBlock matches `optional {`, which the parser reads as an optional (#282).
var optionalBlock = regexp.MustCompile(`\boptional\s*\{`)

var exponent = regexp.MustCompile(`\b[0-9]+(\.[0-9]+)?[eE][+-]?[0-9]+`)

// unknownAggregate reports whether q has an aggregate Validate refuses as unknown: the slicer read
// `-\n(?a)`, an expression its reader didn't skip whitespace in, as the aggregate `-` over ?a.
func unknownAggregate(q Query) bool {
	terms := append([]Term(nil), q.Select...)
	for _, h := range q.Having {
		terms = append(terms, h.Left)
	}
	for _, o := range q.OrderBy {
		terms = append(terms, o.Term)
	}
	for _, t := range terms {
		if t.Agg != nil && !validAggFunc(t.Agg.Func) {
			return true
		}
	}
	return false
}

// queryString writes q as text Parse reads back as q.
func queryString(q Query) string {
	var b strings.Builder
	for _, r := range q.Rules {
		b.WriteString(r.String() + "; ")
	}
	b.WriteString(q.Goal.String())
	cols := func(ts []Term) string {
		out := make([]string, len(ts))
		for i, t := range ts {
			out[i] = t.String()
			if t.Var == "_" {
				out[i] = "?_" // a column reads ?_ as _, which prints as a term
			}
		}
		return strings.Join(out, ", ")
	}
	if len(q.Select)+len(q.Having)+len(q.OrderBy)+len(q.After) > 0 || q.Limit > 0 || q.Offset > 0 {
		b.WriteString(" => " + cols(q.Select))
	}
	if len(q.Having) > 0 {
		var hs []string
		for _, h := range q.Having {
			hs = append(hs, cols([]Term{h.Left})+" "+h.Op+" "+h.Right.String())
		}
		b.WriteString(" having " + strings.Join(hs, ", "))
	}
	if len(q.OrderBy) > 0 {
		var os []string
		for _, o := range q.OrderBy {
			os = append(os, cols([]Term{o.Term})+map[bool]string{true: " desc", false: ""}[o.Desc])
		}
		b.WriteString(" order by " + strings.Join(os, ", "))
	}
	if len(q.After) > 0 {
		var as []string
		for _, v := range q.After {
			v := v
			as = append(as, Term{Const: &v}.String())
		}
		b.WriteString(" after " + strings.Join(as, ", "))
	}
	if q.Limit > 0 {
		b.WriteString(" limit " + strconv.Itoa(q.Limit))
	}
	if q.Offset > 0 {
		b.WriteString(" offset " + strconv.Itoa(q.Offset))
	}
	return b.String()
}

func queryText(q Query) string {
	var b strings.Builder
	for _, r := range q.Rules {
		b.WriteString(r.String() + "; ")
	}
	b.WriteString(q.Goal.String())
	return b.String()
}

func FuzzParseAgreesWithSlicer(f *testing.F) {
	addSeeds(f)
	f.Fuzz(func(t *testing.T, text string) {
		if diff, _ := slicerDiffs(text); diff != "" {
			t.Fatalf("%q: %s", text, diff)
		}
	})
}

// A program with a typo in it gets the message the slicer gave it, word for word: hosts match on these
// (CLAUDE.md). The typos are the corpus's programs with one character deleted, inserted or replaced.
func TestATypoGetsTheSlicersMessage(t *testing.T) {
	chars := []byte(`(),;.:?"_ -=<>!#{}*+/a1`)
	refused, failed := 0, 0
	for seed := int64(1); seed <= 300 && failed < 5; seed++ {
		rnd := rand.New(rand.NewSource(seed))
		text := newGenerator(rnd, genGraph(rnd)).program().String()
		for range 60 {
			b := []byte(text)
			i := rnd.Intn(len(b))
			switch rnd.Intn(3) {
			case 0:
				b = append(b[:i:i], b[i+1:]...)
			case 1:
				b = append(b[:i:i], append([]byte{chars[rnd.Intn(len(chars))]}, b[i:]...)...)
			default:
				b[i] = chars[rnd.Intn(len(chars))]
			}
			if _, err := Parse(string(b)); err != nil {
				refused++
			}
			if diff, msg := slicerDiffs(string(b)); diff != "" || msg != "" {
				failed++
				t.Errorf("%q: %s%s", b, diff, msg)
			}
		}
	}
	if refused < 5000 {
		t.Errorf("control: only %d of 18000 typos were refused", refused)
	}
}
