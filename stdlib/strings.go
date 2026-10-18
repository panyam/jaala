package stdlib

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/panyam/jaala/ns"
)

// Strings registers the string predicates under str: the tests str.contains, str.prefix and
// str.suffix, the two pattern tests str.glob (see CompileGlob) and str.match (see CompilePattern),
// str.distance, which binds the edit distance between two strings, and the functions that build
// text (#284): str.concat, str.replace, str.lower, str.upper, str.capitalize and str.split.
func Strings(v *ns.Vocabulary) error {
	for _, p := range []struct {
		path string
		b    ns.Builtin
	}{
		{"str.contains", strFilter(strings.Contains, "substring", "reports whether a string contains a substring")},
		{"str.prefix", strFilter(strings.HasPrefix, "prefix", "reports whether a string starts with a prefix")},
		{"str.suffix", strFilter(strings.HasSuffix, "suffix", "reports whether a string ends with a suffix")},
		{"str.glob", patFilter(CompileGlob, "pattern", "reports whether the whole string matches a SQLite-style glob (`*` any run, `?` one character, `[a-z]` or `[^a-z]` one of a class, `[[]` a literal `[`)")},
		{"str.match", patFilter(CompilePattern, "regex", "reports whether the string matches an unanchored regular expression")},
		{"str.distance", strDistance()},
		{"str.concat", strConcat()},
		{"str.replace", textFn("str.replace", []string{"string", "old", "new", "result"},
			"binds the string with every occurrence of old replaced by new",
			func(a []string) string { return strings.ReplaceAll(a[0], a[1], a[2]) })},
		{"str.lower", textFn("str.lower", []string{"string", "lower"}, "binds the string in lower case",
			func(a []string) string { return strings.ToLower(a[0]) })},
		{"str.upper", textFn("str.upper", []string{"string", "upper"}, "binds the string in upper case",
			func(a []string) string { return strings.ToUpper(a[0]) })},
		{"str.capitalize", textFn("str.capitalize", []string{"string", "capitalized"},
			"binds the string with its first character in upper case and the rest as written", capitalize)},
		{"str.split", strSplit()},
	} {
		if err := v.AddPredicate(p.path, p.b); err != nil {
			return err
		}
	}
	return nil
}

// strFilter wraps a string(value, pattern) bool as a 2-arity filter (the shape of
// str.contains/str.prefix/str.suffix).
func strFilter(fn func(s, pat string) bool, second, doc string) ns.Builtin {
	b := ns.Filter(2, func(args []ns.Value) (bool, error) { return fn(args[0].S, args[1].S), nil })
	return stringTest(b, second, doc)
}

// stringTest labels a two-argument string test and types both arguments as strings.
func stringTest(b ns.Builtin, second, doc string) ns.Builtin {
	b.Labels = []string{"string", second}
	b.Types = []ns.ArgType{{Type: ns.TypeString}, {Type: ns.TypeString}}
	b.Doc = doc
	return b
}

// patFilter is strFilter for the two PATTERN predicates (str.glob, str.match): the pattern must be compiled
// before it can be tested, so a malformed one is an EVAL ERROR rather than a non-match. That
// direction matters — a bad pattern that quietly matched nothing would read as "the data is clean"
// on a completeness check.
func patFilter(compile func(string) (*regexp.Regexp, error), second, doc string) ns.Builtin {
	return stringTest(ns.Filter(2, func(args []ns.Value) (bool, error) {
		re, err := compile(args[1].S)
		if err != nil {
			return false, err
		}
		return re.MatchString(args[0].S), nil
	}), second, doc)
}

// strDistance is str.distance(a, b, d): d is the Levenshtein distance between a and b, counted in
// characters (runes), so a non-ASCII letter is one edit. It needs both strings bound (#77), since
// enumerating the strings within a distance of another is not a finite answer. A planner waits for
// that, but a body run as written can call it early, which is an error as it is for a filter. With d
// bound too the engine unifies the emitted distance against it, so it tests.
func strDistance() ns.Builtin {
	return ns.Builtin{
		Arity:  3,
		Modes:  [][]bool{{true, true, false}},
		Labels: []string{"string", "other", "distance"},
		Types:  []ns.ArgType{{Type: ns.TypeString}, {Type: ns.TypeString}, {Type: ns.TypeNumber}},
		Doc:    "binds the edit (Levenshtein) distance between two strings, in characters; both strings must be bound",
		Gen: func(_ context.Context, _ ns.Source, args []ns.Arg, emit func([]ns.Value, []string) error) error {
			if !args[0].Bound || !args[1].Bound {
				return errors.New("query: str.distance needs both strings bound (a variable must appear in a relation before str.distance measures it)")
			}
			a, b := args[0].Value, args[1].Value
			return emit([]ns.Value{a, b, ns.N(float64(levenshtein(a.S, b.S)))}, nil)
		},
	}
}

// levenshtein is the fewest single-character insertions, deletions and substitutions that turn a
// into b, over runes, keeping one row of the table.
func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	row := make([]int, len(rb)+1)
	for j := range row {
		row[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		diag := row[0]
		row[0] = i
		for j := 1; j <= len(rb); j++ {
			up := row[j]
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			row[j] = min(row[j]+1, row[j-1]+1, diag+cost)
			diag = up
		}
	}
	return row[len(rb)]
}

// needsBound is the error a text function gives when it runs before what it reads has a value, which
// only a body run as written can do: the planner waits for its mode.
func needsBound(name, what string) error {
	return fmt.Errorf("query: %s needs %s bound (a variable must appear in a relation before %s reads it)", name, what, name)
}

