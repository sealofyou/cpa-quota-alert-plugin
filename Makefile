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

# Pin the baseline x86-64 ISA for both Go and cgo C code. Hosts such as
# generic QEMU VPS CPUs lack AVX/BMI2, and newer distro GCC builds may emit
# them by default; CPA's preflight probe then dies with SIGILL.
build-shared:
	@mkdir -p dist
	CGO_ENABLED=1 GOOS=linux GOARCH=amd64 GOAMD64=v1 \
	CGO_CFLAGS='-O2 -g -march=x86-64 -mtune=generic' \
	go build \
		-buildmode=c-shared \
		-trimpath \
		-ldflags='-s -w -buildid=' \
		-o dist/cpa-quota-alert-plugin.so ./cmd/plugin
