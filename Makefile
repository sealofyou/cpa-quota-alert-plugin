.PHONY: verify check-structure check-format

verify: check-structure check-format

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
	@if find . -name '*.go' -print -quit | grep -q .; then gofmt -w $$(find . -name '*.go' -print); fi