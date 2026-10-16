package jaala_test

import (
	"go/build"
	"slices"
	"strings"
	"testing"
)

// The packages layer one way, and the layering is what lets a host use part of jaala without the
// rest: a fact layer that may not import an engine (agni's C29) imports ns, and stdlib for the
// standard predicates, and never pulls in datalog.
//
//	ns       the contract                imports nothing in jaala
//	stdlib   the standard vocabulary     imports ns, never datalog
//	datalog  the engine                  imports ns only
//	frontends/nl        the plain-language front end (#321)   imports ns and datalog, and nothing imports it
//	frontends/nlmodels  clients for local models (#330)       import frontends/nl, in a module of their own
//	internal/gbnf  a GBNF recognizer      imports nothing; only tests import it (#324)
//
// Test files are not checked: datalog's tests use stdlib for their fixtures.
func TestLayering(t *testing.T) {
	const mod = "github.com/panyam/jaala/"
	allowed := map[string][]string{
		"ns":                          {},
		"stdlib":                      {"ns"},
		"datalog":                     {"ns"},
		"frontends/nl":                {"ns", "datalog"},
		"frontends/nlmodels/llamacpp": {"frontends/nl"},
		"frontends/nlmodels/ollama":   {"frontends/nl"},
		"internal/gbnf":               {},
	}
	for dir, may := range allowed {
		pkg, err := build.ImportDir(dir, 0)
		if err != nil {
			t.Fatalf("%s: %v", dir, err)
		}
		for _, imp := range pkg.Imports {
			if rest, ok := strings.CutPrefix(imp, mod); ok && !slices.Contains(may, rest) {
				t.Errorf("%s imports %s; it may import only %v from jaala", dir, rest, may)
			}
		}
		// Positive control: what a package is meant to import, it does, so this check can see imports.
		for _, want := range may {
			if !slices.Contains(pkg.Imports, mod+want) {
				t.Errorf("control: %s does not import %s, so this check is reading the wrong thing", dir, want)
			}
		}
	}
}
