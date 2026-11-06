# Build all servers and tools into ./build (gitignored).
SERVERS := imsg-mcp grok-mcp codex-mcp sitecheck
PREFIX  ?= $(HOME)/.local/bin
SKILLS_DIR ?= $(HOME)/.claude/skills

.PHONY: build test vet install skills clean $(SERVERS)

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

# Symlink each skills/<name> into ~/.claude/skills so Claude Code picks it up;
# edits in the repo take effect immediately.
skills:
	mkdir -p $(SKILLS_DIR)
	for s in skills/*/; do n=$$(basename $$s); ln -sfn "$(CURDIR)/skills/$$n" "$(SKILLS_DIR)/$$n"; echo "linked $$n"; done

clean:
	rm -rf build
