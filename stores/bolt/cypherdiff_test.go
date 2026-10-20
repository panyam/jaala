package bolt

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"github.com/neo4j/neo4j-go-driver/v5/neo4j/dbtype"

	"github.com/panyam/jaala/datalog"
	"github.com/panyam/jaala/frontends/cypher"
	"github.com/panyam/jaala/ns"
)

// diffGraph is a seed's graph for the differential (#413), built so none of the compiler's
// documented differences from a Cypher engine can arise: no self-loop, since within one MATCH Cypher
// uses a relationship once and a self-loop could stand for two hops; one relationship at most from one
// node to another, so counts of bindings are Cypher's rows; and every node holding every property, so
// a Props read never drops a row Cypher would answer null. Cycles are fine: the generator's
// variable-length matches start at one step and never feed an aggregate, and over the set of ends a
// walk of k steps reaches nothing a trail doesn't.
func diffGraph(seed int64) (cypher.Graph, *ns.MemSource) {
	rnd := rand.New(rand.NewSource(seed))
	src := ns.NewMemSource().
		DeclareSchema("node", ns.Schema{Arity: 2, Labels: []string{"id", "kind"}, Types: []ns.ArgType{{Kind: "node"}, {Type: ns.TypeString}}}).
		DeclareSchema("edge", ns.Schema{Arity: 3, Labels: []string{"from", "kind", "to"}, Types: []ns.ArgType{{Kind: "node"}, {Type: ns.TypeString}, {Kind: "node"}}}).
		DeclareSchema("attr", ns.Schema{Arity: 3, Labels: []string{"id", "key", "value"}, Types: []ns.ArgType{{Kind: "node"}, {Type: ns.TypeString}}})
	n := 3 + rnd.Intn(6)
	kinds, types := []string{"Function", "Package"}, []string{"calls", "imports"}
	id := func(i int) ns.Value { return ns.S(fmt.Sprintf("n%d", i)) }
	for i := range n {
		src.Add("node", ns.Tuple{Vals: []ns.Value{id(i), ns.S(kinds[rnd.Intn(2)])}})
		src.Add("attr", ns.Tuple{Vals: []ns.Value{id(i), ns.S("name"), ns.S(fmt.Sprintf("name%d", rnd.Intn(4)))}})
		src.Add("attr", ns.Tuple{Vals: []ns.Value{id(i), ns.S("size"), ns.N(float64(rnd.Intn(5)))}})
	}
	for i := range n {
		for j := range n {
			if j == i {
				continue
			}
			if rnd.Float64() < 0.35 {
				src.Add("edge", ns.Tuple{Vals: []ns.Value{id(i), ns.S(types[rnd.Intn(2)]), id(j)}})
			}
		}
	}
	return cypher.Store{Node: "node", Edge: "edge", Attr: "attr"}.Graph(kinds, types), src
}

// native writes a value Neo4j answered as jaala would answer it: a node as its key, a number by its
// value, null as absent, a list sorted, so a row compares as text.
func native(v any) (string, bool) {
	switch x := v.(type) {
	case nil:
		return "absent", true
	case string:
		return "s:" + x, true
	case int64:
		return "n:" + ftoa(float64(x)), true
	case float64:
		return "n:" + ftoa(x), true
	case bool:
		return "s:" + fmt.Sprint(x), true
	case dbtype.Node:
		id, _ := x.Props["id"].(string)
		return "s:" + id, true
	case []any:
		var out []string
		for _, e := range x {
			s, ok := native(e)
			if !ok {
				return "", false
			}
			out = append(out, s)
		}
		sort.Strings(out)
		return "[" + strings.Join(out, ",") + "]", true
	}
	return "", false // a relationship, a path, a map: shapes the compiler refuses or answers otherwise
}

func jaalaValue(v ns.Value) string {
	switch {
	case v.Absent:
		return "absent"
	case v.Num != nil:
		return "n:" + ftoa(*v.Num)
	case strings.HasPrefix(v.S, "[") && strings.HasSuffix(v.S, "]"):
		var items []any
		if json.Unmarshal([]byte(v.S), &items) == nil {
			w, _ := native(items)
			return w
		}
	}
	return "s:" + v.S
}

