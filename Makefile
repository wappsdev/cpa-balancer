PLUGIN_DIR := $(HOME)/.cli-proxy-api/plugins/darwin/arm64

.PHONY: test proof build build-import install

test:
	node --check internal/web/assets/app.js
	go vet ./...
	go test ./...
	node internal/web/session_test.mjs
	node internal/web/outlook_test.mjs
	node internal/web/sessions_test.mjs
	node internal/web/affinity_test.mjs
	node internal/web/grouped_test.mjs
	node internal/web/accountuse_test.mjs
	node internal/web/transcripts_test.mjs
	node internal/web/extramodels_test.mjs
	node internal/web/fill_test.mjs
	node internal/web/advisor_test.mjs

# Runs tools/sdkproof against the CPA host code (a scratch copy of the module): what the extra
# models rely on. Slower than test; run it after changing extra models or the CPA version.
proof:
	tools/sdkproof/run.sh

build:
	go build -buildmode=c-shared -o dist/cpa-balancer.dylib ./cmd/cpa-balancer
	rm -f dist/cpa-balancer.h

build-import:
	go build -o dist/cpa-balancer-import ./cmd/cpa-balancer-import

# CPA loads the library at startup; restart it after installing.
install: build
	mkdir -p $(PLUGIN_DIR)
	cp dist/cpa-balancer.dylib $(PLUGIN_DIR)/cpa-balancer.dylib
