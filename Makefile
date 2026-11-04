PLUGIN_DIR := $(HOME)/.cli-proxy-api/plugins/darwin/arm64

.PHONY: test build install

test:
	go vet ./...
	go test ./...
	node internal/web/session_test.mjs
	node internal/web/outlook_test.mjs

build:
	go build -buildmode=c-shared -o dist/cpa-balancer.dylib ./cmd/cpa-balancer
	rm -f dist/cpa-balancer.h

# CPA loads the library at startup; restart it after installing.
install: build
	mkdir -p $(PLUGIN_DIR)
	cp dist/cpa-balancer.dylib $(PLUGIN_DIR)/cpa-balancer.dylib