// texts reads args as text, reporting false when one is unbound or absent. An absent value has no
// text to build from, so a function over one gives no row, as num.* computes nothing from absent.
func texts(args []ns.Arg) ([]string, bool) {
	out := make([]string, len(args))
	for i, a := range args {
		if !a.Bound || a.Value.Absent {
			return nil, false
		}
		out[i] = a.Value.S
	}
	return out, true
}

func absentIn(args []ns.Arg) bool {
	for _, a := range args {
		if a.Bound && a.Value.Absent {
			return true
		}
	}
	return false
}

// textFn is a function of its leading text arguments into its last, run forward only: with the
// result bound too, the engine unifies the emitted result against it, so it tests.
func textFn(name string, labels []string, doc string, f func([]string) string) ns.Builtin {
	n := len(labels)
	mode := make([]bool, n)
	types := make([]ns.ArgType, n)
	for i := range n {
		mode[i] = i < n-1
		types[i] = ns.ArgType{Type: ns.TypeString}
	}
	return ns.Builtin{
		Arity:  n,
		Modes:  [][]bool{mode},
		Labels: labels,
		Types:  types,
		Doc:    doc,
		Gen: func(_ context.Context, _ ns.Source, args []ns.Arg, emit func([]ns.Value, []string) error) error {
			in, ok := texts(args[:n-1])
			if !ok {
				if absentIn(args[:n-1]) {
					return nil
				}
				return needsBound(name, "every argument but its result")
			}
			vals := make([]ns.Value, n)
			for i, a := range args[:n-1] {
				vals[i] = a.Value
			}
			vals[n-1] = ns.S(f(in))
			return emit(vals, nil)
		},
	}
}

func capitalize(a []string) string {
	r, size := utf8.DecodeRuneInString(a[0])
	if size == 0 {
		return a[0]
	}
	return string(unicode.ToUpper(r)) + a[0][size:]
}

// strConcat is str.concat(head, tail, text): text is head followed by tail. Like num.add it runs from
// any two of the three, so with text bound it cuts a head or a tail off, giving no row when text
// doesn't start with the head or end with the tail: str.concat("add_", ?rest, ?name) is Go's
// strings.CutPrefix.
func strConcat() ns.Builtin {
	return ns.Builtin{
		Arity:  3,
		Modes:  [][]bool{{true, true, false}, {true, false, true}, {false, true, true}},
		Labels: []string{"head", "tail", "text"},
		Types:  []ns.ArgType{{Type: ns.TypeString}, {Type: ns.TypeString}, {Type: ns.TypeString}},
		Doc:    "text is head followed by tail, run from any two of the three; cutting a head or tail off holds for none when text lacks it",
		Gen: func(_ context.Context, _ ns.Source, args []ns.Arg, emit func([]ns.Value, []string) error) error {
			if absentIn(args) {
				return nil
			}
			head, tail, text := args[0], args[1], args[2]
			switch {
			case head.Bound && tail.Bound:
				return emit([]ns.Value{head.Value, tail.Value, ns.S(head.Value.S + tail.Value.S)}, nil)
			case head.Bound && text.Bound:
				if rest, ok := strings.CutPrefix(text.Value.S, head.Value.S); ok {
					return emit([]ns.Value{head.Value, ns.S(rest), text.Value}, nil)
				}
				return nil
			case tail.Bound && text.Bound:
				if rest, ok := strings.CutSuffix(text.Value.S, tail.Value.S); ok {
					return emit([]ns.Value{ns.S(rest), tail.Value, text.Value}, nil)
				}
				return nil
			}
			return needsBound("str.concat", "two of its three values")
		},
	}
}

// strSplit is str.split(string, separator, index, part): one row per part, numbered from 0. With
// index bound it gives that part alone, and a negative index counts from the end, so -1 is the last
// part (a hierarchical name's leaf) and -2 the one before it. An index past either end, or not a
// whole number, holds for none. The parts are Go's strings.Split: an empty separator splits between
// characters, and a string without the separator is one part, itself.
func strSplit() ns.Builtin {
	return ns.Builtin{
		Arity:  4,
		Modes:  [][]bool{{true, true, false, false}},
		Labels: []string{"string", "separator", "index", "part"},
		Types:  []ns.ArgType{{Type: ns.TypeString}, {Type: ns.TypeString}, {Type: ns.TypeNumber}, {Type: ns.TypeString}},
		Doc:    "binds each part of the string between separators with its index from 0; given an index, binds that part alone, a negative index counting from the end (-1 is the last part)",
		Gen: func(_ context.Context, _ ns.Source, args []ns.Arg, emit func([]ns.Value, []string) error) error {
			in, ok := texts(args[:2])
			if !ok {
				if absentIn(args[:2]) {
					return nil
				}
				return needsBound("str.split", "the string and separator")
			}
			parts := strings.Split(in[0], in[1])
			if args[2].Bound {
				idx := args[2].Value
				if idx.Num == nil || *idx.Num != float64(int(*idx.Num)) {
					return nil
				}
				i := int(*idx.Num)
				if i < 0 {
					i += len(parts)
				}
				if i < 0 || i >= len(parts) {
					return nil
				}
				return emit([]ns.Value{args[0].Value, args[1].Value, idx, ns.S(parts[i])}, nil)
			}
			for i, p := range parts {
				if err := emit([]ns.Value{args[0].Value, args[1].Value, ns.N(float64(i)), ns.S(p)}, nil); err != nil {
					return err
				}
			}
			return nil
		},
	}
}
