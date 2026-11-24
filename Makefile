BINARY     := cellrift
CMD        := ./cmd/cellrift
BUILD_FLAGS := -trimpath -ldflags "-s -w"

EXAMPLES_DIR := docs/examples

# Matches the Go version pinned in the Dockerfile and CI — keep these in sync.
GO_VERSION := 1.26.11  # keep in sync with the go directive in go.mod

.PHONY: build test test-sched docker-test lint vet staticcheck vulncheck policy release-check rebuildexamples docker clean

build: lint
	go build $(BUILD_FLAGS) -o $(BINARY) $(CMD)

# test runs the suite twice on purpose. The second pass, test-sched, is the
# one that catches races between a test and a mock peer's own goroutine —
# see the comment on that target.
test: test-sched
	go test -race ./...

# test-sched runs the protocol suites with a single OS thread, which is the
# only reliable way to catch a test that asserts on what a mock peer
# OBSERVED without waiting for it. With GOMAXPROCS unset, a 16-core
# developer machine runs the mock's read goroutine on another core within
# microseconds and such a test always passes; a 2-core CI runner does not,
# and four of them failed on this repository's first push to GitHub while
# `make test` had been green for weeks. Pinning one thread forces the
# assertion to run before the mock's goroutine is ever scheduled, turning an
# occasional CI failure into a deterministic local one. The mocks' own
# WaitFor* accessors are the fix; see internal/protocols/common/mockwait.go.
test-sched:
	GOMAXPROCS=1 go test -race -count=1 ./internal/protocols/... ./cmd/...

# docker-test runs the test suite inside a clean golang:$(GO_VERSION) container,
# so results don't depend on whatever Go toolchain happens to be installed
# locally (the exact drift that broke `make lint` — see sctp.go history).
# Uses the glibc-based (non-alpine) image because -race needs cgo + a libc
# the race runtime supports; named volumes cache modules/build output
# across runs. GOFLAGS=-buildvcs=false avoids "dubious ownership" git
# errors from the bind-mounted repo — applies to the nested `go build`
# some cmd/cellrift tests shell out to, not just this top-level `go test`.
docker-test:
	docker run --rm \
		-v $(CURDIR):/src -w /src \
		-v cellrift-gomodcache:/go/pkg/mod \
		-v cellrift-gobuildcache:/root/.cache/go-build \
		-e GOFLAGS=-buildvcs=false \
		golang:$(GO_VERSION) go test -race ./...

lint: vet staticcheck

# policy runs all contribution policy checks locally.
# Set SKIP_CHANGELOG=1 to skip the changelog diff check.
policy:
	@bash scripts/check-tests.sh
	@bash scripts/check-synthetic-data.sh
	@bash scripts/check-changelog.sh
	@bash scripts/check-no-new-deps.sh

vet:
	go vet ./...

staticcheck:
	go tool staticcheck ./...

# vulncheck is not part of `lint`/`build` — it needs network access to the
# vulnerability DB and shouldn't block offline local development. Run it
# explicitly (and in CI) before a release.
vulncheck:
	go tool govulncheck ./...

release-check:
	goreleaser check

rebuildexamples:
	@mkdir -p $(EXAMPLES_DIR)/.bak
	@cp -f $(EXAMPLES_DIR)/terminal.txt $(EXAMPLES_DIR)/junit.xml $(EXAMPLES_DIR)/report.json \
		$(EXAMPLES_DIR)/.bak/ 2>/dev/null || true
	@if go test ./internal/reporter/ -run TestGolden -update-golden && \
	    go test ./internal/reporter/ -run TestGolden; then \
		rm -rf $(EXAMPLES_DIR)/.bak; \
		echo "Golden files regenerated in $(EXAMPLES_DIR)/"; \
	else \
		cp -f $(EXAMPLES_DIR)/.bak/* $(EXAMPLES_DIR)/ 2>/dev/null || true; \
		rm -rf $(EXAMPLES_DIR)/.bak; \
		echo "ERROR: regeneration failed, existing files preserved" >&2; \
		exit 1; \
	fi

docker:
	docker build -t cellrift:dev .

clean:
	rm -f $(BINARY)
