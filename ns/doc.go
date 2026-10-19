// Package ns is the namespace contract between a host and a query engine: what names exist, at
// which paths, and what each one's arguments denote. It holds names, never evaluates them, and
// imports nothing outside the standard library, so a host's fact layer can register into it without
// depending on an engine.
//
// A Vocabulary is one namespace tree. Every name a query can call lives at a path, its segments
// separated by "." (edge, str.contains, acme.power.rail_budget), and a leaf of the tree is one of:
//
//   - a base relation, described by a Schema, whose tuples a Source serves;
//   - a predicate, a Builtin the host computes: a filter or a generator. A generator declares the
//     binding patterns it accepts (Modes), so an engine can schedule it and refuse a call that can
//     never be made. jaala/stdlib registers the standard ones: the string tests under str
//     (str.contains, str.prefix, ...) and absent at the root;
//   - a derived relation, defined by a module written in a Language an engine provides
//     (jaala/datalog provides "datalog"). The vocabulary asks the language what a module defines
//     and enters each member's path; it never parses the text itself.
//
// A segment is a module or a member, never both, and each path has one definer; the vocabulary
// refuses anything else when it is registered, whatever the kinds involved.
//
// A module records its Origin, such as the file it was read from, and AddModulesFS registers a whole
// tree of module files under a Layout (ByDirectory or ByFileName). A failure one module is responsible
// for, whether AddModule refuses it or a language's Check does, is a *ModuleError carrying that
// module's index and origin, so a host can name the file to fix.
//
// Every member has a signature: per argument a name and an ArgType saying what it denotes (an opaque
// entity kind such as "net", a kind taken per row from another argument, a kind located through an
// owner argument, or a scalar type with a unit), optionally closed over a vocabulary of values. Base
// relations and predicates declare theirs; a derived member's comes from its language's Check, which
// may infer it. Check runs once per state of the vocabulary and is shared by every query over it.
// Lookup and Members answer what is at a path, for a host offering drill-down discovery, and Unknown
// and Hint word an unknown name the way every engine over the vocabulary should.
//
// A Vocabulary holds no data. An engine pairs it with a Source per query, so a host composes its
// vocabulary once, checks it at load, and binds each dataset as it reads it. A Federation is one
// Source over several, for facts kept in several stores: each relation is the union of its members',
// and a lookup goes only to the members whose Holds says they can match. A TimedSource serves several
// versions of its facts, each fact once with its VersionSet (an IntervalSet for linear history, a
// BitSet for history that branches), so one Base answers at any version; one implementing SourceAt
// serves each version as a Source of its own, so a question at a version reaches its store, one
// implementing TimedLookupSource answers a bound call across every version at once, and
// Federation.Timed joins timed members with members that don't change between versions.
//
// The engine never writes to a Source. A host edits its facts through a Changes, a change set of the
// facts to add to and remove from each base relation, which it builds from an edit (or from the diff
// between two versions) and applies to its own store, checked first against the vocabulary. Until it
// commits, Overlay serves its store with a session's change set applied, passing on the store's
// interfaces, and a commit keeps the session as a new version (TimedMemSource.Commit).
package ns
