.PHONY: verify linux-release-gate check-structure check-format test vet race build-shared

verify: check-structure check-format test vet

linux-release-gate: verify race build-shared

check-structure:
	@test -f go.mod
	@test -f README.md
	@test -f README_CN.md
	@test -f LICENSE
	@test -f SECURITY.md
	@test -f THIRD_PARTY_NOTICES.md
	@test -f .github/workflows/ci.yml
	@test -f .github/workflows/release.yml
	@test -d docs/specs
	@test -d cmd/plugin
	@test -d internal/abi

check-format:
	@files=$$(find . -name '*.go' -print); \
	if [ -n "$$files" ]; then \
		unformatted=$$(gofmt -l $$files); \
		if [ -n "$$unformatted" ]; then \
			printf '%s\n' "$$unformatted"; \
			exit 1; \
		fi; \
	fi

test:
	go test ./...

vet:
	go vet ./...

race:
	CGO_ENABLED=1 go test -race ./...

build-shared:
	@mkdir -p dist
	CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build \
		-buildmode=c-shared \
		-trimpath \
		-ldflags='-s -w -buildid=' \
		-o dist/cpa-quota-alert-plugin.so ./cmd/plugin
