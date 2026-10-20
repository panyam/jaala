// Package cypher compiles Cypher into jaala programs (#352), for mission #356: a host's caller, or a
// model translating for it, asks in the graph query language it knows, and the engine answers the
// program as it would one written in jaala's syntax. It imports ns and datalog, and nothing in jaala
// imports it.
//
// A Graph says how a vocabulary's relations read as nodes, relationships and properties; Compile
// reads a query against one and returns the Program: its text, parsed, the columns as Cypher names
// them, and the variable each $parameter binds. The text is ordinary jaala, so Validate, Eval,
// Explain and Locate read it as they read any program. Helper relations Compile writes start with
// cypher_: a step of several types or either direction (step), a WITH stage (with, its head what the stage hands
// on), a pattern test or EXISTS (exists), the alternatives of an OR (match), and a UNION (union, with
// a part rule for a part that aggregates). An alternative that equates a node with a text, as each of
// an IN's does, writes the text in the node's place in its rule, so a variable-length match walks
// from it (#392).
//
// It compiles the part of Cypher a program expresses today: MATCH over node and relationship
// patterns (labels, inline properties, either direction, several types, a variable length, *m..n
// or a quantifier, as graph.closure over the step's relation (#281), and shortestPath or a path
// selector, which keep the match's nodes), a path variable over one relationship, p = (a)-[:T*]->(b),
// as graph.paths (#280): each trail as a value, or with shortestPath or SHORTEST 1 one shortest path
// per pair, read by length(p), nodes(p), relationships(p) (the edges' facts, through a helper of three
// arguments, wire) and x IN nodes(p), OPTIONAL MATCH after a MATCH as jaala's optional { } (#282),
// its nodes, relationships and WHERE inside the braces, WHERE (comparisons, AND, OR, XOR,
// NOT, IS NULL on a property or a variable, STARTS WITH, ENDS WITH, CONTAINS, =~, IN over a written list, a label test, a
// pattern, EXISTS { } and COUNT { }, the last a body aggregate so a node with no match counts 0),
// lists as values (#283: collect builds one, carried by a WITH and read by size, head, last, l[i],
// x IN l and UNWIND l AS x through stdlib's list.*, a list literal of constants and IN $param
// included), WITH stages with their aggregates and WHERE, UNION and UNION ALL, CALL proc(args) YIELD over a
// predicate or relation of the vocabulary (each YIELD at the argument its label names), and RETURN
// with aliases, DISTINCT, count, min, max, sum and collect (as jaala's list), a constant column
// (#454, bound through a one-value list), ORDER BY, SKIP and LIMIT. A test returned as a value (IN,
// CONTAINS, a comparison, IS NULL) is refused with the rewrite into WHERE, since jaala has no booleans. toLower, toUpper, replace, split(s, sep)[i] and + between text compile to stdlib's
// str.* (+ joins when a side is a string, a string function or a property typed as text, and adds
// otherwise). A $parameter in the goal is bound with Program.Bind; one a rule reads (a stage, a
// union part, an OR, a pattern test, a variable-length step) needs Params, which writes the values
// in as constants.
//
// Where its answers differ from a Cypher engine's, it says so here rather than guess:
//
//   - The rows are distinct, as a program's answer is: RETURN without DISTINCT answers each row
//     once, and count(x) counts distinct bindings of the match, not paths. Two relationships between
//     one pair of nodes are two bindings only when a relationship variable names them, which binds
//     each edge's fact (#441), and only over a type the host stores as facts.
//   - A property read through the graph's Props relation, or a Derived one, is a match: a node without
//     it has no row, where Cypher would answer null, unless an OPTIONAL MATCH declared the node, whose
//     property RETURN reads is read optionally (#400).
//   - Null is jaala's absent, and Cypher's rules for it are written into the program: a comparison or
//     text test over a value that may be absent (a label's column, a node or value an OPTIONAL MATCH
//     bound, a value a WITH carried) is guarded with not absent(...), so it filters the row as Cypher's
//     unknown does, and count(b) over an optional node sums a marker its braces bind. count(p.x) over
//     a column that may be absent still counts the node (#400).
//   - A variable-length match without a path variable is reachability: it doesn't keep Cypher's rule
//     that a path uses a relationship once, which can differ for a bounded range over a cycle. Bind
//     the path, p = (...), for trails (#280). A zero-length one from a node nothing else binds pairs
//     only the nodes its relationship's relation holds.
//   - Within one MATCH, Cypher uses a relationship once; a program's atoms may read one fact twice,
//     so a self-loop stands for both hops of (a)-->(b)-->(c) where Cypher finds no match.
//   - A path is its nodes, so two relationships between the same nodes make one path, and count(p)
//     counts routes, unless relationships(p) is read, when the path names each edge's fact and they
//     make two. nodes(p) and relationships(p) answer a list, as collect does.
//   - min and max reduce numbers, as jaala's aggregates do, so over text they answer null where
//     Cypher answers the least or greatest string (#413's differential).
//   - collect's list is sorted in jaala's answer order, not in match order, so it doesn't depend on
//     the order rows arrive in; lists compare and order by their text (#405).
//
// A relationship variable, [r:T], binds the edge's fact (#441): RETURN r answers its id, type(r) its
// type, startNode(r) and endNode(r) its ends, and r.prop a column of its type or Props keyed by its
// id, r.prop IS NULL testing that none does. A type the vocabulary derives has no facts, so its id and
// properties are refused, and so is reading one an OPTIONAL MATCH binds, which may be null (#400).
// A relationship variable carries through a WITH with its fact, its ends and its type; a subquery may
// bind one of its own or match the MATCH's own relationship again; and p = (a)-[r:T]->(b) binds r
// beside the path (#445). One on a variable-length relationship stays #398's.
//
// The rest it refuses with an *Error naming the issue that would let it: a path variable over more
// than one relationship, ALL SHORTEST and SHORTEST k
// paths as values, shortestPath as a value outside p = (...), and quantified path patterns (#398), an OPTIONAL MATCH before any MATCH, with an OR in its WHERE or
// adding a label to an earlier node (#400), maps, comprehensions and lists of more than constants (#405), functions beyond the aggregates
// above (#284), writes (#287), USE and LOAD CSV (#289), CALL { } subqueries, an ORDER BY, SKIP or
// LIMIT inside a WITH or a UNION part, a label expression beyond A&B and a query without a MATCH
// or a RETURN (#364), and a WHERE of more than 16 alternatives (#388). It parses all of Neo4j 5's
// query syntax to do that (#382), so valid Cypher is refused by its gap rather than as a typo, and a
// mistake anywhere in the text is reported before a gap.
package cypher
