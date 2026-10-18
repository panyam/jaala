package guide_test

import (
	"context"
	"fmt"

	"github.com/panyam/jaala/datalog"
	"github.com/panyam/jaala/frontends/nl"
)

// DataCard writes what the data holds, for a pack's Card: each relation's
// facts, and each argument's values with how many facts hold each.
func Example_dataCard() {
	v, err := vocabulary(source())
	if err != nil {
		panic(err)
	}
	card, err := nl.DataCard(context.Background(), datalog.MustBase(v, source()))
	if err != nil {
		panic(err)
	}
	fmt.Print(card)
	// Output:
	// # The data: each relation's facts, and each argument's values with how many facts hold each (or how many values, and the commonest, when there are many).
	// # imports(from, to): 3 facts
	// #   from: "api" (1), "app" (1), "log" (1)
	// #   to: "api" (1), "log" (1), "util" (1)
	// # pkg(name, license): 4 facts
	// #   name: "api" (1), "app" (1), "log" (1), "util" (1)
	// #   license: absent (1), "" (1), "Apache-2.0" (1), "MIT" (1)
}

// A pack is the host's domain: what the data is about, what it holds,
// worked examples, and a check that refuses an answer that can't be right.
func Example_pack() {
	v, err := vocabulary(source())
	if err != nil {
		panic(err)
	}
	b := datalog.MustBase(v, source())
	pack := nl.Pack{
		About: "The packages of a Go service and what each imports.",
		Card:  "# Package names are lower case, such as app and util.",
		Examples: []nl.Example{
			{Question: "what does api import?", Program: `imports("api", ?d) => ?d`},
		},
		Check: func(_ context.Context, p nl.Program) error {
			if len(p.Rows) > 1 {
				return fmt.Errorf("the question asks for one package, and this answered %d", len(p.Rows))
			}
			return nil
		},
	}
	if err := pack.Validate(v); err != nil {
		panic(err)
	}
	fmt.Print(pack.Prefix())

	m := draftsByQuestion{"which package imports log?": {
		`imports(?p, ?q) => ?p`,
		`imports(?p, "log") => ?p`,
	}}
	p, tr, err := nl.Translate(context.Background(), "which package imports log?", v, nil, m, nl.Options{Base: b, Pack: &pack})
	if err != nil {
		fmt.Println(err)
		return
	}
	for _, t := range tr.Tries {
		fmt.Printf("try %q: %v\n", t.Kind, t.Err)
	}
	fmt.Println(p.Text, "answers", p.Rows[0].Bind["p"].S)
	// Output:
	// # What the data is about:
	// # The packages of a Go service and what each imports.
	//
	// # Package names are lower case, such as app and util.
	//
	// # Examples:
	// # what does api import?
	// imports("api", ?d) => ?d
	// try "answer": the question asks for one package, and this answered 3
	// try "": <nil>
	// imports(?p, "log") => ?p answers api
}
