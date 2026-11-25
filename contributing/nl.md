# The plain-language front end

`frontends/nl`, its grammars, `frontends/nlmodels` and its translator, and front ends that write another language.

- `nl` (the card, Explain, the `Session`) imports `ns` and `datalog`, is a module of its own (#359; CI's
  `nl` job), and nothing in the root imports it. It runs no model, keeps no agent loop and exports no
  model interface (since #496 for translating, #502 for extracting): the loops are
  `nlmodels/translator`'s, on chakra Runners, over nl's `Session` and `ExtractSession` (below). A refused draft
  goes back with `Explain`'s text, a failed model call, a repeated draft (#340, refused unchecked) or
  an empty answer (over `Options.Base`) moves straight to the fallback, which starts a fresh
  conversation (an empty answer with no fallback left goes back to the model, and its first repeat of
  that draft is asked again with the reason, the call to change and the grounded values,
  `Try.Reasked`, #483), and `Try.Kind` adds `parse`, `eval`, `empty`, `repeat` and `model` to a
  `CheckError`'s kinds. An unknown call's `Fix` names the program's
  own rule it likely means (`localFix`, #341, at `Validate` and `evaluate`, since the vocabulary's
  suggestion can't see the rules). The translator's tests script the model (`scripted`), so tries are exact.
  A draft run over the Base keeps its `datalog.Report` (`Try.Report`), and an empty answer's error
  is an `EmptyAnswer` carrying `Report.Empty`, whose text goes back to the model (#343), with
  `Without` (`without.go`): the goal rerun without the culprit (or first an equality to a constant over
  its variables, which #370 folded into the atom named) selecting the variables it shares with the rest,
  none the rest pins to a constant, up to five rows, goal literals only and never under a FrontEnd;
  `nl.Report.Costliest` ranks drafts by work, and its work column shows only when one was evaluated.
  A host's domain is an `nl.Pack` (`Options.Pack`, #344): `About`, `Card`, `Examples` (checked by
  `Pack.Validate` before any model is asked), a `Grounder` used when the translator's is nil, and a `Check`
  that refuses an answer as `answer`, back to the same model. Its `Prefix()` follows the card and the
  instructions. `nl.DataCard(ctx, b)` writes the generic half of `Card` with one counting program per
  argument (named variables, so `count` counts facts); #347's MCP server reads the same pack.
  `nl.Eval` (#326) scores a `translate` func over `Case`s, matching a row by its values as a multiset
  (the model picks the columns' names and order) and rows as a multiset unless `Ordered`. A Result's `Outcome` is pass, wrong or abstain (an `*Abstained` error, `AbstainAgent` for a host's own
  agent), `Case.Tags` group `Report.Summary(prices)`, and `nl.Record` (`record.go`, #536) is a run as JSON:
  `Write` a file per run, `ReadRecords`, `HistoryTable`. A Try's or Step's `Calls` counts its model calls,
  the loop adding one for chakra's guided finalizing call, which raises no `EventStep`; a model with no
  `Prices` entry is `Cost.Unpriced`, never free.
  `nl.Profile` (`profile.go`, #537) reads records into a `ModelProfile` per `Record.Model`: tallies, cost,
  price, and six skill `Rate`s from the tries. `Case.Reads` feeds schema choice, and `Eval` runs an
  abstention's draft (jaala's text, or a low-agreement draft's rows) into `Result.WouldPass` for calibration.
  Grounding reads `Trace.Offered` (`Session.Offered`: the up-front grounding, then each find's values,
  which the translator copies off its Session) against `Result.Constants`, per name; `Matched` is the old
  first-try-answered rate. `TestEval`
  (in `frontends/nlmodels/translator` since #496) is #322's exercise until Declaire's arm runs: the
  guide's vocabulary, a scripted small model and fallback (`byQuestion`), and the report in its
  `testdata/eval.golden` (`-update`).
  `Options.Actions` (#393, `actions.go`) lets a model take `look`, `values`, `try` and `find` before
  its answer, as the translator's chakra tools (`Actions.AsTools`, which the translator sets), or read
  from the reply under a grammar wrapping the draft's (`actionGrammar` renames its root `program`) for
  a host's own loop with it off: each is a `Trace.Steps` entry, not a `Try`, so first-try counts
  answers. The actions are language-blind: `try` runs through the front end, and a front end that is
  an `nl.Prober` (Cypher's) writes `look` and `values` in its terms. Off by default, so `TestEval`'s
  goldens don't move. `find` asks the Grounder, offered only when there is one. `Options.Vote`
  (`vote.go`) draws more answers once one is accepted (the translator's `agent.Consensus`, seeded per
  draw; at temperature 0 a vote agrees by construction), keeps the rows most agree on (`voteKey`,
  `Session.Key`, a refused draft agreeing with nothing),
  and sets `Program.Confidence`; `Options.Escalate` moves a disputed answer to the fallback, keeping it
  if nothing better comes (`Trace.Escalations`); `Options.Budget` caps tokens, time and model calls
  (`*BudgetSpent`; `Calls` is chakra's `TreeBudget.MaxSteps`). `Options.Tiers` (`tiers.go`, #538) is a per-call
  policy, `local`, `local-then-frontier` or `frontier`, over the translator's `Model` (local) and `Fallback`
  (frontier), each tier capped on its own: the translator's `tiered` runs the local tier judged by
  `Abstain`, then the frontier one from `HandoffDraft`'s try through `Conversation.Handoff`
  (`Prompts.Handoff`); `Try.Tier`, `Step.Tier`, `Trace.Tier`, and `Summary.Tiers` split cost by tier.
  mcp's ask `mode` is the policy (`both` is `local-then-frontier`), `mcp.Options.Tiers` its caps. `Options.Abstain` (`abstain.go`, #482) turns a disputed answer, running out of
  tries or the budget into an `*Abstained` with the closest draft, through `Abstain.Judge`, and a model
  failing never abstains. `Abstain.Declined` (`decline.go`, #551) offers the model `none:` and a reason
  (`Prompts.Decline`, which must say counting is the program's work, or gpt-5.5 declined to count), the
  answer grammar's root renamed `drafted`; `Judge` reads it as `KindDeclined` and `Verdict.Stop` with a
  `*Declined`, and `Abstain.Judge` gives `AbstainDeclined`; `closest` never offers a decline. `Try.Class` and `Result.Class` sort failures as grounding, construct,
  semantics, budget or model (and a case as `abstained`), reading a `CheckError` before `KindAnswer`, since datalog's order-by
  kind is spelled `answer` too; `Report.Failures` counts them, outside `String`, so goldens hold.
  With `Actions`, `state.go`'s `memory` keeps the text values actions showed (`act` returns them), and
  a draft refused as empty or for a constant (`repairable`) has each quoted value the data holds in
  exactly one other spelling (`spelling`: letters and digits, lower-cased) rewritten, from those and
  the refused argument's Domain (`domainOf`); the rewrite is kept only if it then passes, as a `Try`
  with `Repair`. `Actions.Compact` sends `memory.compact`'s one turn instead of the conversation.
  `Actions.Collapse` (#547) sets the preset's `FirstStuck` to chakra's `CollapseStuckSteps`, keyed by
  `actionKey` as `StuckKey` is; off by default, a no-op under Compact (`collapse_test.go`).
  `Actions.Ledger` (#514) shows `Conversation.LedgerParts` on every step's call, each list written to
  a chakra slot (`NewSlotsWith`, v0.3.2, header `Prompts.LedgerHeader`, as the user's) by a request
  stage, so chakra's guided finalizing call carries none: findings and drafts apart, `memory.drafts`
  marking which; with `Compact` the one turn leaves those lines to it. `Pack.Preview` (#514,
  `preview.go`) names a question's relations or values targets; `Session.Preview` reads `PreviewRows`
  of each once per Session (jaala's programs unless the front end is a Prober) and feeds `memory.see`,
  and the translator's `preview` stage is chakra's `NewRetrievalStage`; `loop.emit` keeps its block
  (`RetrievalInfo.Block`) in `loop.shown` so Compact's stage and a vote's history (`withPreview`) carry it.
  `Actions.Build` (#514 piece 4) adds `add LITERAL` and `drop`: `Conversation.build` keeps a
  literal whose body checks (an empty answer included) in `Conversation.body`, refuses one that
  doesn't, and an answer of a tail alone (`=> ?p`) follows the body (`withBody`, in `Judge`, and in
  `Draw` through `Session.built`); `buildGrammar` lets the answer grammar take that tail. An add of a
  literal the body holds is a repeat; a drop never is. Off under a FrontEnd.
  `paths.go` (#514 piece 5): a host's `Pack.Shape` names its
  kind relation, edge relations and id-valued `Ref`s; `KindGraph` reads from the data which kinds each
  joins (the vocabulary can't say: Declaire's edges are untyped and a kind is data); `Paths` searches
  that graph breadth first between two `End`s (a kind and literals over `?x`), a link either way and a
  same-kind link as one step and then as a `graph.closure`, a kind visited once except arriving back at
  the target's, and keeps the programs that answer, shortest first, a longer one answering the same
  first rows dropped. With `Shape` over a Base in jaala's language (`Options.offersPaths`) the model
  gets the `paths FROM => TO` action (`ParseEnds`, `Session.paths`), each end a kind then literals over
  `?x`, its kind graph `Pack.Links` or `KindGraph`'s read once per Session; `guide.Code` is the fixture.
  `Pack.Callables` (#484, `callable.go`) are relations answering a question alone: the translator first
  asks the first model to pick one (`Session.Pick`: About, Card and the catalogue, a grammar of `none`
  or a listed call, `pickTries` 2), a bare call being a program whose rows bind its variables; a `none` is a
  Step, a pick a `Try` with `Mode` `call`, and anything else falls through to drafting. A pick is
  jaala's text under any FrontEnd, voted on with its own prompt and escalated past like a draft.
  `TestEvalCallable` is the pick arm (`testdata/eval_call.golden`), `TestEval` the drafting arm.
  Every piece of text jaala writes into a prompt is a `Prompts` field (`prompts.go`, #485) a pack
  replaces word for word, read through `Pack.prompts()` (nil-safe, field by field over
  `DefaultPrompts`); a new piece of prompt text goes there too, with a test in `prompts_test.go`. Its
  templates (`templates()`) are checked by `Pack.Validate` and up front in `NewSession`.
  An `nl.ExtractSession` (`extract.go`, #502) is the same for a text's facts: `NewExtractSession` writes
  the prefix and picks `ExtractGrammar`, and an `ExtractConversation` per model judges each reply into
  an `ExtractVerdict` (the accepted `Extraction`, `Continue` with the refused line and `ExtractFix`, or
  `Stop` on a repeat or the last try), which `Translator.Extract` (`extract.go` in the translator) runs
  as a Runner turn's `TurnEnd` hook, `ExtractOptions.Fallback` becoming the `Config`'s fallback.
  An `nl.Session` (`session.go`, #496) is the question's half that is jaala's:
  `NewSession` writes the prefix, grounds and picks the grammar, `Act` runs an action, `Draw` and `Key`
  judge and key a vote's draw, `Pick` is the callables' first step, and a `Conversation` per model
  judges each reply into a `Verdict` (the accepted `Program`, `Continue` with what to tell the model,
  or `Stop`), shaped like chakra's `TurnEndVerdict` without importing it. `Conversation.Act` counts
  the actions (`AskedAgain`, `LastLook`), and `Session.Tools` describes them for a loop offering them
  as tools (`Actions.AsTools`, `Prompts.ToolLook` and its siblings). A change to how a reply is judged
  goes in `session.go`. Each turn's `RunnerConfig` is chakra's `presets.GraphQuery` (#528, `loop.preset`):
  `Check` the judging hook, `StuckKey` `actionKey`, `StillStuck` `EndActions`, `RepeatMax` -1 (the
  Conversation answers repeats), `Penalties` empty unless the role sets some (not the preset's DRY),
  `presets.Replace` putting jaala's `actions` stage in `StageActions`' place, and compaction and the
  label (`jaala.translator`) appended. `nlmodels/translator`'s `Ask` runs the Session on
  chakra Runners (`loop.go`, #496): a turn per model, `Conversation.Judge` as its `TurnEnd` hook
  (`Continue` with `AsUser`), the actions a `FuncSource` that a request stage drops once `MaxSteps` are
  used, compaction a turn stage (chakra's guided finalizing call reads the working messages, never a
  request stage's), a vote an `agent.Consensus` with the accepted answer as its `Prior` and the
  conversation flattened to text (`answered`), the time budget checked in each stage and hook, and a
  Try's tokens what the turn spent since the last Try or Step. Before `nl.Translate` went, #500's
  differential held the Runner to it over every eval script and arms for actions, votes and escalation,
  outcome for outcome and request for request; its tests then moved to `nlmodels/translator` with a
  shim (`translate`, `internal/modeltest`: a test `Model`, what `nl.Model` was until #502, as a chakra
  Provider, its requests read back as `Prompt`s (a system note keeping its role), a text action played as a tool call when the request offers that tool, chakra's
  guided finalizing call answered without asking the model), and their goldens didn't move.
  `differential_test.go` keeps its arms as path checks, `loop_test.go` what no moved test sees (tokens,
  the budget, tools dropped), and `server_test.go` runs the loop on selfcheck's `llama-server`, whose
  1 MB model caps a slot at 2048 tokens, so its vocabulary leaves out stdlib. Cypher's translator tests
  and its `TestEval` are `nlmodels/cypherarm`'s, so nlmodels requires the cypher module (its tests
  only), and `internal/guide.CypherDrafts` feeds both that eval and cypher's `FuzzCompile` seeds.
- **`nlmodels/memory` is #415's LongMemEval memory** on chakra's `eval/longmemeval` (v0.3.6). A
  `Memory` is a chakra `SessionMemory`. Each session goes through `Translator.Extract` and is committed
  to an `ns.TimedMemSource` as the next version. `fact`'s key (subject, attribute) makes a later
  statement replace an earlier one, and `At(v)` still reads the old one. `cited` turns Extract's
  `text:start-end` citations into the session id and the quoted words, since a row read across
  sessions has to say which one. Its tools (`query`, `read_session`) call no model, so chakra's
  `MemoryArm` counts all of the agent's spend. `fact` and `event` hold both speakers' statements,
  and `cited` drops `session` lines the model writes, since `Ingest` writes the row (#563). `OneShotArm` runs `Ask` in setup and puts the cited rows
  in the instructions. Its text is the package's `Prompts`. `TestPilot` (tag `eval_llm`, opt-in) runs
  it against the long-context arm with the benchmark's judge.
- **Extract lists held keys and refuses a cut reply** (#563). With `ExtractOptions.Base` and
  `Pack.Keys`, `heldKeys` evaluates each keyed relation for its key values, in the answer's order and
  at most `HeldMax` (200), and `Ask` puts them after the text under `Prompts.ExtractHeld`, so the
  prefix stays the same per text. `NewExtractSession` reads them with `context.Background()`, since
  its signature has no context. A `Try.Truncated` reply is `KindTruncated` before any reading, never
  in `refused` (the same text uncut is fine), and the last one stops with `ErrTruncated`.
- **`nl.Grammar()` is GBNF built from a rule table** (`frontends/nl/grammar.go`, #323), and every text it
  accepts must parse: `TestEveryProgramTheGrammarWritesParses` samples 21,000 programs from the table
  over 70 seeds and every depth (one seed alone missed `-(9)` as a column). The other direction lives
  in `datalog`'s tests, which can reach the corpus: they read `frontends/nl/testdata/program.gbnf` (`go
  test -update .` in `frontends/nl` writes it) with a small Earley recognizer and require every corpus, fuzz-seed and docs
  program, printed in jaala's spelling, to be accepted. A grammar change regenerates the file, or
  `datalog` checks the old one. The card is memoized on the vocabulary (`Memo`). A name is capped at
  `nameMax` (32) characters (#340: a model writing prose ran one to its token limit), written as
  GBNF's `{m,n}`, which `internal/gbnf` expands into nested optionals: flat `x? x?` made Earley five
  times slower.
- **`nl.GrammarFor(v)` is that table with the calls replaced by v's members** (`vocabgrammar.go`,
  #324): each at the arities it takes, a closed domain's argument as a variable or one of its values,
  and a program's own rules named by `local`, a trie keeping them off every root name, whose tails (`lt<n>`) keep a local within `nameMax`. `GoalOnly()`
  drops rules. Without it, `membersOnly` copies each rule reaching `local` with a `-m` suffix and no
  `local`, so a program with no rules calls members only (#567: prose read as a call to a rule). `local`
  starts lowercase (#571: prose opened a sentence as a rule head). Its soundness test also checks each sample against `Grammar()`, so it stays a
  sublanguage. The recognizer lives in `internal/gbnf` for both packages' tests, beside the table
  types, `Render` and the `Sampler` (#354, shared with the Cypher grammar; nl keeps its own short
  types and converts with `table`, since vet refuses unkeyed literals of another package's structs,
  and `program.gbnf` must come out byte-identical). The translator holds
  each answer to it (`ResponseGuided`) when the provider takes a grammar.
- **`frontends/nlmodels` is a module of its own** (#330), and jaala keeps no model client of its
  own: it asks any chakra `Provider` (#487). llama.cpp and Ollama are chakra's presets
  (`agent.NewLlamaCppProvider`, `agent.NewOllamaProvider`, chakra#180, v0.2.2); the grammar and a
  vote's seed go only where `agent.CapabilitiesOf` says the provider can (llama.cpp both, Ollama a
  seed, Anthropic neither) and a temperature only where it says `Temperature`, which is 0 by default,
  since chakra omits a nil one and llama-server then samples at 0.8. Its `translator`
  package (#490) is the entry point a host calls (`translator.New(v, pack, cfg)`, `Ask`): jaala owns
  each role's tuning (temperature 0 and a fallback's 0.7, 512 tokens, a `Thinking` model's 3072, a `Reasoning` one's 16384
  (#549: gpt-5.5's hidden reasoning counts against the cap, and chakra hands a cut-off reply on as the
  answer; `Try.Truncated` marks a call that reached it, `TestEvalOnOpenAI` is the opt-in live check), no
  grammar, a think line and its last fenced block), a `Role` overrides it, and a temperature goes only to a provider whose
  `CapabilitiesOf` says it takes one (chakra#191, v0.2.4). A `Role` with `TextActions` (#509) is for a
  model that can't call tools (Qwen2.5-Coder under `--jinja` writes a call as JSON text): it gets no
  tools, `Session.TextActions`' prefix and grammar, its replies read with `nl.ReadAction` in the
  `TurnEnd` hook, and once its steps run out the request stage sets `StepView.Guided` to
  `Session.FinalGrammar` (chakra#259, v0.2.9): the draft's, a `try⏎` line allowed before it and dropped by
  `Session.reply` (#512: under the draft's grammar alone `try` ran into the first name). A repeated action
  isn't run and takes no step (`Conversation.asked`, `Repeats`, #517), and the translator marks it a
  free result (the tool returns chakra's `FreeResultError`, v0.3.2, read as the call's result;
  `TurnEndVerdict.Free` for a text reply), the `MaxRepeats`-th ending the actions, with
  `MaxFreeRefusals` the same. chakra's `NewRepeatCache` isn't used: the Conversation also serves text
  roles, ends the actions at the cap, and repeats an add only when its literal is in the body.
  `Role.Penalties` go out filtered by `Capabilities.Sendable`; chakra's `NewStuckDetector` (v0.3.0) runs first among
  the request stages, keyed by `actionKey` (a tool call, or a text reply read with `ReadAction`), and
  sends `Prompts.Stuck` at the fallback's temperature on the second repeat or alternation, then
  `Conversation.EndActions`. A `look` naming nothing lists members mentioning the word (`lookFor`), else
  answers as `find`; `nl.Look`, mcp's tool, is unchanged. jaala's agent loop runs on chakra (#496),
  jaala the Session a chakra agent plugs in, never a wrapper over it. `Translate(ctx, q, o, model,
  fallback)` is `Ask` under per-call options and roles, which is `mcp.Translator`'s method.
  CI's `nlmodels` job tests it; the root's `./...` doesn't reach it. Like every
  sub-module it requires jaala and `frontends/nl` at the last release and replaces both with the
  checkout (Releasing). A consumer must not pair the nl module with a jaala tag up to v0.1.71,
  which holds `frontends/nl` as a package: that is an ambiguous import. Its old path, `github.com/panyam/jaala/nlmodels`, shipped
  once (nlmodels/v0.1.65) with no consumer, and nothing is tagged under that prefix again. `./selfcheck.sh` runs a real
  `llama-server` on llama.cpp's 1 MB test model when `make llama` has fetched it (Homebrew's
  llama.cpp ships no GBNF validator, so a server is the only check llama.cpp reads the grammar): every reply must
  stay inside the grammar as `internal/gbnf` reads it. The model lives outside the checkout, at
  `~/.cache/jaala/stories260K.gguf` (`$XDG_CACHE_HOME` if set, or `JAALA_LLAMA_MODEL`), so every
  worktree and clone uses the one copy; without it the step reports "could not run". `llama-server` exits with "Abort
  trap: 6" when selfcheck's cleanup kills it, after its test, which is not a failure.
- **A model can write another language than jaala's** (`nl.FrontEnd`, `Options.FrontEnd`, #354): the
  prompt is the front end's card, instructions and examples (the pack's About and Card still follow,
  its jaala examples don't), a draft is compiled (`Compiled`, whose `Explain` is set even on a
  failed compile) before `Validate`, every refusal goes back through `Compiled.Explain`, and
  `Try.Language`, `Program.Language`/`Compiled` and `Report.Language()` ("passed n/m in cypher")
  record it. The interface is nl's and `frontends/cypher` implements it (`NewFrontEnd`), so cypher
  imports nl and never the other way. `cypher.Grammar(g)` is the subset as a gbnf table narrowed
  to g's labels, types and keys, without CALL or `$parameters` (XOR came back once #377 made a
  chain of them compile in linear time); its test holds every sample to parsing, not compiling (a random query names
  variables nothing matched), and requires the guide's real answers to be accepted instead.
- **A name added to stdlib moves more than stdlib's tests** (#360). The plain-language card lists every
  member, so `frontends/nl`'s `card.golden`, `nlmodels/translator`'s `eval.golden` (`-update`) and the docsite's
  `Example_card` (`examples/guide/plainlang_test.go`, edited by hand) change, as do the two datalog
  tests listing `str`'s members (`checkerror_test.go`, `namespace_test.go`). `TestEveryBuiltinHasADemo`
  wants a `demos/reference/builtins-*.yaml` calling it, and a new error message the catalogue. Each line
  is prompt cost for every translation (#360's six took `TestEval` from 10,401 tokens to
  14,190), so keep a builtin's `Doc` short. A text function is a relation with its result last, like
  `num.*`, so expressions take it with no engine change; an absent input gives no row. An argument taking
  values its enumeration never lists (`str.split`'s and `str.find`'s negative index) is marked
  `ns.Builtin.Aliased` (#480): `extendBuiltin` passes it bound only when the atom writes a constant,
  so a value a join or the demand rewrite binds is matched against the listed rows. When a test
  marks a value unbound by comparing with `ns.Value{}`, `ns.S("")` is that value too.
