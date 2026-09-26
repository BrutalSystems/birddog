# Run `make check` before every commit: it is exactly what CI runs.
.PHONY: check fmt vet test typecheck-plugin test-plugin build

# Everything CI gates on. It was Go-only once, so a green `make check` could
# still push a red build — the plugin's typecheck and suite ran nowhere local.
check: fmt vet test typecheck-plugin test-plugin

fmt:
	@test -z "$$(gofmt -l .)" || { echo "needs gofmt:"; gofmt -l .; exit 1; }

vet:
	@go vet ./...

test:
	@go test ./... -race

typecheck-plugin:
	@npm run --silent typecheck:plugin

test-plugin:
	@npm run --silent test

build:
	@go build -o bin/birddog ./cmd/birddog
