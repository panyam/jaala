# Issues and missions

How issues are ranked, and what each mission did. Live status is on the mission issues and in `NEXTSTEPS.md`.

Issues are ranked by the mission they serve (labels `P0`–`P3`, `waiting`, `mission`,
`mission:active`, `mission_<slug>`), and its tickets are linked as blocked-by. Most of jaala's
missions unblock a host's active mission; such a mission closes when the host's half of its exercise
passes too, so it can stay open after jaala's tickets close. Two are active: `mission_text_memory`
(#437, below) and `mission_agent_frontend` (#322): a host's caller asks in plain language and jaala's
front end turns it into a program that checks. Declaire's translator runs (declaire#288) reopened it
with #482 to #485 and #490, all closed by v0.1.91 (31/31): jaala's model clients are chakra's (#487),
jaala a helper a chakra agent plugs in, with the translator entry point (#490), callable picks (#484),
the re-ask (#483) and host prompt overrides (#485). It waits on Declaire: declaire#357 (the entry
point), declaire#74 (its relations callable) and a rerun of its arms on declaire#288. jaala's half is `frontends/nl` and `frontends/nlmodels`
(#321, #323 to #326, #330, #331, #336, #337, released in v0.1.66 and frontends/nlmodels/v0.1.66);
`go test -run TestEval -v .` in `frontends/nlmodels/translator` is its jaala-side exercise (`frontends/nl` until #496; fix line 5/5). Declaire's
translator-alone arm (declaire#295, for declaire#290) first ran on v0.1.66 and passed 2 of 14 on
Qwen2.5-Coder 7B against declaire#288's bar of 10; its jaala asks became #340 and #341 (PR #342,
released in v0.1.67). Declaire's 32B run then stopped on an unexplained empty answer on 6 of its
11 failures (declaire#296), which became #343 and #348 (PRs #346 and #349, released in v0.1.68):
`Report.Empty` names the culprit and whether it matches alone, and the translator sends that back.
#344 (domain packs, `nl.Pack` and `nl.DataCard`, PR #350, v0.1.69) and #347 (`frontends/mcp`, an
MCP server for any vocabulary that a host wraps with its pack, PR #351, v0.1.70 and the first
`frontends/mcp/v0.1.70`) followed; Declaire moves its translator and server onto them in
declaire#302, and its wording experiments (declaire#308) replace `mcp.Instructions`' default. The mission's done-when points at
declaire#288's translator line rather than quoting its number, which is Declaire's to set.
`mission_cypher_frontend` (#356, from epic #278) ran from 2026-10-18 and closed 2026-10-20, when Declaire's v0.1.84 run (declaire PR #348) answered x10b:
an agent or the model translating for it writes Cypher, and `frontends/cypher` compiles it into a
program that checks, so Declaire can measure whether Cypher or jaala's syntax costs an agent less per
correct answer (declaire#288's "one arm per front end"; the arms are declaire#248). Its tickets: #352
(the compiler over a graph view of a vocabulary, and `go test -run TestEval -v .` in `frontends/cypher`, `nlmodels/cypherarm` since #496,
`nl`'s cases in Cypher; PRs #359 and #369), #353 (errors located in the Cypher text, #372), #354
(the translator writing Cypher, the 32B arm) and #355 (`frontends/mcp` taking Cypher, closed by
#366's `cypher` tool and `cypher.Store`, PR #368). #279 (facts as terms, #371) came out of
`waiting` beside them; the other gaps (#280 to #284) and #364 (`CALL { }`, an order or limit in a
stage) stay `waiting` until an exercise case is refused for one. The two store missions closed on 2026-10-16. `mission_store_federation` (#219), a host answers a question over many stores without holding them
in memory, measured by the `Stores` benchmark (#218: stores asked, unindexed probes, round trips,
peak heap and a repeated question's fetches as the number of stores grows) and Declaire's `bench
-time` on a federated view. The benchmark's stores run in its process, so `peak-MB` counts their
work too: `page`'s 15 MB at 50 stores is mostly the fixture's own paging (`pageOf` builds every
match before sorting), not the engine, so profile before blaming jaala. `peak-MB` also moves run
to run (`attrs-of-reached` read 14.3 to 17.2 MB over four runs of one commit), so rerun with
`-count 3` before reading a few MB as a regression; store calls and `shipped` are exact. Run it as
`go test ./datalog -run '^$' -bench 'BenchmarkStores/.*/stores=50' -benchtime 1x`. Its seven jaala tickets closed, the last #233 (a `StreamSource` read
charged to the budget, from declaire#211's measurement).
`mission_store_pushdown` (#235), epic #213's second half: a store answers the part of a question it
can evaluate (a conjunction, a walk, an aggregate, a page) and jaala combines the results, measured
by the same benchmark's `shipped` column and its questions `two-hops-in-repo`, `count-lines`, `page`,
`self-loops` (#236) and `attrs-of-reached` (q09r's shape, #268); `edges` is the unbound read no store
filters, and `edges-budgeted` carries #233's stopped read. All fourteen of its jaala tickets are
closed (#236 to #241, #246, #250, #252 to #254 by v0.1.50; #263 and #268, from Declaire's first
measurement and folding #259, by v0.1.52; #270, a witnessed goal's bindings reaching what its rule
calls, q09r's `http.route`, after v0.1.52): a conjunction, a walk, a count, a page, a batch of
bindings (across a partitioned federation too), a walked step and a lone filtered literal each go to
a store, and every jaala line of its "Done when" holds. Declaire's first real-view timing (x04 in
its `bench/external`, declaire#255: 2m44s on stores against 4.6s on mem) reopened it: #290
(v0.1.55) read generator-only relations in full, taking x04 to 51.5s; #291 (v0.1.56) stopped a push
at a filter, 14.6s; #293 (v0.1.57) ranked an all-free generator after held relations, 3.6s, under
mem's 3.3s. #297 (v0.1.57) lets a store take `str.glob` with a push (`ns.FilterSource`), which x05
needs Declaire to implement. All 18 jaala tickets closed. The earlier four are closed too: `mission_selfcheck` (#86, at v0.1.29) left `./selfcheck.sh` as the check to
run on `main` (now every five merged PRs, `make selfcheck-status`); `mission_docsite` (#104) left the docs site
with every example run by the engine; `mission_aggregate_answers` (#172, at v0.1.42) left Soufflé
checking every aggregate program but `list`; and `mission_browser_memory` (#193, closed with agni
#896) left the `Memory` benchmark's peak heap in use (not `B/op`, which the join's short-lived
bindings dominate) as the measure of what a query holds. Several missions can be active at once (`queue.sh` prints them). `mission_graph_server_stores` (#420,
started 2026-10-19) is worked in its own worktree and isn't `mission:active`: a host answers over a graph
kept in Neo4j or Memgraph as it would in memory, with the server doing the joins. Its exercise is `make
exercise-graph-server`; its tickets are #411 (`stores/bolt`), #412 (pushdown as Cypher, profiles), #413
(checking a Cypher database against jaala), declaire#339 and agni#1073, the last two left with the
hosts. `mission_text_memory` (#437, filed 2026-10-19, `mission:active` from 2026-10-26) is a host's assistant extracting what it was told into versions and
answering from them, for less money and time per correct answer than reading the history: #414
(`nl.Extract`, whose eval makes its exercise's first half runnable; since #502 the translator's
`Extract` over an `nl.ExtractSession`, and `TestExtract` in `frontends/nlmodels/translator`), then #415 (P1, its first PR a
`make exercise-memory` target running the one-shot arm on an oracle subset), LongMemEval used to find which
primitives an agent lacks (arms that hold the model fixed and vary the layer), not to rank jaala as an
agent. Epic #455 lists those primitives and which side owns each; the split between jaala and its
hosts is the docsite's `architecture/layering.md` ("What stays with a host"). A new issue gets a
priority and a mission link when filed, or `waiting` with the trigger that would unpark it.
