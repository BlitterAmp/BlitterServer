.PHONY: build run check fmt vet lint-api gen-check

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
	npx --yes @redocly/cli@latest lint api/openapi.yaml --config .redocly.yaml

gen-check:
	npx --yes openapi-typescript@latest api/openapi.yaml --output /dev/null
	go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@latest -generate types,client -package apiclient -o /dev/null api/openapi.yaml
