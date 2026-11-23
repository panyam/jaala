# jaala

A Datalog engine for graph-shaped data, extracted from agni. `ns/` is the contract (the namespace
tree, values, signatures, the `Language` hook), `stdlib/` the standard vocabulary, and `datalog/` the
engine. Read `ns/doc.go`, then `datalog/doc.go`. Each front end under `frontends/` is a Go module of
its own: `nl` (plain language), `nlmodels` (its translator, on chakra), `mcp` (an MCP server) and
`cypher` (Cypher compiled to jaala). `stores/bolt` is a module too, a Source over Neo4j and Memgraph.
The root's `./...` stops at each module, so run a module's tests from inside it. `docsite/` is the
documentation site, a module of its own; every example runs on the engine (`make -C docsite check`).

This file holds what every session needs. The detail lives in `contributing/`, one file per area
([index](contributing/README.md)). Read the file for the area before changing it:

| Working on | Read |
| --- | --- |
| errors, values, parsing, answers, facts, closures and paths | [contributing/engine.md](contributing/engine.md) |
| checks, Explain, aggregates, units, demand, inlining, planning | [contributing/rewrites.md](contributing/rewrites.md) |
| a Source interface: lookups, streams, batches, pushdown, walks, versions | [contributing/stores.md](contributing/stores.md) |
| `frontends/nl`, its grammars, `frontends/nlmodels` | [contributing/nl.md](contributing/nl.md) |
| `frontends/mcp` | [contributing/mcp.md](contributing/mcp.md) |
| `frontends/cypher` | [contributing/cypher.md](contributing/cypher.md) |
| `stores/bolt` | [contributing/bolt.md](contributing/bolt.md) |
| tests, selfcheck, the corpus, Soufflé, fuzzing, fixtures | [contributing/testing.md](contributing/testing.md) |
| a release, branches, stacked PRs | [contributing/releasing.md](contributing/releasing.md) |
| issue ranking and mission history | [contributing/missions.md](contributing/missions.md) |
| agni or Declaire, upgrade notes | [contributing/hosts.md](contributing/hosts.md) |

## Commands

CI (`.github/workflows/ci.yml`) runs this set at the root, and each module's job runs its own version
of it inside the module. All of it must pass:

```sh
gofmt -l .                      # must print nothing
go vet ./...
go test -race -count=1 ./...
go test -count=1 ./datalog -run '^TestGeneratedProgramsAgree$'   # the corpus skips under -race (#143)
GOOS=js GOARCH=wasm go build ./...
go list -deps ./... | grep '\.' | grep -v '^github.com/panyam/jaala' | grep -v '^vendor/'   # must print nothing
```

- `make lint seccheck secrets` is CI's `scan` job (staticcheck, gosec, gitleaks) over the root and
  every module. A gosec finding is fixed, or annotated `// #nosec G<n> -- <why>` on its line.
- `make selfcheck` runs `./selfcheck.sh` (the corpus at 5000 seeds, fuzzing, Soufflé, a real
  `llama-server`) on a commit in a worktree of its own, and notes the result on the commit.
  `make selfcheck-status` says when one is due: every five merged PRs. Never `pkill -f selfcheck.sh`.
- Every Makefile recipe is a one-line call into `scripts/`. Put a new target's shell there.
- Running tests under wasm needs an empty environment; [testing.md](contributing/testing.md) has the line.

## Rules every change follows

- **Standard library only, and it must build for wasm.** agni runs the engine in the browser. The
  deps check greps for a dot, so a standard package can trip it too (`crypto/sha256` does). Run the
  `go list -deps` line before pushing a new import.
- **The packages layer one way.** `ns` imports nothing in jaala, `stdlib` imports `ns` and never
  `datalog`, and production `datalog` imports `ns` only. `TestLayering` guards it. Nothing in the root imports a
  front end.
- **Error text keeps its `query:` prefix, and existing fragments stay stable** (`unknown relation
  "x"`, `takes N args`, `not stratifiable`). agni's tests match on them. A new message gets new
  wording and a line in the docsite's `content/reference/errors.md`. A check error is a
  `*CheckError` built with `checkErr`, so `Locate` and `nl.Explain` can read it.
- **`Base` is shared across concurrent `Eval`s.** Anything mutable reachable from it is per-query,
  atomic or locked. Each Eval carries its own run state; a new loop over candidates calls
  `countWork` and returns its error.
- **A rewrite that rebuilds a `Literal` keeps its `at`, and one that rebuilds a `Rule` its `text`.**
  Witnesses read them, and dropping one fails silently.
- **`Naive` is the reference evaluator and stays unoptimized.** A new evaluator or option goes in
  the tests' `both()`.
- **A docs change rides with the code.** A store interface change updates the docsite's
  `guide/stores.md` and `architecture/stores.md`; an `ns.Builtin` change `guide/predicates.md`. A
  name added to stdlib moves nl's and the translator's goldens (`-update`).
- **Every piece of prompt text is a `Prompts` field** (`frontends/nl/prompts.go`) with a test.
- **Comments use agni's circuit vocabulary on purpose**, but host-specific test data stays in the host.

## Testing

- **Red-check every new test.** Break the behaviour it guards, keep the symbol, and see the test fail
  on its assertion. A build failure counts as "not checked", and a `-run` matching no test passes.
- **Never relax an assertion.** Count assertions added and removed, and report both.
- **Give a test a control** that shows its fixture can tell the cases apart. Most surviving
  mutations here were fixtures that could not.
- **Inlining hides rewrites.** A test of how a rewrite treats a derived relation gives it two rules,
  or runs with `Witnesses()`.
- **Cost is pinned** in `datalog/testdata/work.golden`, within 10% either way. A PR that moves it says why.
- The generated corpus (`generate_test.go`) compares all three evaluators on random programs;
  `JAALA_GEN_SEED=n` replays one. A new corpus control has to hold at CI's 300 seeds.

## Releasing

The owner picks the version (patch bumps on v0.1.x). Merge the PR, run the CI set on the merged
`main`, then `make bump-root V=v0.1.N`, commit that to `main`, and `make tag-push V=v0.1.N
NOTES=notes.txt` on that commit. Check each module resolves with `GOPROXY=direct go list -m
github.com/panyam/jaala/<dir>@V` from a scratch module. A breaking change gets an upgrade note on the
issue a host filed. [releasing.md](contributing/releasing.md) has the rest.

## Issues and PRs

- Issues are ranked by the mission they serve (`P0` to `P3`, `waiting`, `mission:active`,
  `mission_<slug>`); `~/.claude/skills/retriage/queue.sh` prints the queue. Live status is on the
  mission issues and in `NEXTSTEPS.md`.
- Before branching, and again before opening a PR, run `gh pr list --state all --search '<N>'`:
  another session may be on the same ticket. Work in a worktree off `origin/main`, and create the
  branch before the first commit. Squash against `origin/main`, never a local name.
- A PR body closes only its own issue. Any "fix", "fixes", "close" or "resolves" before `#N` closes
  it, even mid-phrase, and across repos too. Write "left for #N" or "for panyam/jaala#N".
- Check `gh pr view N --json state` says `MERGED` before deleting a branch; deleting an open PR's
  head branch closes it.
- jaala issues are worked from jaala sessions and host issues from host sessions. A host that needs
  something files a jaala issue. [hosts.md](contributing/hosts.md) says how to run a host's suite
  against a branch.
