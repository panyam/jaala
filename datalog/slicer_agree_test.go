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
	case err == nil && serr != nil && readsAsWritten(q):
		return "", "" // the slicer refused a spelling of a query it reads, as #276 allows
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
// spelled a query the slicer knew, with whitespace it didn't skip (?a *\n2) or an exponent it cut at
// its sign (?a * 1e-3), and means what that query means.
func readsAsWritten(q Query) bool {
	sq, err := slParse(queryString(q))
	return err == nil && reflect.DeepEqual(q, sq) || err != nil && exponentInExpression.MatchString(err.Error())
}

// exponentInExpression is the slicer refusing a number like 1e-3 inside arithmetic, which it read up to
// the sign and then as 1e: `bare identifier "1e"`.
var exponentInExpression = regexp.MustCompile(`bare identifier "[+-]?[0-9]+(\.[0-9]+)?[eE]"`)

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
