# The Cypher front end

`frontends/cypher`.

- **`frontends/cypher` compiles Cypher to jaala text over a host's `Graph`** (#352, mission #356):
  `Node` and `Edge` name the relation holding a label or a type, its key or endpoints, `Fixed`
  constants (Declaire's `node(id, "Function")`) and property columns, `Props` a key-value
  relation (`attr(id, key, value)`), and `Derived` a relation or predicate per property for any
  node, one the host computes (#378: Declaire's `name.of(id, name)`), read as `kvAtom` reads `Props`,
  after a label's column and before `Props`; `Store.Derived` carries it. The text goes through `datalog.Parse`, so `Validate`, `Locate`
  and `Explain` read it unchanged. A node is its key; an equality every row holds is written into
  the atom (`pkg("app", _)`) unless something else reads the value, which keeps a variable and a
  comparison (`settle`), so a planner sees constants; an alternative's (an IN's, an OR's) equality of a
  node's key to text goes into its own rule's atoms and head the same way (`keysFixed`, #392). A frag's node keys are rendered last for
  that reason, and kept as a frag (`subst`): rendered early, a `$param` key in a rule escaped the
  refusal and matched everything (#352's second PR caught it). An OR becomes a `cypher_match` rule
  per alternative, its head every node of the match beside what RETURN reads, so `count(*)` and
  `collect` count bindings (#439, found by #413's Neo4j differential), a WHERE pattern or `EXISTS { }` a `cypher_exists` rule over the nodes it shares
  (each a stand-in, `nodeVar.of`, so reading one's column inside reads it there), `COUNT { }` a
  body aggregate (so a node with no match counts 0), an `OPTIONAL MATCH` an `optional { }` holding
  its nodes' label atoms, relationships and WHERE (`optPart`, `into`, `closeOptionals`, #282), a
  property RETURN reads through Props on one of its nodes, or a value computed from it, in braces of
  its own, and `count(b)` over its node a `sum` of a `?_seen` its braces bind; a relationship variable (#441,
  `relVar`) binds its edge's fact (`as ?r`) unless a type it reads is derived, and an undirected or
  several-type one goes through a `cypher_rel` helper binding its type and ends, a one-type `type(r)`
  column being bound by `list.member` of a one-value list, since only an atom binds a constant. A WITH
  carries one (#445) as its fact plus hidden columns after the WITH's own (`carried.part`: its ends
  and, for a helper-bound one, its type), so the next stage's `relVar` reads them; a subquery declares
  its own in a scoped `c.rels`, and one the MATCH bound is a stand-in sharing its fact, passed to the
  `cypher_exists` rule as an argument (COUNT's braces share it by name); `p = (a)-[r:T]->(b)` writes
  r's atom beside `graph.paths`. COUNT counts bindings, not distinct keys, so it needed no change, and
  counting `?r` itself is refused by the engine (the aggregate must read a variable an atom binds,
  and `as ?r` lowers later). A constant RETURN or WITH column binds through
  `constColumn` (#454: list.member of a one-value list, as a one-type `type(r)` does), and a test
  returned as a value is refused naming its WHERE rewrite, the text a model's retry reads. Cypher's null rules
  are written into the program: `guards` adds `not absent(...)` to `=` (both sides), `<>` and text
  tests over a `nullable` operand (a label's column, an optional node or its property, a carried
  value, a node a WITH carried from an optional, `nodeVar.absent`), and `IS NULL` reads a variable
  too; what's still refused names #400. A variable length (`*m..n`, a
  quantifier, `shortestPath` or a path selector, the last two keeping only the nodes) `graph.closure`
  over the type's relation or a `cypher_step` helper (#281), and a path variable over one relationship
  (`p = (a)-[:T*]->(b)`, `declarePath`) `graph.paths` as a trail, or `"shortest"` under
  `shortestPath`/`SHORTEST 1` (#280), over a `cypher_wire(?x, ?r, ?y)` helper binding each edge's
  fact when the query reads `relationships(p)` (the parser records which, `edgePaths`), since a
  fact relation can't be walked through `walkSteps`; `length`, `nodes`, `relationships` and
  `x IN nodes(p)` are `path.*` calls (`pathFuncs`). Each `WITH` closes a stage (`stage.go`): a `compiler` per stage on a shared
  `world`, the stage a `cypher_with` rule whose head is what it hands on, a carried node keeping
  its labels with its label atom written only when a column is read (`nodeAtm.lazy`). A `UNION` is
  one `cypher_union` relation, a part that aggregates first a `cypher_part` rule. `CALL p(args)
  YIELD` is an atom over a predicate or relation, written after the patterns (Naive runs a
  generator where it's written), each YIELD at the argument its label names. `toLower`, `toUpper`,
  `replace`, `split(s, sep)[i]` and text `+` are str.* calls (`textFuncs`, `isText` reads the
  schema's types). A `$param` is a goal variable (`Program.Bind`), refused inside any rule, or with
  `Params` a constant everywhere. Anything outside the subset is an `*Error` whose `Gap` names the
  issue (#279 to #289, #364 for CALL { } and an order or limit in a WITH or union part, #398 for
  the paths #280 left: ALL SHORTEST, SHORTEST k, several relationships, quantified path patterns), tested by position in
  `TestGapsAreRefusedByName`; its known differences from a Cypher engine (distinct rows,
  reachability rather than trails, a Props read as a match) are in `doc.go`. Its `TestEval` scores the
  guide's questions in Cypher with `nl.Eval` on the translator, in `nlmodels/cypherarm` since #496
  (`internal/guide` is the fixture both front ends' `TestEval` share, `testdata/eval.golden`,
  `-update`), and `FuzzCompile` (in selfcheck) requires
  any text to compile to a program that validates, or to fail with an `*Error` or a type error
  the query itself makes. A `(` in an expression is tried as a pattern first and rewound
  on failure; `parser.notPattern` keeps each failed attempt by token index, so a deep nesting parses in
  one pass (#449: 6,000 levels took 11s and hung fuzz workers), and `TestThePatternMemoChangesNoParse`
  holds every parse to what it was without it. It is a module of its own (CI's `cypher` job, `SUB_MODS_TO_TAG`), standard
  library and jaala only, and replaces jaala and nl with the checkout (Releasing); `internal/guide`
  imports no front end (its `Case` is converted to `nl.Case` in each test) so the root never
  requires one. Run its tests from inside it: the root's `./...` stops at the module. The docsite's
  `guide/cypher.md` includes `examples/guide/cypher_test.go`, so the docsite replaces the cypher
  module with its checkout too.
