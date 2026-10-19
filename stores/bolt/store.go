package bolt

import (
	"context"
	"fmt"
	"iter"
	"slices"
	"strings"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"

	"github.com/panyam/jaala/frontends/cypher"
	"github.com/panyam/jaala/ns"
)

// nodeLabel is the label every node the store writes carries, beside its relations' labels, so a
// node is found by its key whichever relation named it, and one index (on its id) serves them all.
const nodeLabel = "JaalaNode"

// Options configure a Store.
type Options struct {
	// Database is the database sessions open, "" for the server's default.
	Database string
	// LookupsOnly turns pushdown off: the store answers lookups, batches and whole reads, and refuses
	// every conjunction and summary, so the engine joins, counts and pages itself. Answers are the
	// same either way; it is for comparing what a question ships, and for a server misbehaving.
	LookupsOnly bool
}

// A Store is an ns.Source over a graph server that speaks Bolt (Neo4j, Memgraph), laid out by a
// cypher.Graph: a label's relation is the nodes holding that label, a type's relation the
// relationships of that type, and the graph's Props relation the nodes' other properties. It reads
// a relation whole as a stream (ns.StreamSource), looks a call up by its bound values (ns.LookupSource,
// ns.BatchLookupSource), says which of those an index answers (ns.IndexedLookups), evaluates a join,
// a count and a page on the server (ns.PushSource, ns.SummarySource), walks a type's relation from
// its starts in one query (ns.WalkSource), and writes a change set (Apply). See the package doc for how facts are laid out.
//
// A Store is safe for concurrent use, as its driver is.
type Store struct {
	driver      neo4j.DriverWithContext
	db          string
	lookupsOnly bool
	server      string
	rels        map[string]*relation
	order       []string
	labelled    []string // every property a label's relation keeps, which the Props relation never reads
}

// A relation is one vocabulary relation and where its facts live: the labels or types holding it,
// or the graph's Props.
type relation struct {
	name   string
	schema ns.Schema
	nodes  []cypher.Node
	edges  []cypher.Edge
	props  *cypher.PropRelation
	// labelled is every property a label's relation keeps, for the Props relation to skip.
	labelled []string
}

func (r *relation) labelledProps() []string { return r.labelled }

