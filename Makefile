# Run `make check` before every commit: it is exactly what CI runs.
.PHONY: check fmt vet fixtures test typecheck-plugin test-plugin build

# Everything CI gates on. It was Go-only once, so a green `make check` could
# still push a red build — the plugin's typecheck and suite ran nowhere local.
check: fmt vet fixtures test typecheck-plugin test-plugin

# A test fixture that is ignored rather than tracked passes here and fails in
# CI, which checks out only what is tracked. That is exactly how a blanket
# *.jsonl rule hid the transcript format fixture through a 1.0.0 release
# attempt: green locally, red on the tag.
fixtures:
	@missing=$$(find . -path ./node_modules -prune -o -type d -name testdata -print | \
	  while read d; do \
	    for f in "$$d"/*; do \
	      [ -e "$$f" ] || continue; \
	      git ls-files --error-unmatch "$$f" >/dev/null 2>&1 || echo "$$f"; \
	    done; \
	  done); \
	[ -z "$$missing" ] || { echo "testdata files that git does not track:"; echo "$$missing"; exit 1; }

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
