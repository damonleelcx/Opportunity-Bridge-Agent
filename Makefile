# Opportunity Bridge Agent
#
# GOWORK=off keeps the build independent of any Go workspace the checkout
# happens to sit inside. Without it, cloning this repo under a directory that
# has its own go.work fails with an error that says nothing about this project.
export GOWORK := off

BIN      := bin
ADDR     ?= :8787
CORPUS   ?= data
STATE    ?= .state/oba.json
DEMO     ?= demo/scripted-turns.json

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | \
	  awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

.PHONY: env
env: ## Create .env from .env.example if it does not exist yet
	@if [ -f .env ]; then echo ".env already exists; leaving it alone"; else \
	  cp .env.example .env && chmod 600 .env && \
	  echo "created .env (mode 600) - fill in your key"; fi

.PHONY: build
build: ## Build both binaries into ./bin
	@mkdir -p $(BIN)
	go build -o $(BIN)/obagent ./cmd/obagent
	go build -o $(BIN)/obaeval ./cmd/obaeval

.PHONY: run
run: ## Run against the real Claude API (needs ANTHROPIC_API_KEY or `ant auth login`)
	OBA_ADDR=$(ADDR) OBA_CORPUS_DIR=$(CORPUS) OBA_STATE_PATH=$(STATE) go run ./cmd/obagent

.PHONY: run-deepseek
run-deepseek: ## Run against DeepSeek (needs DEEPSEEK_API_KEY); model ids follow the backend
	OBA_ADDR=$(ADDR) OBA_CORPUS_DIR=$(CORPUS) OBA_STATE_PATH=$(STATE) \
	  OBA_BACKEND=deepseek go run ./cmd/obagent

.PHONY: demo
demo: ## Run offline against the scripted backend - no API key, no network
	OBA_ADDR=$(ADDR) OBA_CORPUS_DIR=$(CORPUS) OBA_BACKEND=scripted OBA_SCRIPT=$(DEMO) \
	  go run ./cmd/obagent

.PHONY: test
test: ## Run every test, including the evaluation suite
	go test ./...
	@echo
	@echo "NOTE: the postgres backend was NOT covered by this run (its tests skip"
	@echo "      without a database). Run 'make test-pg' to cover it."

# Postgres tests need a real postgres, because a store tested against a
# stand-in proves that the stand-in works. This starts one, runs them, and
# leaves it running so a second run is fast; 'make test-pg-down' removes it.
#
# The port is chosen at run time rather than fixed. A fixed one was tried and
# collided with an unrelated ssh tunnel already listening on it, so the tests
# authenticated against somebody else's database and failed in a way that
# looked like a bug in this code.
PG_TEST_CONTAINER ?= oba-test-pg
PG_TEST_IMAGE     ?= postgres:17

.PHONY: test-pg
test-pg: ## Run the store tests against a real postgres in a container
	@port=$$(./scripts/free-port.sh) ; \
	 if [ -z "$$(docker ps -q -f name=^$(PG_TEST_CONTAINER)$$)" ]; then \
	   echo "starting $(PG_TEST_CONTAINER) on port $$port" ; \
	   docker rm -f $(PG_TEST_CONTAINER) >/dev/null 2>&1 || true ; \
	   docker run -d --name $(PG_TEST_CONTAINER) -e POSTGRES_PASSWORD=obatest \
	     -e POSTGRES_USER=oba -e POSTGRES_DB=oba -p $$port:5432 $(PG_TEST_IMAGE) >/dev/null ; \
	 else \
	   port=$$(docker port $(PG_TEST_CONTAINER) 5432/tcp | head -1 | sed 's/.*://') ; \
	   echo "reusing $(PG_TEST_CONTAINER) on port $$port" ; \
	 fi ; \
	 for i in $$(seq 1 40); do \
	   docker exec $(PG_TEST_CONTAINER) pg_isready -U oba -d oba >/dev/null 2>&1 && break ; \
	   sleep 1 ; \
	 done ; \
	 OBA_TEST_DATABASE_URL="postgres://oba:obatest@localhost:$$port/oba?sslmode=disable" \
	   go test ./internal/store/ -count=1

.PHONY: test-pg-down
test-pg-down: ## Remove the postgres test container
	docker rm -f $(PG_TEST_CONTAINER) >/dev/null 2>&1 || true

.PHONY: eval
eval: ## Run the evaluation datasets and print the reliability report
	go run ./cmd/obaeval

.PHONY: eval-live
eval-live: ## Run the evaluation suite with routing measured against the real classifier
	go run ./cmd/obaeval -live

.PHONY: eval-report
eval-report: ## Run the suite and write the full machine-readable report
	@mkdir -p $(BIN)
	go run ./cmd/obaeval -json $(BIN)/eval-report.json

.PHONY: check
check: ## fmt check, vet, test - what CI runs
	@test -z "$$(gofmt -l . | grep -v '^$$')" || { echo "gofmt needed:"; gofmt -l .; exit 1; }
	go vet ./...
	go test ./...

.PHONY: fmt
fmt: ## Format everything
	gofmt -w .

.PHONY: clean
clean: ## Remove build output and local state
	rm -rf $(BIN) .state