// cypherGen writes Cypher over diffGraph's vocabulary that means something: nodes of its labels,
// relationships of its types, its two properties, and the clauses the compiler takes. It stays off
// the documented differences the graph doesn't already rule out: a variable-length match never feeds
// an aggregate (Cypher counts paths, jaala the distinct bindings, and a DAG has several paths), and a
// LIMIT always comes with an ORDER BY over every column, so the rows it keeps are one set.
type cypherGen struct{ rnd *rand.Rand }

func (g cypherGen) pick(xs ...string) string { return xs[g.rnd.Intn(len(xs))] }
func (g cypherGen) chance(p float64) bool    { return g.rnd.Float64() < p }

func (g cypherGen) label() string {
	if g.chance(0.5) {
		return ""
	}
	return ":" + g.pick("Function", "Package")
}

func (g cypherGen) rel() string { return ":" + g.pick("CALLS", "IMPORTS") }

func (g cypherGen) cond(vars []string) string {
	v := g.pick(vars...)
	switch g.rnd.Intn(5) {
	case 0:
		return fmt.Sprintf("%s.size > %d", v, g.rnd.Intn(4))
	case 1:
		return fmt.Sprintf("%s.name = 'name%d'", v, g.rnd.Intn(4))
	case 2:
		return fmt.Sprintf("%s.size <> %s.size", v, g.pick(vars...))
	case 3:
		return fmt.Sprintf("%s.name STARTS WITH 'name'", v)
	}
	return fmt.Sprintf("NOT %s.size = %d", v, g.rnd.Intn(4))
}

// query writes one query: a MATCH, sometimes an OPTIONAL MATCH from one of its nodes, a WHERE, and
// a RETURN, or a WITH stage before it, or a UNION of two.
func (g cypherGen) query() string {
	if g.chance(0.1) {
		return g.simple("x") + " UNION " + g.simple("x")
	}
	var vars []string
	var b strings.Builder
	varLength := false
	switch g.rnd.Intn(4) {
	case 0:
		fmt.Fprintf(&b, "MATCH (a%s)", g.label())
		vars = []string{"a"}
	case 1:
		fmt.Fprintf(&b, "MATCH (a%s)-[%s]->(b%s)", g.label(), g.rel(), g.label())
		vars = []string{"a", "b"}
	case 2:
		fmt.Fprintf(&b, "MATCH (a%s)-[%s]->(b)-[%s]->(c%s)", g.label(), g.rel(), g.rel(), g.label())
		vars = []string{"a", "b", "c"}
	default:
		fmt.Fprintf(&b, "MATCH (a%s)-[%s*1..%d]->(b%s)", g.label(), g.rel(), 1+g.rnd.Intn(3), g.label())
		vars = []string{"a", "b"}
		varLength = true
	}
	optional := ""
	if g.chance(0.25) {
		optional = "o"
		fmt.Fprintf(&b, " OPTIONAL MATCH (%s)-[%s]->(o)", g.pick(vars...), g.rel())
	}
	if g.chance(0.5) {
		c := g.cond(vars)
		if g.chance(0.3) {
			c += g.pick(" AND ", " OR ") + g.cond(vars)
		}
		fmt.Fprintf(&b, " WHERE %s", c)
	}
	if !varLength && g.chance(0.15) {
		v := g.pick(vars...)
		fmt.Fprintf(&b, " WITH %s, count(*) AS k WHERE k > %d RETURN %s.name AS n, k", v, g.rnd.Intn(2), v)
		return b.String()
	}
	// A page only over distinct rows of properties, never a node (Cypher orders a node by its internal
	// id, jaala by its key) and never a value an OPTIONAL MATCH may leave null (Cypher puts null last,
	// jaala absent first), so the rows a LIMIT keeps are one set on both sides.
	paged := optional == "" && g.chance(0.3)
	cols := g.columns(vars, optional, !varLength && !paged, paged)
	distinct := ""
	if paged || g.chance(0.3) {
		distinct = "DISTINCT "
	}
	fmt.Fprintf(&b, " RETURN %s%s", distinct, strings.Join(cols, ", "))
	if paged {
		var by []string
		for i := range cols {
			by = append(by, fmt.Sprintf("c%d", i)+g.pick("", " DESC"))
		}
		fmt.Fprintf(&b, " ORDER BY %s LIMIT %d", strings.Join(by, ", "), 1+g.rnd.Intn(4))
	}
	return b.String()
}

