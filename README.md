# jaala

Jaala (Sanskrit for "network") is a small query engine for graph-shaped data.

Its engine, `datalog`, is a Datalog evaluator with stratified negation, recursion, aggregation (in
the answer, with `having`, and in rules: `degree(?n, count(?m)) :- edge(?n, ?m)`), `order by` and `limit`, binding-pattern indexes, and provenance: every answer row carries the citations of the
facts that produced it, and on request a witness tree of the rules and facts behind it. `SemiNaive`
derives only what a query's constants demand (magic sets, including through negation) and plans each
rule body, so cost doesn't depend on the order a rule is written in. It knows nothing about any
particular domain.

It comes in three packages, layered one way:

- `ns` is the vocabulary: one tree of dotted paths holding every name a query can call, with each
  name's signature. A leaf is a base relation whose facts some `Source` serves, a predicate the host
  computes, or a derived relation defined by a module. `ns` parses and evaluates nothing, and imports
  only the standard library, so a host's fact layer can register into it without taking on an engine.
- `stdlib` is the standard vocabulary: the string tests (`str.contains`, `str.glob`, ...) and
  `absent`, plus the glob and regex compilers for Go code that must agree with queries. It imports
  `ns` and never the engine.
- `datalog` is the engine. It provides Datalog as a module language, links the modules a query
  names, and evaluates queries over a vocabulary paired with one `Source`. It imports only `ns`.

```go
v, _ := ns.NewVocabulary(nil)                     // names only; no data
v.AddRelation("component.net", ns.Schema{Arity: 2, Labels: []string{"ref_des", "net"}})
v.AddRelation("component.class", ns.Schema{Arity: 2, Labels: []string{"ref_des", "class"}})
stdlib.Register(v)                                // str.contains, str.prefix, ..., absent
v.AddLanguage(datalog.Language)
v.AddModule("net", datalog.LanguageName, `
# Nets that carry a test point.
has_test_point(?n: net) :- component.net(?tp, ?n), component.class(?tp, "test_point");
`, "inline")                                      // origin: where the text came from
v.AddModulesFS(libFS, "lib", datalog.LanguageName, ns.ByDirectory(".dl"))   // or a whole tree
if err := v.Check(); err != nil { ... }           // once, at load; *ns.ModuleError names the file

base, err := datalog.NewBase(v, designSource)     // per dataset
rows, err := datalog.SemiNaive{}.Eval(ctx, datalog.MustParse(`net.has_test_point(?n) => ?n`), base,
    datalog.Budget(10_000_000))                   // optional: Bind, Budget, Witnesses
```

A query naming `net.has_test_point` pulls in the module that defines it. Each member carries a
signature (argument names, entity kinds, scalar types and units), declared or inferred through its
rules, which `v.Lookup(path)` returns for a host that lists and drills into what is available. A
query constant, or a value bound with `datalog.Bind`, is read as its argument's type: `"3"` in a
number argument is 3, and `"abc"` there is an error rather than an empty answer. `Bind` takes a set
of values per variable, so one query can ask about a whole selection.

It started as the query engine inside [agni](https://github.com/panyam/agni), an EDA tooling
engine, and was extracted so other graph tools could share it.

All three packages import only the Go standard library and each other, in that one direction, and
build for `GOOS=js GOARCH=wasm`.

`frontends/` holds what turns text other than jaala's own syntax into a program that checks, which
no engine-only host needs: `frontends/nl`, which asks a model to write a program from a
plain-language question (the vocabulary as a card, a grammar per vocabulary for constrained
decoding, errors a model can fix from, a translate loop and an eval), and `frontends/nlmodels`, a
module of its own with clients for llama.cpp and Ollama. See `frontends/README.md`.

The documentation site lives in `docsite/`, where every example is run on the engine when the
site is built; `docsite/README.md` says how to build it and add to it.

Licensed under Apache-2.0.
