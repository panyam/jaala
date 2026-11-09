package nl

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/panyam/jaala/datalog"
)

// A Shape says how a host's graph is laid out in its relations (#514 piece 5), so jaala can find
// the joins between two kinds of node without the model writing them. It names relations only.
type Shape struct {
	// Kinds is the relation holding each node's kind, of two arguments: id, then kind. Declaire's is
	// node(id, kind).
	Kinds string
	// Edges are relations of two node ids: source, then target, such as calls(src, dst).
	Edges []string
	// Refs are attributes whose value is a node id, such as Declaire's attr(id, "callee", id).
	Refs []Ref
}

// A Ref is an attribute relation of three arguments (id, key, value) whose value under Key names
// another node.
type Ref struct {
	Relation, Key string
}

// A Link is one way the data joins two kinds of node: an edge relation, or a Ref, seen in the data
// from a node of kind From to one of kind To, Count times.
type Link struct {
	Relation string
	Key      string // set for a Ref
	From, To string
	Count    int
}

// KindGraph reads which kinds of node each of shape's edges and refs joins, over b: the vocabulary
// can't say, since a node's kind is data. It runs one counting program per edge and ref, so a host
// computes it once per data version. Links come sorted by relation, key and kinds.
func KindGraph(ctx context.Context, b *datalog.Base, shape Shape, eval ...datalog.Option) ([]Link, error) {
	var out []Link
	read := func(rel, key, hop string) error {
		prog := fmt.Sprintf("%s, %s(?s, ?ks), %s(?d, ?kd) => ?ks, ?kd, count(?s)", hop, shape.Kinds, shape.Kinds)
		rows, err := evalText(ctx, b, prog, 0, eval)
		if err != nil {
			return fmt.Errorf("nl: the kinds %s joins: %w", rel, err)
		}
		for _, r := range rows {
			n := 0
			if c := r.Bind["count(s)"]; c.Num != nil {
				n = int(*c.Num)
			}
			out = append(out, Link{Relation: rel, Key: key, From: r.Bind["ks"].S, To: r.Bind["kd"].S, Count: n})
		}
		return nil
	}
	for _, e := range shape.Edges {
		if err := read(e, "", fmt.Sprintf("%s(?s, ?d)", e)); err != nil {
			return nil, err
		}
	}
	for _, r := range shape.Refs {
		if err := read(r.Relation, r.Key, fmt.Sprintf("%s(?s, %q, ?d)", r.Relation, r.Key)); err != nil {
			return nil, err
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, c := out[i], out[j]
		return a.Relation+"\x00"+a.Key+"\x00"+a.From+"\x00"+a.To < c.Relation+"\x00"+c.Key+"\x00"+c.From+"\x00"+c.To
	})
	return out, nil
}

// An End is one end of a path: a kind of node, and literals over ?x narrowing which nodes of it
// (str.glob(?x, "github.com/panyam/goapplib/*"), or ?x = "an id"), "" for every node of the kind.
type End struct {
	Kind, Where string
}

// PathOptions bound a search. Zero values take the defaults.
type PathOptions struct {
	MaxHops  int // links in a path, 3 when 0
	MaxPaths int // paths run, shortest first, 20 when 0
	Rows     int // rows shown per path, 5 when 0
	// Depth caps a closure's walk (graph.closure's max), 6 when 0.
	Depth int
	Eval  []datalog.Option // each path runs with these, such as a Budget
}

// A Path is a join from one End to the other that answered: the program, how many hops it takes,
// and the first rows it gave (the two ends' ids).
type Path struct {
	Program string
	Hops    int
	Rows    [][2]string
}

