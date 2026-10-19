package stdlib

import (
	"context"

	"github.com/panyam/jaala/ns"
)

// Absent registers absent(?x) at the root, the only way to ASK about a field the source did not state.
// Before Value.Absent existed such a field bound to the empty string, so it was not merely hard to
// select, it was indistinguishable from one that was stated as "". Its negation is the useful half as
// often as not: `not absent(?min)` reads "this row states a lower bound". It is not a string test,
// since it asks about any value, which is why it is not under str.
//
// It also registers coalesce(?a, ?b, ?v) at the root, which binds ?v to ?a unless ?a is absent, and to
// ?b otherwise (#284). Both inputs must be bound, absent counting as bound, so a value that may be
// absent (a min over nothing, a field the source left out) can fall back to another; more fallbacks
// nest, coalesce(?a, coalesce(?b, ?c)). Over two absent values it binds absent, which is a value,
// rather than answering no row.
func Absent(v *ns.Vocabulary) error {
	if err := v.AddPredicate("absent", ns.Builtin{
		Arity: 1, Labels: []string{"value"},
		Doc:   "reports whether the field carried no value at all, which is different from an empty string and from zero; `not absent(?x)` reads \"this field is stated\"",
		Holds: func(args []ns.Value) (bool, error) { return args[0].Absent, nil },
	}); err != nil {
		return err
	}
	return v.AddPredicate("coalesce", ns.Builtin{
		Arity:  3,
		Modes:  [][]bool{{true, true, false}},
		Labels: []string{"value", "fallback", "result"},
		Doc:    "binds the value, or the fallback when the value is absent",
		Gen: func(_ context.Context, _ ns.Source, args []ns.Arg, emit func([]ns.Value, []string) error) error {
			if !args[0].Bound || !args[1].Bound {
				return needsBound("coalesce", "its value and its fallback")
			}
			r := args[0].Value
			if r.Absent {
				r = args[1].Value
			}
			return emit([]ns.Value{args[0].Value, args[1].Value, r}, nil)
		},
	})
}
