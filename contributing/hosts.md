# Working with hosts

agni and Declaire: upgrade notes, running a host's suite against a branch, and reproducing a host's ask.

A field added to a struct a host implements against (`ns.Conjunction`) is ignored silently by every
host built before it, so behaviour that depends on the host acting on it is opt-in through a new
optional interface (`ns.FilterSource`, #297): Declaire's store accepted filters it didn't apply, and
groups grown past them joined every row the filter would drop.

Most of what a host derives while ingesting stays in Go however much jaala grows (a survey of both on
2026-10-18): parsing, type resolution (go/types, SCIP), minting identities, and anything ordered.
Declaire's linkers are rules waiting on nothing in jaala since v0.1.71 (declaire#205), and moving them
retires its hand-built linker store. agni's net roles and part classes could be rules too, but some 90
Go rules, guards, trace and rendering read them off `check.Model`, so a rule there duplicates the Go
until agni#1061 settles the model's shape. Write statements (#404, compiling into #287's change sets) add edits; they replace no ingestion.

agni (github.com/panyam/agni) is the first host and Declaire (github.com/panyam/declaire) the
second. Cross-repo work is split by repo: jaala issues are worked from jaala sessions, host issues
from host sessions. A host that needs something jaala lacks files a jaala issue rather than working
around it, and a release that breaks hosts gets an upgrade note on the issue they filed, with the
exact lines each must change (as #7's v0.1.6 comment did for `Modes`).

To see what a change does to a host before writing its upgrade note, run the host's own suite
against `main` and against the branch through a scratch `go.work` (`use` the host checkout and a
jaala worktree), and diff the failures: a failure in both is the host's or its environment's, one in
the branch only is this change's (#161, #168). Diff the whole sorted list of failing test names
(`grep '^--- FAIL' | sed 's/ (.*//' | sort -u`), never a `head` of it: a truncated list reported 9
of agni's 13 environmental failures for #196. A host checkout pins an older tag, so a copy may need
a small patch to build against `main` (agni's `Bind` call, for #132); patch a copy in the scratchpad,
never the host's own checkout. With no local checkout, `gh repo clone panyam/agni -- --depth 1`
into the scratchpad. Run the suite before believing what a doc says a host does: #205 assumed agni
normalizes units upstream (its C24), but agni serves board geometry in `mm` and reads bare
thresholds in it, so folding changed two of its answers (upgrade note on agni#1035). agni moved to
metres (agni#1036) before #303 made the fold opt-in, so a host's own migration can outrun jaala's
fix: check its `go.mod` and recent PRs before writing it an upgrade note.

Reproduce a host's ask before building it: Declaire's run asked for "a Fix for a qualified rule
head", which v0.1.65 already had; the real gap (#341) was the error after the model followed it,
since the unknown-name fix never read the program's own rules. Feeding the model's likely next
draft through `Validate` and `nl.Explain` in a scratch module shows which message it actually gets.

To find why a host's question is slow, run it alone: Declaire's `bench -dir <dir> -engine stores
-time` over a directory holding its `workspace.txtpb` and only that question (#290 did this for x04).
Declaire's `Runner` passes `Explain` only when its context carries a `*datalog.Report`, so reading
which relations were demanded and where the time went needs a patched copy of both repos in the
scratchpad, joined by a `go.work`; the Explain text (`demand [b], 7205 demanded`) then names the
rewrite to blame. Run the whole `bench/external` before and after a planner or push change, not only
the question it targets: #297's first cut sped nothing up and tripled x04's fetched rows, which only
the counts showed. Read `fetched`, `work` and `pushes` rather than times while anything else runs
(a background selfcheck took v0.1.56's x04 from 14.6s to 39s); they are exact. A question failing on
both engines and on older tags can still be jaala's: x06b was #295's deadlock, fixed by v0.1.56. A
before/after needs no patched copy when Explain isn't wanted: build `./cmd/declaire` from Declaire's
checkout twice, with `GOWORK` naming a scratch `go.work` that `use`s Declaire and a jaala checkout
plus its four `frontends/` modules (`main`'s, then the branch's worktree). Its bench cache
(`~/Library/Caches/declaire/bench`) is warm, and `-repos` narrows a run. bench/external's programs
aren't always the form agents write (x05's isn't, #402), so measure that form as a scratch question
beside a copy of `workspace.txtpb`.
