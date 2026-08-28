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