// columns are a RETURN's columns, each aliased c0, c1, ..: nodes, properties, and, when aggregates
// mean the same on both sides, a count, a sum, a min, a max or a collect.
func (g cypherGen) columns(vars []string, optional string, aggregate, propsOnly bool) []string {
	all := vars
	if optional != "" {
		all = append(slices.Clone(vars), optional)
	}
	var cols []string
	for range 1 + g.rnd.Intn(2) {
		v := g.pick(all...)
		if propsOnly {
			cols = append(cols, g.pick(v+".name", v+".size"))
		} else {
			cols = append(cols, g.pick(v, v+".name", v+".size"))
		}
	}
	if aggregate && g.chance(0.4) {
		v := g.pick(all...)
		cols = append(cols, g.pick("count(*)", "count("+v+")", "sum("+v+".size)", "min("+v+".name)", "max("+v+".size)", "collect("+v+".name)"))
	}
	for i := range cols {
		cols[i] += fmt.Sprintf(" AS c%d", i)
	}
	return cols
}

func (g cypherGen) simple(v string) string {
	return fmt.Sprintf("MATCH (%s%s) WHERE %s RETURN %s.name AS c0", v, g.label(), g.cond([]string{v}), v)
}

type diffOutcome int

const (
	diffAgreed        diffOutcome = iota
	diffRefused                   // the compiler refused it, by a gap or as a type error
	diffServerRefused             // Neo4j refused it
	diffShape                     // Neo4j answered a value with no jaala counterpart
	diffDisagreed
)

// knownCypherDisagreements are disagreements understood and not yet fixed, matched by the query or
// by jaala's error, so the differential counts them rather than failing; issue 0 is a difference
// frontends/cypher documents. Each carries a repro that TestKnownCypherDisagreementsStillDisagree runs,
// so a pattern that matches nothing, or a fixed bug, shows at once.
var knownCypherDisagreements = []struct {
	issue int
	why   string
	match *regexp.Regexp
	repro string
	seed  int64 // the graph the repro disagrees over
}{
	{438, "a closure with neither end bound over a derived step", regexp.MustCompile(`unknown relation "\\x00w:cypher_step`),
		"MATCH (a)-[:CALLS*1..3]->(b) RETURN b AS c0", 1},
	{439, "an OR's rule drops the match's other variables, so an aggregate loses bindings", regexp.MustCompile(`WHERE .* OR .*(count|collect|sum|avg)\(`),
		"MATCH (a)-[:IMPORTS]->(b)-[:IMPORTS]->(c) WHERE c.name STARTS WITH 'name' OR a.size > 0 RETURN a.name AS c0, count(*) AS c1", 96},
	{0, "min and max reduce numbers only (doc.go)", regexp.MustCompile(`(min|max)\(\w+\.name\)`),
		"MATCH (a) RETURN min(a.name) AS c0", 1},
}

func knownCypher(text, diff string) int {
	for i, k := range knownCypherDisagreements {
		if k.match.MatchString(text) || k.match.MatchString(diff) {
			return i
		}
	}
	return -1
}

// exclusive is the Neo4j server the differential may clear whole: its MATCH reads every node of a
// label, whatever namespace wrote it, so it runs only where JAALA_BOLT_EXCLUSIVE says nothing else
// shares the server (CI's service container, or one scripts/bolt-servers.sh starts with EXCLUSIVE=1).
func exclusive(t *testing.T) server {
	t.Helper()
	if os.Getenv("JAALA_BOLT_EXCLUSIVE") == "" {
		t.Skip("the differential clears the whole server: set JAALA_BOLT_EXCLUSIVE=1 on one nothing else uses")
	}
	for _, s := range servers(t) {
		if s.name == "neo4j" {
			return s
		}
	}
	t.Skip("the differential runs against Neo4j: set JAALA_BOLT_NEO4J")
	return server{}
}

