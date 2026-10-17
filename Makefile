.PHONY: test vet build fmt-check migration-smoke ci

test:
	go test -race ./...

vet:
	go vet ./...

build:
	go build ./...

fmt-check:
	@test -z "$$(gofmt -l $$(find cmd internal -name '*.go' -type f))" || (gofmt -l $$(find cmd internal -name '*.go' -type f); exit 1)

migration-smoke:
	python3 scripts/migration-smoke.py

ci: fmt-check test vet build migration-smoke