// New returns a Store over driver for the relations g lays out, each with its schema from schemas.
// It refuses a relation g names that schemas doesn't hold, an argument g leaves unplaced, a node key
// or endpoint typed as a number (a node is found by its key's text), and a property name the store
// reserves (`id`, or one starting with `_`). It creates the index on every node's id if the server
// lacks it. g's Derived properties are the host's to compute and aren't served.
func New(ctx context.Context, driver neo4j.DriverWithContext, g cypher.Graph, schemas map[string]ns.Schema, opts Options) (*Store, error) {
	s := &Store{driver: driver, db: opts.Database, lookupsOnly: opts.LookupsOnly, rels: map[string]*relation{}}
	rel := func(name string) (*relation, error) {
		if r, ok := s.rels[name]; ok {
			return r, nil
		}
		sc, ok := schemas[name]
		if !ok {
			return nil, fmt.Errorf("bolt: the graph lays out %q, which has no schema", name)
		}
		r := &relation{name: name, schema: sc}
		s.rels[name] = r
		s.order = append(s.order, name)
		return r, nil
	}
	for _, n := range g.Nodes {
		r, err := rel(n.Relation)
		if err != nil {
			return nil, err
		}
		if len(r.edges) > 0 || r.props != nil {
			return nil, fmt.Errorf("bolt: %s holds label %s and something else too", n.Relation, n.Label)
		}
		if err := r.placed("label "+n.Label, n.Fixed, n.Props, n.Key); err != nil {
			return nil, err
		}
		for p, i := range n.Props {
			if i != n.Key {
				if err := reserved(p); err != nil {
					return nil, err
				}
				s.labelled = append(s.labelled, p)
			}
		}
		r.nodes = append(r.nodes, n)
	}
	for _, e := range g.Edges {
		r, err := rel(e.Relation)
		if err != nil {
			return nil, err
		}
		if len(r.nodes) > 0 || r.props != nil {
			return nil, fmt.Errorf("bolt: %s holds type %s and something else too", e.Relation, e.Type)
		}
		if err := r.placed("type "+e.Type, e.Fixed, e.Props, e.From, e.To); err != nil {
			return nil, err
		}
		for p := range e.Props {
			if err := reserved(p); err != nil {
				return nil, err
			}
		}
		r.edges = append(r.edges, e)
	}
	if p := g.Props; p != nil {
		r, err := rel(p.Relation)
		if err != nil {
			return nil, err
		}
		if len(r.nodes) > 0 || len(r.edges) > 0 {
			return nil, fmt.Errorf("bolt: %s holds the properties and something else too", p.Relation)
		}
		if err := r.placed("properties", nil, nil, p.Node, p.Key, p.Value); err != nil {
			return nil, err
		}
		r.props = p
	}
	slices.Sort(s.labelled)
	s.labelled = slices.Compact(s.labelled)
	if g.Props != nil {
		s.rels[g.Props.Relation].labelled = s.labelled
	}
	info, err := driver.GetServerInfo(ctx)
	if err != nil {
		return nil, fmt.Errorf("bolt: %w", err)
	}
	s.server = info.Agent()
	if err := s.index(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

// placed checks that every argument of r is a key, an endpoint, a fixed constant or a property, and
// that keys and endpoints aren't numbers.
func (r *relation) placed(what string, fixed map[int]string, props map[string]int, keys ...int) error {
	at := make([]bool, r.schema.Arity)
	mark := func(i int) error {
		if i < 0 || i >= r.schema.Arity {
			return fmt.Errorf("bolt: %s reads %s's argument %d, which has %d", what, r.name, i, r.schema.Arity)
		}
		at[i] = true
		return nil
	}
	for _, k := range keys {
		if err := mark(k); err != nil {
			return err
		}
		if r.numeric(k) {
			return fmt.Errorf("bolt: %s's key or endpoint, %s's argument %d, is a number; a node is found by its key's text", what, r.name, k)
		}
	}
	for i := range fixed {
		if err := mark(i); err != nil {
			return err
		}
	}
	for _, i := range props {
		if err := mark(i); err != nil {
			return err
		}
	}
	for i, ok := range at {
		if !ok {
			return fmt.Errorf("bolt: %s leaves %s's argument %d with nowhere to be kept", what, r.name, i)
		}
	}
	return nil
}

func reserved(p string) error {
	if p == "id" || strings.HasPrefix(p, "_") || p == "" {
		return fmt.Errorf("bolt: property %q is the store's own; name it otherwise", p)
	}
	return nil
}

func (r *relation) argType(i int) ns.ArgType {
	if i < len(r.schema.Types) {
		return r.schema.Types[i]
	}
	return ns.ArgType{}
}

func (r *relation) numeric(i int) bool { return r.argType(i).Type == ns.TypeNumber }

// textual is whether argument i is read as text: a string, or an entity.
func (r *relation) textual(i int) bool {
	t := r.argType(i)
	return t.Type == ns.TypeString || t.Kind != "" || t.KindFrom != ""
}

func (s *Store) index(ctx context.Context) error {
	q := "CREATE INDEX jaala_node_id IF NOT EXISTS FOR (n:" + nodeLabel + ") ON (n.id)"
	if s.memgraph() {
		q = "CREATE INDEX ON :" + nodeLabel + "(id)"
	}
	// An auto-commit run, since Memgraph refuses an index inside an explicit transaction.
	sess := s.driver.NewSession(ctx, neo4j.SessionConfig{DatabaseName: s.db, AccessMode: neo4j.AccessModeWrite})
	defer sess.Close(ctx)
	res, err := sess.Run(ctx, q, nil)
	if err == nil {
		_, err = res.Consume(ctx)
	}
	if err != nil {
		return fmt.Errorf("bolt: creating the node index: %w", err)
	}
	return nil
}

// memgraph is whether the server is Memgraph, which names itself "Neo4j/v5.11.0 compatible graph
// database server - Memgraph" and writes an index without a name or IF NOT EXISTS.
func (s *Store) memgraph() bool { return strings.Contains(strings.ToLower(s.server), "memgraph") }

// Schema is the schema New was given for rel, when the graph lays it out.
func (s *Store) Schema(rel string) (ns.Schema, bool) {
	r, ok := s.rels[rel]
	if !ok {
		return ns.Schema{}, false
	}
	return r.schema, true
}

// Relations lists the relations the graph lays out, labels first, in the graph's order.
func (s *Store) Relations() []string { return slices.Clone(s.order) }

// Tuples reads rel whole. A failed read reads as no facts; the engine reads through TuplesSeq,
// which reports it.
func (s *Store) Tuples(rel string) []ns.Tuple {
	ts, _ := s.TuplesContext(context.Background(), rel)
	return ts
}

// TuplesContext reads rel whole under ctx.
func (s *Store) TuplesContext(ctx context.Context, rel string) ([]ns.Tuple, error) {
	var out []ns.Tuple
	for t, err := range s.TuplesSeq(ctx, rel) {
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

// TuplesSeq streams rel's facts as the server hands them over, one query per label or type, and
// closes the result when the engine stops reading.
func (s *Store) TuplesSeq(ctx context.Context, rel string) iter.Seq2[ns.Tuple, error] {
	return func(yield func(ns.Tuple, error) bool) {
		r, ok := s.rels[rel]
		if !ok {
			return
		}
		for _, m := range r.matches() {
			q, params, build := m.query(r, nil, false)
			if !s.stream(ctx, q, params, func(rec *neo4j.Record) bool {
				t, err := build(rec)
				return yield(t, err) && err == nil
			}, func(err error) { yield(ns.Tuple{}, err) }) {
				return
			}
		}
	}
}

// stream runs q and hands each record to each until it returns false, reporting a failure to fail.
// It returns whether the caller should go on.
func (s *Store) stream(ctx context.Context, q string, params map[string]any, each func(*neo4j.Record) bool, fail func(error)) bool {
	sess := s.driver.NewSession(ctx, neo4j.SessionConfig{DatabaseName: s.db, AccessMode: neo4j.AccessModeRead})
	defer sess.Close(ctx)
	res, err := sess.Run(ctx, q, params)
	if err != nil {
		fail(fmt.Errorf("bolt: %w", err))
		return false
	}
	for res.Next(ctx) {
		if !each(res.Record()) {
			res.Consume(ctx)
			return false
		}
	}
	if err := res.Err(); err != nil {
		fail(fmt.Errorf("bolt: %w", err))
		return false
	}
	return true
}

// Lookup answers rel's facts matching bound. A bound key, endpoint, property name or text property
// is asked of the server; a bound number isn't, since the engine reads a number as its argument's
// value and unit (folded, when the vocabulary folds), so the server returns more and the engine
// keeps what matches.
func (s *Store) Lookup(ctx context.Context, rel string, bound map[int]ns.Value) ([]ns.Tuple, error) {
	positions := make([]int, 0, len(bound))
	for i := range bound {
		positions = append(positions, i)
	}
	slices.Sort(positions)
	vals := make([]ns.Value, len(positions))
	for j, i := range positions {
		vals[j] = bound[i]
	}
	res, err := s.LookupBatch(ctx, rel, positions, [][]ns.Value{vals})
	if err != nil {
		return nil, err
	}
	return res[0], nil
}

// LookupBatch answers one lookup per entry of values, at positions, in one query per label or type:
// the values go to the server as a list it unwinds, and each row comes back with the index of the
// value that matched it.
func (s *Store) LookupBatch(ctx context.Context, rel string, positions []int, values [][]ns.Value) ([][]ns.Tuple, error) {
	out := make([][]ns.Tuple, len(values))
	r, ok := s.rels[rel]
	if !ok {
		return out, nil
	}
	for _, m := range r.matches() {
		asked := []int{} // the values this label or type can match, by their index
		for k, vs := range values {
			if m.admits(positions, vs) {
				asked = append(asked, k)
			}
		}
		if len(asked) == 0 {
			continue
		}
		filters := m.filters(r, positions)
		if len(filters) == 0 {
			q, params, build := m.query(r, nil, false)
			// Nothing the server can filter on: read it once and hand every value the lot, which
			// the engine then filters.
			var all []ns.Tuple
			var ferr error
			s.stream(ctx, q, params, func(rec *neo4j.Record) bool {
				t, err := build(rec)
				ferr = err
				all = append(all, t)
				return err == nil
			}, func(err error) { ferr = err })
			if ferr != nil {
				return nil, ferr
			}
			for _, k := range asked {
				out[k] = append(out[k], all...)
			}
			continue
		}
		q, params, build := m.query(r, filters, true)
		rows := make([]map[string]any, 0, len(asked))
		for _, k := range asked {
			row := map[string]any{"i": int64(k)}
			for j, p := range positions {
				if f, ok := filters[p]; ok {
					if v := values[k][j]; !v.Absent {
						row[f.param] = v.S
					} else {
						row[f.param] = nil
					}
				}
			}
			rows = append(rows, row)
		}
		params["vals"] = rows
		var ferr error
		s.stream(ctx, q, params, func(rec *neo4j.Record) bool {
			t, err := build(rec)
			if err != nil {
				ferr = err
				return false
			}
			i, _ := rec.Get("i")
			k := int(i.(int64))
			out[k] = append(out[k], t)
			return true
		}, func(err error) { ferr = err })
		if ferr != nil {
			return nil, ferr
		}
	}
	return out, nil
}

// LooksUp is true for every relation the store holds: each is asked per call, never read whole for
// a call with a value bound.
func (s *Store) LooksUp(rel string) bool { _, ok := s.rels[rel]; return ok }

// Indexed is whether a lookup of rel binding bound reads the node index: it binds a node's key, a
// relationship's endpoint, or the node a property is read from. Anything else scans a label or
// type.
func (s *Store) Indexed(rel string, bound []int) bool {
	r, ok := s.rels[rel]
	if !ok {
		return false
	}
	for _, m := range r.matches() {
		indexed := false
		for _, p := range bound {
			if f, ok := m.filters(r, []int{p})[p]; ok && f.indexed {
				indexed = true
			}
		}
		if !indexed {
			return false
		}
	}
	return true
}
