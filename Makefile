GO_IMAGE ?= golang:1.27.2-alpine3.24
LINT_IMAGE ?= golangci/golangci-lint:v2.5.0
RUN_GO = docker run --rm -v $(CURDIR):/src -w /src -e GOFLAGS=-buildvcs=false -v acp-gocache:/go $(GO_IMAGE)

.PHONY: init lint test build up down

init: ## create .env and master key if missing
	@test -f .env || { cp .env.example .env; sed -i "s/^ACP_DB_PASSWORD=.*/ACP_DB_PASSWORD=$$(head -c 24 /dev/urandom | od -An -tx1 | tr -d ' \n')/" .env; chmod 600 .env; }
	@mkdir -p secrets
	@test -f secrets/master.key || { (umask 077; head -c 32 /dev/urandom | base64 > secrets/master.key); }
	@git config core.hooksPath scripts/hooks 2>/dev/null || true

lint:
	docker run --rm -v $(CURDIR):/src -w /src -v acp-gocache:/go $(LINT_IMAGE) golangci-lint run ./cmd/... ./internal/...

test:
	$(RUN_GO) sh -c 'go vet ./cmd/... ./internal/... && go test ./cmd/... ./internal/...'

build:
	docker compose build

up: init
	docker compose up -d --build

down:
	docker compose down
