VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
PKG     := github.com/Crocogiciel-Studio/podbox/internal/version
LDFLAGS := -s -w -X $(PKG).Version=$(VERSION) -X $(PKG).Commit=$(COMMIT)
GOBUILD := CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)"
PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64
EMBED_DIR := internal/images/bin

.PHONY: build test lint cross cross-embed e2e clean

build:
	$(GOBUILD) -o bin/podbox ./cmd/podbox

test:
	go test ./...

lint:
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi
	go vet ./...

cross:
	@for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; ext=; [ $$os = windows ] && ext=.exe; \
		echo "build $$os/$$arch"; \
		GOOS=$$os GOARCH=$$arch $(GOBUILD) -o dist/podbox-$$os-$$arch$$ext ./cmd/podbox || exit 1; \
	done

# Release builds: embed the linux proxy binaries so macOS/Windows hosts can
# build the proxy image without a registry.
cross-embed:
	@mkdir -p $(EMBED_DIR)
	GOOS=linux GOARCH=amd64 $(GOBUILD) -o $(EMBED_DIR)/podbox-linux-amd64 ./cmd/podbox
	GOOS=linux GOARCH=arm64 $(GOBUILD) -o $(EMBED_DIR)/podbox-linux-arm64 ./cmd/podbox
	@for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; ext=; [ $$os = windows ] && ext=.exe; \
		echo "build $$os/$$arch (embedproxy)"; \
		GOOS=$$os GOARCH=$$arch $(GOBUILD) -tags embedproxy -o dist/podbox-$$os-$$arch$$ext ./cmd/podbox || exit 1; \
	done

e2e: build
	PODBOX=$(CURDIR)/bin/podbox test/e2e/evasion.sh
	PODBOX=$(CURDIR)/bin/podbox test/e2e/lifecycle.sh
	PODBOX=$(CURDIR)/bin/podbox test/e2e/agentctx.sh
	PODBOX=$(CURDIR)/bin/podbox test/e2e/clone.sh
	PODBOX=$(CURDIR)/bin/podbox test/e2e/clonediff.sh

clean:
	rm -rf bin dist $(EMBED_DIR)
