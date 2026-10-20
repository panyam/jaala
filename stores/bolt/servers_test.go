package bolt

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"iter"
	"net/url"
	"os"
	"testing"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"

	"github.com/panyam/jaala/frontends/cypher"
	"github.com/panyam/jaala/ns"
)

var bg = context.Background()

// A server is one graph server the tests run against, named by the environment: JAALA_BOLT_NEO4J
// and JAALA_BOLT_MEMGRAPH hold a bolt:// URL each (user and password in it when the server wants
// them). With neither set the tests skip, unless JAALA_BOLT_REQUIRE is set, as CI's bolt job and
// the exercise set it, so a missing server fails there rather than passing.
type server struct {
	name   string
	driver neo4j.DriverWithContext
}

func servers(t *testing.T) []server {
	t.Helper()
	var out []server
	for _, s := range []struct{ name, env string }{{"neo4j", "JAALA_BOLT_NEO4J"}, {"memgraph", "JAALA_BOLT_MEMGRAPH"}} {
		target := os.Getenv(s.env)
		if target == "" {
			continue
		}
		u, err := url.Parse(target)
		if err != nil {
			t.Fatalf("%s: %v", s.env, err)
		}
		auth := neo4j.NoAuth()
		if u.User != nil {
			pass, _ := u.User.Password()
			auth = neo4j.BasicAuth(u.User.Username(), pass, "")
			u.User = nil
		}
		d, err := neo4j.NewDriverWithContext(u.String(), auth)
		if err != nil {
			t.Fatalf("%s: %v", s.env, err)
		}
		if err := d.VerifyConnectivity(bg); err != nil {
			t.Fatalf("%s (%s): %v", s.env, u, err)
		}
		t.Cleanup(func() { d.Close(bg) })
		out = append(out, server{s.name, d})
	}
	if len(out) == 0 {
		if os.Getenv("JAALA_BOLT_REQUIRE") != "" {
			t.Fatal("JAALA_BOLT_REQUIRE is set, and neither JAALA_BOLT_NEO4J nor JAALA_BOLT_MEMGRAPH names a server")
		}
		t.Skip("no graph server: set JAALA_BOLT_NEO4J or JAALA_BOLT_MEMGRAPH to a bolt:// URL")
	}
	return out
}

// namespace is the one these tests write in, so runs from different checkouts (two worktrees, a
// selfcheck's own) share a server without touching each other's graph: JAALA_BOLT_NAMESPACE, or one
// named by this checkout's path.
var namespace = func() string {
	if ns := os.Getenv("JAALA_BOLT_NAMESPACE"); ns != "" {
		return ns
	}
	wd, _ := os.Getwd()
	sum := sha256.Sum256([]byte(wd))
	return "t" + hex.EncodeToString(sum[:4])
}()

// empty removes everything this run's namespace holds, so each test starts from an empty graph.
func empty(t testing.TB, d neo4j.DriverWithContext) {
	t.Helper()
	emptyIn(t, d, namespace)
}

func emptyIn(t testing.TB, d neo4j.DriverWithContext, ns string) {
	t.Helper()
	label, err := baseLabel(ns)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := neo4j.ExecuteQuery(bg, d, "MATCH (n:"+label+") DETACH DELETE n", nil, neo4j.EagerResultTransformer); err != nil {
		t.Fatal(err)
	}
}

// open is a fresh store over d for g, with the graph emptied first.
func open(t testing.TB, d neo4j.DriverWithContext, g cypher.Graph, schemas map[string]ns.Schema) *Store {
	t.Helper()
	empty(t, d)
	s, err := New(bg, d, g, schemas, Options{Namespace: namespace})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// counting is a Store that counts what the engine asked of it. It embeds the Store, so every
// interface the Store implements stays visible to the engine.
type counting struct {
	*Store
	lookups, batches, batchedValues, whole, shipped int
	pushes, summaries, pages, walks                 int
}

func (c *counting) Walk(ctx context.Context, w ns.Walk) iter.Seq2[ns.Tuple, error] {
	c.walks++
	return func(yield func(ns.Tuple, error) bool) {
		for t, err := range c.Store.Walk(ctx, w) {
			c.shipped++
			if !yield(t, err) {
				return
			}
		}
	}
}

func (c *counting) Push(ctx context.Context, cj ns.Conjunction) iter.Seq2[[]ns.Tuple, error] {
	c.pushes++
	if cj.Page != nil {
		c.pages++
	}
	return func(yield func([]ns.Tuple, error) bool) {
		for row, err := range c.Store.Push(ctx, cj) {
			c.shipped += len(row)
			if !yield(row, err) {
				return
			}
		}
	}
}

func (c *counting) Summarize(ctx context.Context, cj ns.Conjunction, s ns.Summary) iter.Seq2[ns.SummaryRow, error] {
	c.summaries++
	return func(yield func(ns.SummaryRow, error) bool) {
		for row, err := range c.Store.Summarize(ctx, cj, s) {
			c.shipped++
			if !yield(row, err) {
				return
			}
		}
	}
}

func (c *counting) Lookup(ctx context.Context, rel string, bound map[int]ns.Value) ([]ns.Tuple, error) {
	c.lookups++
	ts, err := c.Store.Lookup(ctx, rel, bound)
	c.shipped += len(ts)
	return ts, err
}

func (c *counting) LookupBatch(ctx context.Context, rel string, positions []int, values [][]ns.Value) ([][]ns.Tuple, error) {
	c.batches++
	c.batchedValues += len(values)
	res, err := c.Store.LookupBatch(ctx, rel, positions, values)
	for _, ts := range res {
		c.shipped += len(ts)
	}
	return res, err
}

func (c *counting) TuplesSeq(ctx context.Context, rel string) iter.Seq2[ns.Tuple, error] {
	c.whole++
	return func(yield func(ns.Tuple, error) bool) {
		for t, err := range c.Store.TuplesSeq(ctx, rel) {
			c.shipped++
			if !yield(t, err) {
				return
			}
		}
	}
}
