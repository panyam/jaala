# Developer setup and release. Every recipe calls a script in scripts/, so the shell stays out of
# make. The engine itself needs nothing but Go; Soufflé (#23) and llama.cpp (#330) are only for
# ./selfcheck.sh, and never a Go dependency.

.PHONY: setup souffle llama tag tag-push bump-root

setup: souffle llama

souffle: ## Install Soufflé 2.5, CI's version, for selfcheck's differential
	@./scripts/setup-souffle.sh

llama: ## Install llama-server and fetch its 1 MB test model, for selfcheck's grammar check
	@./scripts/setup-llama.sh

# =============================================================================
# Release (after mcpkit's): CLAUDE.md, Releasing, says when
# =============================================================================

# The sub-modules published with jaala, each tagged <dir>/vX at the same commit as jaala's vX. A
# release of some of them names them: make tag-push V=... SUB_MODS_TO_TAG=frontends/nl (none: jaala alone).
SUB_MODS_TO_TAG := frontends/nl frontends/nlmodels frontends/mcp frontends/cypher
export SUB_MODS_TO_TAG

tag: ## Tag jaala and its sub-modules at HEAD, the notes from NOTES (make tag V=v0.1.N NOTES=notes.txt)
	@V="$(V)" NOTES="$(NOTES)" ./scripts/tag-all.sh

tag-push: ## Tag, push and publish the release (make tag-push V=v0.1.N NOTES=notes.txt)
	@$(MAKE) tag V=$(V) NOTES=$(NOTES)
	@V="$(V)" ./scripts/tag-push.sh

bump-root: ## Point the sub-modules at a published jaala tag and drop their replace (make bump-root V=v0.1.N)
	@V="$(V)" ./scripts/bump-root.sh
