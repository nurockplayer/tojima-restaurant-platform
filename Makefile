.PHONY: test vet build fmt-check migration-smoke test-postgres ci

test:
	go test -race -count=1 ./...

vet:
	go vet ./...

build:
	go build ./...

fmt-check:
	@test -z "$$(gofmt -l $$(find cmd internal migrations -name '*.go' -type f))" || (gofmt -l $$(find cmd internal migrations -name '*.go' -type f); exit 1)

migration-smoke:
	python3 scripts/migration-smoke.py

test-postgres:
	sh scripts/test-postgres.sh

ci: fmt-check test vet build migration-smoke
