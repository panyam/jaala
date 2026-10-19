// Package bolt is an ns.Source over a graph server that speaks Bolt, Neo4j's client protocol, which
// Neo4j and Memgraph serve (#411). A host keeps its facts in the server rather than its own process
// and asks jaala the same questions: each relation is read, looked up and batched there, and written
// with Apply.
//
// A cypher.Graph lays the facts out, the same Graph frontends/cypher compiles Cypher over, so a host
// describes its graph once:
//
//   - A label's relation is the nodes holding that label. Its key is the node's id, its properties
//     the node's, and a constant the label fixes (node(id, "Function")) is the label itself.
//   - A type's relation is the relationships of that type, from one node's id to another's.
//   - The graph's Props relation (attr(id, key, value)) is a node's other properties.
//
// Every node carries the label JaalaNode beside its own, so one index on its id finds it whichever
// relation named it. Two relations keying on one value describe one node, as the Cypher compiler
// reads them.
//
// A value keeps its kind: text as a string, a number as a number with its unit and, when the number
// doesn't spell itself (`01`, `3.3V`), its spelling beside it, so answers keep data's spelling. An
// absent value is no property. A fact's ID and citations are kept with it. The store's own properties
// are `id` and those starting with `_`, which a graph's property names can't use.
//
// A server's graph differs from a relation in three ways the store refuses rather than bends: a node
// key and an endpoint are text (a node is found by its key), a label's relation holds one fact per key
// and Props one value per node and key, since each is one node's property, and an argument the Graph
// doesn't place has nowhere to be kept.
//
// The store holds each fact once, as a change set names it, by its values: a host Source serving a
// fact twice counts it twice where the store counts it once.
//
// A lookup asks the server for a bound key, endpoint, property name or text property. A bound number
// isn't asked: the engine reads a number as its argument's value and unit (folded, when the
// vocabulary folds), so the server returns more and the engine keeps what matches. The store says a
// lookup binding a key, endpoint or Props node is indexed (ns.IndexedLookups), so the planner runs
// those first.
//
// A join, a count and a page go to the server too (#412). Push writes a conjunction as a MATCH per
// atom (separate clauses, since one MATCH never binds a relationship twice, which would lose the row
// where two atoms read one fact) and the conditions the server matches exactly as jaala does: node
// ids, text and absent values. A number's constant or join, and an ordering comparison, are left to
// the engine, which checks every row again. Summarize groups by each value with its unit and
// spelling and counts the matches, and is refused where a constant or join isn't exact, since the
// engine can't check a summary's matches. A page orders by _ord, the hex of a node id's UTF-8, which
// every server orders as jaala orders text; Neo4j ordered a parameter's strings by UTF-16 unit, though
// its stored ids came out right. Options.LookupsOnly turns all of it off.
//
// It is a module of its own, as the front ends are, so the Neo4j driver reaches only its importers.
package bolt
