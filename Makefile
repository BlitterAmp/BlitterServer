.PHONY: build run check fmt vet lint-api

build:
	go build -o dist/blittarr ./cmd/blittarr

run:
	go run ./cmd/blittarr

check: fmt vet
	go test ./...

fmt:
	@test -z "$$(gofmt -l .)" || (gofmt -l . && exit 1)

vet:
	go vet ./...

lint-api:
	npx --yes @redocly/cli@latest lint api/openapi.yaml
