# Builds tess with the web app embedded. `make` alone builds; `make help` lists targets.
# Needs Go and, for the web app, Node.js. On Windows, run it from Git Bash.

GO      ?= go
NPM     ?= npm
EXE     := $(shell $(GO) env GOEXE)
BIN     := tess$(EXE)
VERSION ?= dev
LDFLAGS := -s -w -X main.version=$(VERSION)

export CGO_ENABLED := 0

# Recursive wildcard, so no find is needed on Windows.
rwildcard = $(foreach d,$(wildcard $(1:=/*)),$(call rwildcard,$d,$2) $(filter $(subst *,%,$2),$d))

GO_SRC   := go.mod go.sum $(call rwildcard,cmd internal tile web,*.go)
WEB_SRC  := $(call rwildcard,web/src web/static,*) web/vite.config.ts web/tsconfig.json web/package.json
WEB_DEPS := web/node_modules/.tess-stamp
WEB_OUT  := web/build/app/index.html

.DEFAULT_GOAL := build
.PHONY: build web web-check go-only run test vet fmt deps check integration up down clean help

build: $(BIN) ## Build tess with the web app (default)

$(BIN): $(GO_SRC) $(WEB_OUT)
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $@ ./cmd/tess

go-only: ## Build tess without rebuilding the web app (no Node.js needed)
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/tess

web: $(WEB_OUT) ## Build the web app into web/build/app

$(WEB_DEPS): web/package.json web/package-lock.json
	cd web && $(NPM) ci --ignore-scripts
	touch $@

$(WEB_OUT): $(WEB_DEPS) $(WEB_SRC)
	cd web && $(NPM) run build

web-check: $(WEB_DEPS) ## Type-check the web app
	cd web && $(NPM) run check

run: build ## Build, then start tess serve
	./$(BIN) serve

test: ## Unit tests, no services needed
	$(GO) test ./...

vet: ## go vet, with and without the integration tag
	$(GO) vet ./...
	$(GO) vet -tags integration ./...

# Line endings are ignored, since a Windows checkout with core.autocrlf has CRLF that git
# strips on commit.
fmt: ## Fail if any Go file needs gofmt
	@bad=0; for f in $$(git ls-files -co --exclude-standard '*.go'); do \
		if [ -n "$$(tr -d '\r' < $$f | gofmt -l)" ]; then echo "needs gofmt: $$f"; bad=1; fi; \
	done; exit $$bad

deps: ## Check direct dependencies against design.md section 11
	scripts/check-deps.sh

check: fmt vet test deps web-check ## Everything CI checks, except integration

up: ## Start PostgreSQL, MySQL, and MariaDB in Docker
	docker compose up -d --wait

down: ## Stop the Docker databases
	docker compose down

integration: ## Integration tests; run `make up` first
	$(GO) test -tags integration ./internal/tiles/... ./internal/cli/... ./internal/tui/...

clean: ## Remove tess and the built web app
	rm -rf $(BIN) web/build/app web/.svelte-kit

help: ## List targets
	@grep -E '^[a-zA-Z_-]+:.*## ' $(MAKEFILE_LIST) | sed 's/:.*## /\t/' | sort
