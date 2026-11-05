GO_IMAGE ?= golang:1.27.2-alpine3.24
LINT_IMAGE ?= golangci/golangci-lint:v2.15.0
RUN_GO = docker run --rm -v $(CURDIR):/src -w /src -e GOFLAGS=-buildvcs=false -v acp-gocache:/go $(GO_IMAGE)
GO_TEST_DEPS = apk add --no-cache git openssh-client-default >/dev/null

.PHONY: init lint test test-db build up down sqlc

init: ## create .env and master key if missing
	@test -f .env || { cp .env.example .env; sed -i "s/^ACP_DB_PASSWORD=.*/ACP_DB_PASSWORD=$$(head -c 24 /dev/urandom | od -An -tx1 | tr -d ' \n')/" .env; chmod 600 .env; }
	@mkdir -p secrets
	@test -f secrets/master.key || { (umask 077; head -c 32 /dev/urandom | base64 > secrets/master.key); }
	@chown 10001:10001 secrets/master.key 2>/dev/null || true
	@test -f secrets/worker.token || { (umask 077; head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n' > secrets/worker.token); }
	@chown 10001:10001 secrets/worker.token 2>/dev/null || true
	@git config core.hooksPath scripts/hooks 2>/dev/null || true

lint:
	docker run --rm -v $(CURDIR):/src -w /src -v acp-gocache:/go $(LINT_IMAGE) golangci-lint run ./cmd/... ./internal/...

test:
	$(RUN_GO) sh -c '$(GO_TEST_DEPS) && go vet ./cmd/... ./internal/... && go test ./cmd/... ./internal/...'

test-db: ## DB-backed tests against a throwaway PostgreSQL (one database per package)
	./scripts/test-db.sh

build:
	docker compose build

up: init
	docker compose up -d --build

down:
	docker compose down

SQLC_IMAGE ?= sqlc/sqlc:1.30.0
sqlc: ## regenerate internal/db/dbgen from migrations and queries
	docker run --rm -v $(CURDIR):/src -w /src $(SQLC_IMAGE) generate
