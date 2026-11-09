package nl

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/panyam/jaala/ns"
)

// A Sample is what the model is shown of one of Pack.Preview's targets before its first draft (#514).
type Sample struct {
	// Target is the relation or values target the pack named.
	Target string
	// Text says what was read and lists its rows, as the values action writes them:
	//
	//	pkg(name, license) answered its first 5 rows:
	//	  "app", "MIT"
	Text string
}

// PreviewRows is how many rows or values a Sample shows.
const PreviewRows = 5

// Preview reads Pack.Preview's targets for the question over Options.Base, in the pack's order, which
// is the order a loop shows them in. A relation is read as its rows and anything else as a values
// target, through the front end when it is a Prober. The text values the samples show are kept, as
// an action's are, so a later answer guessing one's spelling can be repaired. It reads once per
// Session and returns the same samples after. err joins the targets it couldn't read (an unknown
// name, a predicate, nothing to show), each named; the others are still returned. Without
// Pack.Preview or a Base it returns nothing.
func (s *Session) Preview(ctx context.Context) ([]Sample, error) {
	if s.previewed {
		return s.preview, s.previewErr
	}
	s.previewed = true
	if s.o.Pack == nil || s.o.Pack.Preview == nil || s.o.Base == nil {
		return nil, nil
	}
	var errs []error
	for _, target := range s.o.Pack.Preview(ctx, s.question) {
		text, seen, err := s.sample(ctx, target)
		if err != nil {
			errs = append(errs, fmt.Errorf("preview %s: %w", target, err))
			continue
		}
		s.mem.see(seen)
		s.preview = append(s.preview, Sample{Target: target, Text: text})
	}
	s.previewErr = errors.Join(errs...)
	return s.preview, s.previewErr
}

// sample reads one target: a relation's rows in jaala's language, or a values target's values.
func (s *Session) sample(ctx context.Context, target string) (string, []string, error) {
	o := s.o
	if _, ok := o.FrontEnd.(Prober); !ok {
		o.FrontEnd = nil // the programs below are jaala's
		if e, err := s.v.Lookup(target); err == nil && (e.Kind == ns.EntryBase || e.Kind == ns.EntryDerived) {
			prog, what := rowsProgram(target, e.Args, PreviewRows)
			return runRows(ctx, prog, s.v, o, what, PreviewRows)
		}
	}
	probe, err := Probe(s.v, o.FrontEnd, target, PreviewRows)
	if err != nil {
		return "", nil, err
	}
	return runRows(ctx, probe, s.v, o, "values of "+target, PreviewRows)
}

// rowsProgram lists up to limit of rel's rows, a variable per argument, and what it reads as the
// signature's names (pkg(name, license)).
func rowsProgram(rel string, args []ns.ArgSig, limit int) (string, string) {
	vars := make([]string, len(args))
	names := make([]string, len(args))
	taken := map[string]bool{}
	for i, a := range args {
		names[i] = a.Name
		name := a.Name
		if !isVarName(name) || taken[name] {
			name = fmt.Sprintf("a%d", i)
		}
		taken[name] = true
		vars[i] = "?" + name
	}
	cols := strings.Join(vars, ", ")
	return fmt.Sprintf("%s(%s) => %s limit %d", rel, cols, cols, limit), fmt.Sprintf("%s(%s)", rel, strings.Join(names, ", "))
}

// isVarName says name can follow ? as a named variable: a letter, then letters, digits or _.
func isVarName(name string) bool {
	if name == "" || name == "_" {
		return false
	}
	for i, r := range name {
		letter := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'
		if !letter && (i == 0 || r != '_' && (r < '0' || r > '9')) {
			return false
		}
	}
	return true
}
