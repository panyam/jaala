package guide_test

import (
	"context"
	"errors"
	"fmt"

	"github.com/panyam/jaala/datalog"
	"github.com/panyam/jaala/ns"
)

// Lookup tells a host what a path names: a module's members, or a member's
// signature, doc and where it was defined.
func Example_lookup() {
	src := source()
	v, err := vocabulary(src)
	if err != nil {
		fmt.Println(err)
		return
	}
	mod, _ := v.Lookup("deps")
	fmt.Println("deps holds", mod.Members)

	m, _ := v.Lookup("deps.depends_on")
	fmt.Println(m.Signature())
	fmt.Println(m.Doc)
	fmt.Println("defined in", m.Origin)

	b, _ := datalog.NewBase(v, src)
	q := datalog.MustParse(`deps._step(?a, ?b) => ?a`)
	_, err = datalog.SemiNaive{}.Eval(context.Background(), q, b)
	fmt.Println(err)
	// Output:
	// deps holds [deps.depends_on]
	// deps.depends_on(a, b)
	// Everything a package depends on, directly or through others.
	// defined in deps.dl
	// query: unknown relation "deps._step"; "deps" holds depends_on
}

// Check reports a mistake in a module before any query runs, and a
// ModuleError says which module and file it came from.
func Example_moduleError() {
	v, _ := vocabulary(source())
	err := v.AddModule("broken", "datalog",
		`orphan(?x, ?y) :- imports(?x, _);`, "broken.dl")
	if err == nil {
		err = v.Check()
	}
	fmt.Println(err)
	var me *ns.ModuleError
	if errors.As(err, &me) {
		fmt.Println("module", me.Path, "from", me.Origin)
	}
	// Output:
	// query: rule "broken.orphan" head variable ?y is not bound by a positive body relation
	// module broken from broken.dl
}