// cypherCase loads facts into the server and asks text of both: jaala through frontends/cypher over
// the facts in memory, Neo4j natively over what the store wrote. It reports the outcome and, on a
// disagreement, both answers.
func cypherCase(t testing.TB, sv server, g cypher.Graph, src *ns.MemSource, text string) (diffOutcome, string) {
	v := vocabulary(t, src)
	p, err := cypher.Compile(v, g, text)
	if err != nil {
		return diffRefused, err.Error()
	}
	if _, err := neo4j.ExecuteQuery(bg, sv.driver, "MATCH (n) DETACH DELETE n", nil, neo4j.EagerResultTransformer); err != nil {
		t.Fatal(err)
	}
	s, err := New(bg, sv.driver, g, schemasOf(src), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Apply(bg, load(src)); err != nil {
		t.Fatal(err)
	}
	res, nerr := neo4j.ExecuteQuery(bg, sv.driver, text, nil, neo4j.EagerResultTransformer)
	if nerr != nil {
		return diffServerRefused, nerr.Error()
	}
	var want []string
	for _, rec := range res.Records {
		var cols []string
		for _, x := range rec.Values {
			w, ok := native(x)
			if !ok {
				return diffShape, fmt.Sprintf("%T", x)
			}
			cols = append(cols, w)
		}
		want = append(want, strings.Join(cols, " | "))
	}
	slices.Sort(want)
	want = slices.Compact(want)
	tab, jerr := datalog.SemiNaive{}.EvalTable(bg, p.Query, base(t, src), datalog.Budget(200_000))
	if jerr != nil {
		return diffDisagreed, fmt.Sprintf("jaala errs %v, neo4j answers %q", jerr, want)
	}
	var got []string
	for i := range tab.Len() {
		var cols []string
		for _, x := range tab.Rows[i] {
			cols = append(cols, jaalaValue(x))
		}
		got = append(got, strings.Join(cols, " | "))
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		return diffDisagreed, fmt.Sprintf("jaala answers %q, neo4j %q", got, want)
	}
	return diffAgreed, ""
}

// shrinkFacts drops an edge, or a node with its properties and edges, while fails still holds, and
// returns the smallest source it reached: whole pieces, so every node still holds every property and
// the graph stays one the documented differences can't show on.
func shrinkFacts(src *ns.MemSource, fails func(*ns.MemSource) bool) *ns.MemSource {
	type fact struct {
		rel string
		t   ns.Tuple
	}
	var all []fact
	for _, rel := range src.Relations() {
		for _, t := range src.Tuples(rel) {
			all = append(all, fact{rel, t})
		}
	}
	build := func(fs []fact) *ns.MemSource {
		out := ns.NewMemSource()
		for _, rel := range src.Relations() {
			sc, _ := src.Schema(rel)
			out.DeclareSchema(rel, sc)
		}
		for _, f := range fs {
			out.Add(f.rel, f.t)
		}
		return out
	}
	without := func(fs []fact, drop func(fact) bool) []fact {
		return slices.DeleteFunc(slices.Clone(fs), drop)
	}
	for {
		var cands [][]fact
		for _, f := range all {
			switch f.rel {
			case "edge":
				cands = append(cands, without(all, func(g fact) bool {
					return g.rel == "edge" && fact(g).t.Vals[0] == f.t.Vals[0] && g.t.Vals[2] == f.t.Vals[2] && g.t.Vals[1] == f.t.Vals[1]
				}))
			case "node":
				id := f.t.Vals[0].S
				cands = append(cands, without(all, func(g fact) bool {
					return g.t.Vals[0].S == id || g.rel == "edge" && g.t.Vals[2].S == id
				}))
			}
		}
		smaller := false
		for _, c := range cands {
			if fails(build(c)) {
				all, smaller = c, true
				break
			}
		}
		if !smaller {
			return build(all)
		}
	}
}

// Neo4j answers every query jaala compiles from Cypher as jaala does (#413), over seeded graphs built
// so the compiler's documented differences can't arise, and queries that mean something over them.
// A disagreement is shrunk to the fewest facts that still show it. JAALA_BOLT_CYPHER_SEEDS sets the
// seeds (CI's bolt job runs 100 under -race, selfcheck 1000), JAALA_GEN_SEED=n replays one. The control is that
// most queries were compared, rather than refused by either side.
func TestCypherAgreesWithNeo4j(t *testing.T) {
	sv := exclusive(t)
	first, seeds := int64(1), envInt("JAALA_BOLT_CYPHER_SEEDS", 40)
	if s := envInt("JAALA_GEN_SEED", 0); s > 0 {
		first, seeds = int64(s), 1
	}
	counts := map[diffOutcome]int{}
	known := map[int]int{}
	failed := 0
	for seed := first; seed < first+int64(seeds); seed++ {
		g, src := diffGraph(seed)
		gen := cypherGen{rand.New(rand.NewSource(seed))}
		for range 20 {
			text := gen.query()
			out, diff := cypherCase(t, sv, g, src, text)
			if out == diffDisagreed {
				if k := knownCypher(text, diff); k >= 0 {
					known[k]++
					continue
				}
				failed++
				small := shrinkFacts(src, func(m *ns.MemSource) bool {
					o, _ := cypherCase(t, sv, g, m, text)
					return o == diffDisagreed
				})
				_, d := cypherCase(t, sv, g, small, text)
				var facts []string
				for _, rel := range small.Relations() {
					for _, f := range small.Tuples(rel) {
						facts = append(facts, rel+show(f.Vals))
					}
				}
				t.Errorf("seed %d (JAALA_GEN_SEED=%d replays it): %s\n  %s\n  over %s", seed, seed, text, d, strings.Join(facts, " "))
				if failed >= 5 {
					t.Fatal("stopping after 5")
				}
			}
			counts[out]++
		}
	}
	var ks []string
	for i, n := range known {
		k := knownCypherDisagreements[i]
		ks = append(ks, fmt.Sprintf("#%d %s: %d", k.issue, k.why, n))
	}
	sort.Strings(ks)
	t.Logf("%d seeds: %d agreed, %d refused by the compiler, %d by neo4j, %d answered a shape jaala has no value for; known, counted not failed: %s",
		seeds, counts[diffAgreed], counts[diffRefused], counts[diffServerRefused], counts[diffShape], strings.Join(ks, ", "))
	if total := seeds * 20; counts[diffAgreed] < total/2 {
		t.Errorf("control: only %d of %d queries were compared", counts[diffAgreed], total)
	}
}

// Every known disagreement's repro still disagrees and still matches its pattern, so a fixed bug's
// entry goes, and a pattern that matches nothing shows.
func TestKnownCypherDisagreementsStillDisagree(t *testing.T) {
	sv := exclusive(t)
	for _, k := range knownCypherDisagreements {
		g, src := diffGraph(k.seed)
		out, diff := cypherCase(t, sv, g, src, k.repro)
		if out != diffDisagreed || knownCypher(k.repro, diff) < 0 {
			t.Errorf("#%d (%s): %s now gives %v %s; delete its entry if it's fixed", k.issue, k.why, k.repro, out, diff)
		}
	}
}

// A relationship variable's type, ends and count answer as Neo4j's do (#441), over the
// differential's graphs: its fact's id is the store's, so RETURN r itself isn't compared. control:
// every query is compared, none refused by either side.
func TestARelationshipVariableAgreesWithNeo4j(t *testing.T) {
	sv := exclusive(t)
	queries := []string{
		"MATCH (a)-[r]->(b) RETURN type(r) AS c0, count(r) AS c1",
		"MATCH (a)-[r]-(b) WHERE startNode(r) = a RETURN a.name AS c0, b.name AS c1, type(r) AS c2",
		"MATCH (a)<-[r]-(b) WHERE endNode(r) = a RETURN a.name AS c0, count(*) AS c1",
	}
	for seed := int64(1); seed <= 5; seed++ {
		g, src := diffGraph(seed)
		for _, text := range queries {
			if out, diff := cypherCase(t, sv, g, src, text); out != diffAgreed {
				t.Errorf("seed %d: %s: %v %s", seed, text, out, diff)
			}
		}
	}
}