// Paths finds the joins from one End to the other over links (KindGraph's), runs each, and returns
// those that answered, shortest first (#514 piece 5). A path whose first rows are a shorter one's
// is left out: a plain hop and its closure often answer alike. A small model picks among them rather than
// writing the join. A path's Program asks no limit, so a model answering with it gets every row;
// only the rows shown are capped at Rows. A link can be walked either way, and a link from a kind to itself can be walked
// as a closure (graph.closure, up to Depth steps), so calls* reaches a test's callee's callee. Each
// node on a path is held to its kind, and a path visits a kind once, apart from its closures. err
// is the first path's failure, a *PathError, when none answered and one failed.
func Paths(ctx context.Context, b *datalog.Base, shape Shape, links []Link, from, to End, o PathOptions) ([]Path, error) {
	if o.MaxHops <= 0 {
		o.MaxHops = 3
	}
	if o.MaxPaths <= 0 {
		o.MaxPaths = 20
	}
	if o.Rows <= 0 {
		o.Rows = 5
	}
	if o.Depth <= 0 {
		o.Depth = 6
	}
	var found []Path
	var firstErr error
	answered := map[string]bool{} // the rows each kept path gave, so a longer one giving the same goes
	for _, steps := range kindPaths(links, from.Kind, to.Kind, o.MaxHops, o.MaxPaths) {
		prog := pathProgram(shape, steps, from, to, o.Depth)
		rows, err := evalText(ctx, b, prog, o.Rows, o.Eval)
		if err != nil {
			if firstErr == nil {
				firstErr = &PathError{Program: prog, Err: err}
			}
			continue
		}
		if len(rows) == 0 {
			continue
		}
		p := Path{Program: prog, Hops: len(steps)}
		var key []string
		for _, r := range rows {
			p.Rows = append(p.Rows, [2]string{r.Bind["from"].S, r.Bind["to"].S})
			key = append(key, r.Bind["from"].S+"\x00"+r.Bind["to"].S)
		}
		sort.Strings(key)
		if k := strings.Join(key, "\x01"); !answered[k] {
			answered[k] = true
			found = append(found, p)
		}
	}
	if len(found) == 0 {
		return nil, firstErr
	}
	return found, nil
}

// A PathError is a join that failed to parse, check or run. Its Program is the text jaala wrote
// for it, which the error's position (datalog.Locate) points into; the ends the model sent hold no
// such place.
type PathError struct {
	Program string
	Err     error
}

func (e *PathError) Error() string { return e.Err.Error() }

// Unwrap is the join's own error, so errors.As finds a *datalog.CheckError through it.
func (e *PathError) Unwrap() error { return e.Err }

// A step walks one link, forward or back, once or as a closure.
type step struct {
	link             Link
	back, closure    bool
	fromKind, toKind string
}

// kindPaths are the sequences of steps from kind from to kind to, breadth first, at most max links
// long and at most limit of them. A path doesn't revisit a kind, apart from a closure on one and
// arriving back at to, where it ends: x05's two ends are both functions.
func kindPaths(links []Link, from, to string, max, limit int) [][]step {
	var out [][]step
	type state struct {
		at    string
		steps []step
		seen  map[string]bool
	}
	queue := []state{{at: from, seen: map[string]bool{from: true}}}
	for len(queue) > 0 && len(out) < limit {
		s := queue[0]
		queue = queue[1:]
		if len(s.steps) == max {
			continue
		}
		for _, l := range links {
			var next []step
			switch {
			case l.From == s.at && l.To == s.at:
				// one step either way before the closures, so a direct edge is the shorter answer
				next = append(next, step{link: l, fromKind: s.at, toKind: s.at}, step{link: l, back: true, fromKind: s.at, toKind: s.at},
					step{link: l, closure: true, fromKind: s.at, toKind: s.at},
					step{link: l, closure: true, back: true, fromKind: s.at, toKind: s.at})
			case l.From == s.at:
				next = append(next, step{link: l, fromKind: s.at, toKind: l.To})
			case l.To == s.at:
				next = append(next, step{link: l, back: true, fromKind: s.at, toKind: l.From})
			}
			for _, st := range next {
				revisit := st.toKind != st.fromKind && s.seen[st.toKind]
				if revisit && st.toKind != to {
					continue
				}
				if st.fromKind == st.toKind && len(s.steps) > 0 && s.steps[len(s.steps)-1].link == st.link {
					continue // a link walked twice in a row on one kind is what its closure is for
				}
				steps := append(append([]step{}, s.steps...), st)
				if st.toKind == to {
					out = append(out, steps)
					if len(out) == limit {
						return out
					}
				}
				if revisit {
					continue // the ends may share a kind; a path ends on reaching it again
				}
				seen := map[string]bool{st.toKind: true}
				for k := range s.seen {
					seen[k] = true
				}
				queue = append(queue, state{at: st.toKind, steps: steps, seen: seen})
			}
		}
	}
	return out
}

