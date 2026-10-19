package stdlib

import (
	"context"
	"math"
	"strings"

	"github.com/panyam/jaala/ns"
)

// ListValues registers the functions under list that read a list value (jaala#283), the text the list
// aggregate writes (ns.ListText), or one written in a query: list.member, list.length and list.at. Each reads its list from the
// text alone, so it works on a list from any rule, a Source's column or a host's binding. Indexes
// count from 0, and list.at's negative index from the end. A value that isn't a list, absent included,
// gives no row: which values reach a function depends on the plan, so an error there would make one
// evaluator fail where another answers (as path.* found, jaala#280).
func ListValues(v *ns.Vocabulary) error {
	listArg := ns.ArgType{Type: ns.TypeList}
	for _, p := range []struct {
		path string
		b    ns.Builtin
	}{
		{"list.member", listFn("list.member", []string{"list", "index", "value"}, []ns.ArgType{listArg, {Type: ns.TypeNumber}, {}},
			"binds each value of the list with its index from 0; given a value, binds where it is",
			func(vals []ns.Value, _ []ns.Arg, emit func(...ns.Value) error) error {
				// a bound index or value is the engine's to match each row against, as for any generator
				for i, x := range vals {
					if err := emit(ns.N(float64(i)), x); err != nil {
						return err
					}
				}
				return nil
			})},
		{"list.length", listFn("list.length", []string{"list", "length"}, []ns.ArgType{listArg, {Type: ns.TypeNumber}},
			"binds how many values the list holds",
			func(vals []ns.Value, _ []ns.Arg, emit func(...ns.Value) error) error {
				return emit(ns.N(float64(len(vals))))
			})},
		{"list.at", listFn("list.at", []string{"list", "index", "value"}, []ns.ArgType{listArg, {Type: ns.TypeNumber}, {}},
			"binds the value at an index from 0, a negative one counting from the end (-1 is the last)",
			func(vals []ns.Value, args []ns.Arg, emit func(...ns.Value) error) error {
				i, ok := wholeIndex(args[1])
				if !ok {
					return nil
				}
				if i < 0 {
					i += len(vals)
				}
				if i < 0 || i >= len(vals) {
					return nil
				}
				return emit(args[1].Value, vals[i])
			})},
	} {
		if err := v.AddPredicate(p.path, p.b); err != nil {
			return err
		}
	}
	return nil
}

// listFn is a function of a list (its first argument) into the rest. read is handed the list's values
// and every argument, and emits the arguments after the list. list.at needs its index bound too.
func listFn(name string, labels []string, types []ns.ArgType, doc string, read func([]ns.Value, []ns.Arg, func(...ns.Value) error) error) ns.Builtin {
	mode := make([]bool, len(labels))
	mode[0] = true
	if name == "list.at" {
		mode[1] = true
	}
	return ns.Builtin{
		Arity:  len(labels),
		Modes:  [][]bool{mode},
		Labels: labels,
		Types:  types,
		Doc:    doc,
		Gen: func(_ context.Context, _ ns.Source, args []ns.Arg, emit func([]ns.Value, []string) error) error {
			if !args[0].Bound {
				return needsBound(name, "its list")
			}
			if args[0].Value.Absent || args[0].Value.Num != nil {
				return nil
			}
			vals, ok := readList(args[0].Value.S)
			if !ok {
				return nil
			}
			return read(vals, args, func(out ...ns.Value) error {
				return emit(append([]ns.Value{args[0].Value}, out...), nil)
			})
		},
	}
}

// readList reads a list's values, from the text ListText writes or from a list written in a query's
// text, whose quotes the parser keeps escaped ("[\"a\"]", jaala#314).
func readList(s string) ([]ns.Value, bool) {
	if vals, err := ns.ParseList(s); err == nil {
		return vals, true
	}
	if !strings.Contains(s, `\"`) {
		return nil, false
	}
	vals, err := ns.ParseList(strings.ReplaceAll(s, `\"`, `"`))
	return vals, err == nil
}

// wholeIndex reads a bound index, false when it isn't a whole number.
func wholeIndex(a ns.Arg) (int, bool) {
	if !a.Bound || a.Value.Num == nil || *a.Value.Num != math.Trunc(*a.Value.Num) || math.Abs(*a.Value.Num) > math.MaxInt32 {
		return 0, false
	}
	return int(*a.Value.Num), true
}
