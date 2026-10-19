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
// selector, which keep the match's nodes), WHERE (comparisons, AND, OR, XOR,
// NOT, IS NULL, STARTS WITH, ENDS WITH, CONTAINS, =~, IN over a written list, a label test, a
// pattern, EXISTS { } and COUNT { }, the last a body aggregate so a node with no match counts 0),
// WITH stages with their aggregates and WHERE, UNION and UNION ALL, CALL proc(args) YIELD over a
// predicate or relation of the vocabulary (each YIELD at the argument its label names), and RETURN
// with aliases, DISTINCT, count, min, max, sum and collect (as jaala's list, a text), ORDER BY, SKIP
// and LIMIT. toLower, toUpper, replace, split(s, sep)[i] and + between text compile to stdlib's
// str.* (+ joins when a side is a string, a string function or a property typed as text, and adds
// otherwise). A $parameter in the goal is bound with Program.Bind; one a rule reads (a stage, a
// union part, an OR, a pattern test, a variable-length step) needs Params, which writes the values
// in as constants.
//
// Where its answers differ from a Cypher engine's, it says so here rather than guess:
//
//   - The rows are distinct, as a program's answer is: RETURN without DISTINCT answers each row
//     once, and count(x) counts distinct bindings of the match, not paths. Cypher's duplicate rows
//     need a relationship as a value (#279).
//   - A property read through the graph's Props relation, or a Derived one, is a match: a node without it has no row,
//     where Cypher would answer null (#282).
//   - A variable-length match is reachability: it doesn't keep Cypher's rule that a path uses a
//     relationship once, which can differ for a bounded range over a cycle (#280). A zero-length one
//     from a node nothing else binds pairs only the nodes its relationship's relation holds.
//   - collect answers a text listing the values, not a list (#283).
//
// The rest it refuses with an *Error naming the issue that would let it: a relationship variable
// read anywhere (#279), paths as values and quantified path patterns (#280), OPTIONAL MATCH (#282), lists, maps and UNWIND (#283), functions beyond the aggregates
// above (#284), writes (#287), USE and LOAD CSV (#289), CALL { } subqueries, an ORDER BY, SKIP or
// LIMIT inside a WITH or a UNION part, a label expression beyond A&B and a query without a MATCH
// or a RETURN (#364), and a WHERE of more than 16 alternatives (#388). It parses all of Neo4j 5's
// query syntax to do that (#382), so valid Cypher is refused by its gap rather than as a typo, and a
// mistake anywhere in the text is reported before a gap.
package cypher