var endVar = regexp.MustCompile(`\?x\b`)

// pathProgram writes steps as a program from ?from to ?to, each node held to its kind, both ends
// narrowed by their Where, answering the distinct pairs of ends. It writes no limit: the caller caps
// the rows it reads, and a model keeps whatever the program says (#530).
func pathProgram(shape Shape, steps []step, from, to End, depth int) string {
	vars := make([]string, len(steps)+1)
	vars[0], vars[len(steps)] = "?from", "?to"
	for i := 1; i < len(steps); i++ {
		vars[i] = fmt.Sprintf("?n%d", i)
	}
	lits := []string{fmt.Sprintf("%s(?from, %q)", shape.Kinds, from.Kind)}
	if w := strings.TrimSpace(from.Where); w != "" {
		lits = append(lits, endVar.ReplaceAllString(w, "?from"))
	}
	for i, st := range steps {
		u, w := vars[i], vars[i+1]
		if st.back {
			u, w = w, u
		}
		switch {
		case st.closure:
			lits = append(lits, fmt.Sprintf("graph.closure(%q, %s, %s, 1, %d, _)", st.link.Relation, u, w, depth))
		case st.link.Key != "":
			lits = append(lits, fmt.Sprintf("%s(%s, %q, %s)", st.link.Relation, u, st.link.Key, w))
		default:
			lits = append(lits, fmt.Sprintf("%s(%s, %s)", st.link.Relation, u, w))
		}
		lits = append(lits, fmt.Sprintf("%s(%s, %q)", shape.Kinds, vars[i+1], st.toKind))
	}
	if w := strings.TrimSpace(to.Where); w != "" {
		lits = append(lits, endVar.ReplaceAllString(w, "?to"))
	}
	return strings.Join(lits, ", ") + " => ?from, ?to"
}

// evalText parses, checks and runs a program over b, with limit rows at most when it's above 0.
func evalText(ctx context.Context, b *datalog.Base, text string, limit int, eval []datalog.Option) ([]datalog.Row, error) {
	q, err := datalog.Parse(text)
	if err != nil {
		return nil, err
	}
	if limit > 0 && (q.Limit == 0 || q.Limit > limit) {
		q.Limit = limit
	}
	if err := datalog.Validate(q, b.Vocabulary()); err != nil {
		return nil, err
	}
	return datalog.SemiNaive{}.Eval(ctx, q, b, eval...)
}

// An EndsProblem says why ParseEnds refused a paths argument.
type EndsProblem int

const (
	EndsNoArrow EndsProblem = iota + 1 // no " => " between the two ends
	EndsNoKind                         // an end is blank, so it names no kind
	EndsUnbound                        // an end's literals never mention ?x
)

// An EndsError is ParseEnds' refusal (#532). Its Error is jaala's own wording; a Session writes the
// pack's instead (Prompts.PathsUsage, PathsNoKind, PathsUnbound), so a host can reword what a model
// reads. Arg is the whole argument and Kind the end's kind, for EndsUnbound.
type EndsError struct {
	Problem   EndsProblem
	Arg, Kind string
}

