# Build all servers into ./build (gitignored).
SERVERS := imsg-mcp grok-mcp codex-mcp
PREFIX  ?= $(HOME)/.local/bin

.PHONY: build test vet install clean $(SERVERS)

build: $(SERVERS)

$(SERVERS):
	go build -trimpath -ldflags "-s -w" -o build/$@ ./cmd/$@

test:
	go vet ./...
	go test ./...

# Copy the binaries somewhere stable so MCP configs don't point into the repo.
install: build
	mkdir -p $(PREFIX)
	cp $(addprefix build/,$(SERVERS)) $(PREFIX)/

clean:
	rm -rf build
