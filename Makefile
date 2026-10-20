# Developer setup and release. Every recipe calls a script in scripts/, so the shell stays out of
# make. The engine itself needs nothing but Go; Soufflé (#23) and llama.cpp (#330) are only for
# ./selfcheck.sh, and never a Go dependency.

.PHONY: setup souffle llama selfcheck selfcheck-status exercise-graph-server servers-up servers-down tag tag-push bump-root

setup: souffle llama

souffle: ## Install Soufflé 2.5, CI's version, for selfcheck's differential
	@./scripts/setup-souffle.sh

llama: ## Install llama-server and fetch its 1 MB test model, for selfcheck's grammar check
	@./scripts/setup-llama.sh

selfcheck: ## Run ./selfcheck.sh on a commit in its own worktree and note the result on it (make selfcheck REF=origin/main SEEDS=5000 FUZZ=30s CYPHER_FUZZ=2m STORES=neo4j,memgraph)
	@REF="$(REF)" SEEDS="$(SEEDS)" FUZZ="$(FUZZ)" CYPHER_FUZZ="$(CYPHER_FUZZ)" STORES="$(STORES)" NOPUSH="$(NOPUSH)" ./scripts/selfcheck-run.sh

servers-up: ## Start the graph-server pair every worktree shares (Neo4j on 17687, Memgraph on 17688), and print its URLs
	@./scripts/bolt-compose.sh up

servers-down: ## Remove the shared graph-server pair, and every namespace's graph with it
	@./scripts/bolt-compose.sh down

exercise-graph-server: ## The graph-server mission's exercise (#420) over the servers STORES names (make exercise-graph-server STORES=neo4j,memgraph)
	@STORES="$(STORES)" ./scripts/exercise-graph-server.sh

selfcheck-status: ## The last selfcheck pass on origin/main, any failure since, and whether a run is due (make selfcheck-status EVERY=5)
	@EVERY="$(EVERY)" ./scripts/selfcheck-status.sh

# =============================================================================
# Release (after mcpkit's): CLAUDE.md, Releasing, says when
# =============================================================================

# The sub-modules published with jaala, each tagged <dir>/vX at the same commit as jaala's vX. A
# release of some of them names them: make tag-push V=... SUB_MODS_TO_TAG=frontends/nl (none: jaala alone).
SUB_MODS_TO_TAG := frontends/nl frontends/nlmodels frontends/mcp frontends/cypher stores/bolt
export SUB_MODS_TO_TAG

tag: ## Tag jaala and its sub-modules at HEAD, the notes from NOTES (make tag V=v0.1.N NOTES=notes.txt)
	@V="$(V)" NOTES="$(NOTES)" ./scripts/tag-all.sh

tag-push: ## Tag, push and publish the release (make tag-push V=v0.1.N NOTES=notes.txt)
	@$(MAKE) tag V=$(V) NOTES=$(NOTES)
	@V="$(V)" ./scripts/tag-push.sh

bump-root: ## Point the sub-modules at the version about to be tagged, before make tag-push (make bump-root V=v0.1.N)
	@V="$(V)" ./scripts/bump-root.sh