func (e *EndsError) Error() string {
	switch e.Problem {
	case EndsNoKind:
		return fmt.Sprintf("paths: an end needs a kind first, in %q", e.Arg)
	case EndsUnbound:
		return fmt.Sprintf("paths: the literals after %s never mention ?x, the end's node, so nothing ties them to it; write them over ?x, as %s ?x = \"an id\"", e.Kind, e.Kind)
	}
	return pathsUsage
}

const pathsUsage = "paths takes FROM => TO, each a kind and literals over ?x, as func str.glob(?x, \"a/*\") => func ?x = \"b.F\""

// prompt is the refusal as the pack words it.
func (e *EndsError) prompt(pr Prompts) string {
	switch e.Problem {
	case EndsNoKind:
		return fmt.Sprintf(pr.PathsNoKind, strconv.Quote(e.Arg))
	case EndsUnbound:
		return fmt.Sprintf(pr.PathsUnbound, e.Kind)
	}
	return pr.PathsUsage
}

// ParseEnds reads a paths action's argument, FROM => TO, each end a kind and then literals over ?x:
// func str.glob(?x, "github.com/a/*") => func ?x = "github.com/b.F". An end with literals that
// never mention ?x is refused: nothing would tie them to the end, and the join would run them
// unbound (#530). Every refusal is an *EndsError.
func ParseEnds(arg string) (from, to End, err error) {
	a, b, ok := strings.Cut(arg, " => ")
	if !ok {
		return End{}, End{}, &EndsError{Problem: EndsNoArrow, Arg: arg}
	}
	end := func(s string) (End, error) {
		kind, where, _ := strings.Cut(strings.TrimSpace(s), " ")
		if kind == "" {
			return End{}, &EndsError{Problem: EndsNoKind, Arg: arg}
		}
		where = strings.TrimSpace(where)
		if where != "" && !endVar.MatchString(where) {
			return End{}, &EndsError{Problem: EndsUnbound, Arg: arg, Kind: kind}
		}
		return End{Kind: kind, Where: where}, nil
	}
	if from, err = end(a); err != nil {
		return End{}, End{}, err
	}
	to, err = end(b)
	return from, to, err
}

// paths runs a paths action: the ends from arg, the pack's Links (or KindGraph's, read once per
// Session), and the joins that answered, each with its first rows, as the model reads them. The
// text values in the rows are kept, as an action's are.
func (s *Session) paths(ctx context.Context, arg string) string {
	if !s.o.offersPaths() {
		return "paths needs the pack's Shape and data to run on"
	}
	from, to, err := ParseEnds(arg)
	var ee *EndsError
	if errors.As(err, &ee) {
		return ee.prompt(s.pr)
	}
	if err != nil {
		return err.Error()
	}
	if !s.linksRead {
		s.linksRead = true
		s.links = s.o.Pack.Links
		if s.links == nil {
			s.links, s.linksErr = KindGraph(ctx, s.o.Base, *s.o.Pack.Shape, s.o.Eval...)
		}
	}
	if s.linksErr != nil {
		return s.linksErr.Error()
	}
	o := PathOptions{Eval: s.o.Eval}
	found, err := Paths(ctx, s.o.Base, *s.o.Pack.Shape, s.links, from, to, o)
	a, b, _ := strings.Cut(arg, " => ")
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	if len(found) == 0 {
		var pe *PathError
		if errors.As(err, &pe) {
			return strings.TrimRight(Explain(pe.Program, pe.Err), "\n")
		}
		if err != nil {
			return err.Error()
		}
		return fmt.Sprintf(s.pr.PathsNone, a, b, "3")
	}
	var out strings.Builder
	fmt.Fprintf(&out, s.pr.PathsFound, a, b)
	var seen []string
	for i, p := range found {
		fmt.Fprintf(&out, "\n\n%d. %s", i+1, p.Program)
		for _, r := range p.Rows {
			fmt.Fprintf(&out, "\n  %q, %q", r[0], r[1])
			seen = append(seen, r[0], r[1])
		}
	}
	s.mem.see(seen)
	return out.String()
}
