# Build the snmpscan binary.
#
# Usage:
#   make                build for the host platform
#   make linux          cross-compile for linux/amd64
#   make install        install into $(PREFIX)/bin
#   make test           run go test ./...
#   make live           run the tests that need a real agent, e.g.
#                       make live AGENT=[2001:db8::1]:161 COMMUNITY=public
#   make clean          remove the built binary

BIN     := snmpscan
PREFIX  ?= /usr/local

GO      ?= go
GOFLAGS ?=

.PHONY: all
all: $(BIN)

$(BIN): $(shell find . -name '*.go') go.mod go.sum
	$(GO) build $(GOFLAGS) -o $@ ./cmd/snmpscan

.PHONY: linux
linux:
	GOOS=linux GOARCH=amd64 $(GO) build $(GOFLAGS) -o $(BIN) ./cmd/snmpscan

.PHONY: install
install: $(BIN)
	install -D -m 0755 $(BIN) $(DESTDIR)$(PREFIX)/bin/$(BIN)
	install -d -m 0755 $(DESTDIR)/etc/snmpscan
	install -m 0644 .snmpscan/*.device $(DESTDIR)/etc/snmpscan/

.PHONY: test
test:
	$(GO) test ./...

# Integration tests are skipped unless AGENT is set.
.PHONY: live
live:
	SNMPSCAN_TEST_AGENT=$(AGENT) SNMPSCAN_TEST_COMMUNITY=$(COMMUNITY) \
		$(GO) test ./internal/poll -run 'Live|Tuning|FilterPayoff|BulkWalk' -v -count=1 -timeout 600s

.PHONY: vet
vet:
	$(GO) vet ./...

.PHONY: fmt
fmt:
	gofmt -l -w .

.PHONY: clean
clean:
	rm -f $(BIN)
