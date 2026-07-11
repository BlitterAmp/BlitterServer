.PHONY: build run check fmt vet lint-api gen-check generate

build:
	go build -ldflags "-X main.version=$$(git describe --tags --always --dirty)" -o dist/blittarr ./cmd/blittarr

run:
	go run ./cmd/blittarr

check: fmt vet
	go test ./...

fmt:
	@test -z "$$(gofmt -l .)" || (gofmt -l . && exit 1)

vet:
	go vet ./...

lint-api:
	npx --yes @redocly/cli@2.38.0 lint api/openapi.yaml --config .redocly.yaml

gen-check:
	npx --yes openapi-typescript@7.13.0 api/openapi.yaml --output /dev/null
	go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@latest -generate types,client -package apiclient -o /dev/null api/openapi.yaml

generate:
	go generate ./...
