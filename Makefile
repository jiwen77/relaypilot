.PHONY: test syntax go-test smoke files build release-check

test: syntax go-test smoke files
	if git rev-parse --is-inside-work-tree >/dev/null 2>&1; then git diff --check; fi

syntax:
	bash -n relaypilot.sh install-relaypilot.sh scripts/smoke-agent.sh

go-test:
	@if command -v go >/dev/null 2>&1; then go test ./...; else echo "go not found; skipping Go tests"; fi

smoke:
	bash scripts/smoke-agent.sh

files:
	bash scripts/check-files.sh

build:
	go build -trimpath -ldflags "-s -w" -o dist/relaypilot ./cmd/relaypilot

release-check: test
	OUT_DIR=$$(mktemp -d /tmp/relaypilot-dist.XXXXXX) scripts/build-release.sh
