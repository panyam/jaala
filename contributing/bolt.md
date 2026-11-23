# The Bolt store

`stores/bolt`, a Source over Neo4j and Memgraph.

- **`stores/bolt` is a Source over a graph server that speaks Bolt** (#411, mission #420), Neo4j's
  protocol, which Neo4j and Memgraph serve: a module of its own on `neo4j-go-driver` (no third-party
  module beyond it, and it builds for wasm), laid out by a `cypher.Graph` so a host describes its graph
  once. A label's relation is nodes with that label keyed by `id`, a type's relation relationships, the
  Props relation the nodes' other properties, and every node also carries `JaalaNode` (or
  `JaalaNode_<ns>` with `Options.Namespace`, #435: each its own graph and index), indexed on `id`
  (Memgraph only takes the index in an auto-commit run, and names itself "Neo4j/v5.11.0 compatible ...
  Memgraph"). It streams a whole read, looks up and batches (`UNWIND $vals`, one query per label or
  type) by key, endpoint, Props key or text property, never by a number (the engine folds units), and
  says a key or endpoint lookup is indexed. `Apply` writes an `ns.Changes` in one transaction, removals,
  then checks, then additions; a value keeps kind, unit and spelling (`_unit.p`, `_text.p`), a fact
  its id and citations (`_id:L`, `_cites:L` on a label's node, `_id`/`_cites` on a relationship, `_id.k`
  for a Props key) and its values' key (`_key…`), which a relationship MERGEs on. It refuses a key that
  isn't text, a second fact per node in a label's relation or per Props key, an unplaced argument and a
  reserved property (`id`, `_…`), and holds each fact once, as a change set does. Its tests need a
  server (`JAALA_BOLT_NEO4J`, `JAALA_BOLT_MEMGRAPH`, skipped without unless `JAALA_BOLT_REQUIRE`):
  CI's `bolt` job runs both as service containers, the corpus at 300 seeds (`JAALA_BOLT_SEEDS`) through
  `internal/corpus` against each seed's MemSource, values and, under `CanonicalCites`, citations and
  witnesses (a dropped edge id survived a values-only compare). `scripts/bolt-servers.sh` runs a command
  against the servers `STORES` names, using a URL, else the shared pair `make servers-up` starts
  (`stores/bolt/compose.yml`, project `jaala-bolt`, 17687 and 17688), else a throwaway container; exit 3
  for "could not run". The tests write in a namespace of their own (`JAALA_BOLT_NAMESPACE`, else a hash
  of the checkout's path), so worktrees and selfchecks share the pair; two runs forced into one
  namespace wipe each other's graph mid-run; `make exercise-graph-server` and selfcheck's graph-server step (1000 seeds) go through it.
  Joins, counts and pages go as Cypher (#412, `push.go`): a MATCH per atom (one MATCH never binds a
  relationship twice, which lost the row where two atoms read one fact), conditions only where the
  server matches exactly (ids, text, absent, null-safe text joins); a number's constant or join is
  left to the engine (`inexact`), which also refuses the conjunction as a summary, since a summary's
  matches aren't rechecked. A page orders by `_ord` (hex of the id's UTF-8) and only over ids;
  `TestAPageOrdersIdsAsJaalaDoes` passes ordering by `.id` too, since Neo4j orders stored ids by code
  point, so `_ord` is defensive, against Neo4j ordering parameters by UTF-16 unit. A lone literal with a
  constant is a lookup, never a push, so a push's number-constant path needs a direct `Push` test
  (`TestAPushNeverLeavesOutAMatchOnANumber`), and a summary's spelling shows only for text saying more
  than the number (`3.3V nominal`), since answers canonicalize `3.3V`. `Options.LookupsOnly` turns
  pushdown off; the exercise prints shipped with it and without. `walk.go` walks a type's relation
  (`ns.WalkSource`) in one query: Neo4j's `*0..d-1` with `WITH DISTINCT n` (its planner prunes it to a
  BFS: 5,423 facts in 8 ms on the exercise's graph), Memgraph's `*BFS ..d-1` plus the starts, which its
  BFS doesn't return; a bound property or several literals (`ConjunctionWalkSource`) stay the engine's.
  `TestCypherAgreesWithNeo4j` (#413, `cypherdiff_test.go`) runs the same Cypher through
  `frontends/cypher` and natively in Neo4j over seeded graphs that rule out doc.go's differences (no
  self-loops, one relationship per ordered pair, every node every property) and seeded queries that
  mean something (`cypherGen`: the grammar's sampler writes syntax, and 1997 of 2000 draws didn't
  compile). It clears the whole server, since a label MATCH sees every namespace, so it runs only with
  `JAALA_BOLT_EXCLUSIVE` (CI's job, 100 seeds; selfcheck's own step, `EXCLUSIVE=1`, 1000). A shrink
  drops whole nodes or edges, never one property, or it recreates the Props-as-match difference.
  `knownCypherDisagreements` holds the open ones with a repro and seed (#438, min/max over text);
  #439's entry went when #440 fixed it, which `TestKnownCypherDisagreementsStillDisagree` asked for.
