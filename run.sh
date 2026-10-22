#!/usr/bin/env bash
# Reproduces the go-spiffe lite bootstrap PoC end to end in a sandbox.
#
# It simulates github.com/spiffe/go-spiffe as a local bare repository, runs
# the branch flow proposed in the spiffe/go-spiffe#413 discussion (merge into a
# lite branch, fix the reference there, fast-forward main) with the agreed
# steady state applied (require on a released lite tag, no replace), then builds two
# consumers against the simulated upstream the way the Go toolchain resolves
# a real one (git insteadOf rewrite plus GOPRIVATE, isolated module cache).
#
# Nothing outside OUT is modified, and the Go module cache under ~/go is never
# used. The upstream repository is cloned from GitHub, or from a local clone
# when GO_SPIFFE_REPO points at one (faster, only read).
#
# Usage: [GO_SPIFFE_REPO=/path/to/go-spiffe] ./run.sh [OUT]
#        OUT defaults to ./out next to this script.
set -euo pipefail

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
OUT=${1:-"$HERE/out"}
GO_SPIFFE_SRC=${GO_SPIFFE_REPO:-https://github.com/spiffe/go-spiffe}
PR_BASE=e9973f6314a3fa0e36eb1f00fbfe37bdc1554b96   # v2.8.1, base of PR 413
PR_HEAD=5f336e348ed01e526a8f0fa614a36fbd7adaa677   # head of PR 413

step() { printf '\n==> %s\n' "$*"; }

rm -rf "$OUT/upstream.git" "$OUT/work" "$OUT/probe" "$OUT/consumer-lite" "$OUT/consumer-v2" "$OUT/logs"
mkdir -p "$OUT/env" "$OUT/gomodcache" "$OUT/logs"

# Environment that makes the Go toolchain fetch github.com/spiffe/go-spiffe
# from the simulated upstream, with a module cache that never touches ~/go.
cat > "$OUT/env/gitconfig" <<EOF
[url "file://$OUT/upstream.git"]
	insteadOf = https://github.com/spiffe/go-spiffe
EOF
sim_env() {
	GIT_CONFIG_GLOBAL="$OUT/env/gitconfig" GIT_CONFIG_NOSYSTEM=1 \
	GOPRIVATE=github.com/spiffe/go-spiffe GOMODCACHE="$OUT/gomodcache" GOFLAGS= \
	"$@"
}

step "1. Simulated upstream: main at the PR base, PR head merged as branch lite"
git clone --quiet --bare "$GO_SPIFFE_SRC" "$OUT/upstream.git"
git -C "$OUT/upstream.git" update-ref refs/heads/main "$PR_BASE"
git -C "$OUT/upstream.git" symbolic-ref HEAD refs/heads/main
git clone --quiet "$OUT/upstream.git" "$OUT/work"
git -C "$OUT/work" config user.name "PoC Maintainer"
git -C "$OUT/work" config user.email "poc@example.org"
git -C "$OUT/work" fetch --quiet https://github.com/spiffe/go-spiffe refs/pull/413/head
git -C "$OUT/work" branch lite "$PR_HEAD"
git -C "$OUT/work" push --quiet origin lite

step "2. Tag lite/v0.1.0 on the lite branch"
git -C "$OUT/work" tag -a lite/v0.1.0 -m "lite v0.1.0" "$PR_HEAD"
git -C "$OUT/work" push --quiet origin lite/v0.1.0

step "3. Negative check: a consumer pinning v2 at the PR head cannot build"
mkdir -p "$OUT/probe" && cd "$OUT/probe"
cat > go.mod <<'EOF'
module probe

go 1.24.0
EOF
cat > main.go <<'EOF'
package main

import (
	"fmt"

	"github.com/spiffe/go-spiffe/v2/spiffeid"
)

func main() { fmt.Println(spiffeid.RequireFromString("spiffe://example.org/w")) }
EOF
sim_env go get "github.com/spiffe/go-spiffe/v2@$PR_HEAD" >/dev/null 2>&1
if sim_env go mod tidy >"$OUT/logs/01-pin-before-fix.log" 2>&1; then
	echo "unexpected: go mod tidy succeeded"; exit 1
fi
grep -o 'unknown revision lite/v0.0.0' "$OUT/logs/01-pin-before-fix.log" | head -1 || { cat "$OUT/logs/01-pin-before-fix.log"; exit 1; }

step "4. Fix the reference on the lite branch (the follow-up PR), then fast-forward main"
cd "$OUT/work" && git checkout --quiet lite
git am --quiet "$HERE/fix-the-reference.patch"
sim_env env GOWORK=off go mod tidy
git diff --quiet || { echo "unexpected: tidy changed files beyond the patch"; git status --short; exit 1; }
sim_env env GOWORK=off go build ./...
make replace-check
git push --quiet origin lite
git push --quiet origin lite:main
git -C "$OUT/upstream.git" for-each-ref --format='%(refname:short) -> %(objectname:short)' refs/heads/main refs/heads/lite refs/tags/lite/v0.1.0

step "5. Motivating use case: a lite-only consumer with identity delivered as files"
mkdir -p "$OUT/consumer-lite" && cp "$HERE"/consumer-lite/*.go "$OUT/consumer-lite/" && cd "$OUT/consumer-lite"
printf 'module example.org/promlike\n\ngo 1.24.0\n' > go.mod
sim_env go get github.com/spiffe/go-spiffe/lite@v0.1.0 >/dev/null 2>&1
sim_env go mod tidy >/dev/null 2>&1
sim_env go run . | tee "$OUT/logs/02-consumer-lite-run.log"
echo "modules linked into the binary:"
sim_env go list -deps -f '{{if .Module}}  {{.Module.Path}}{{end}}' . | sort -u | tee "$OUT/logs/03-consumer-lite-build-modules.log"
sim_env go mod graph > "$OUT/logs/04-consumer-lite-mod-graph.log"
printf 'gRPC/protobuf edges in go mod graph: %s\n' "$(grep -ciE 'grpc|protobuf' "$OUT/logs/04-consumer-lite-mod-graph.log" || true)"

step "6. Contrast: an existing v2 consumer pinned at main now resolves, and interoperates with lite values"
mkdir -p "$OUT/consumer-v2" && cp "$HERE"/consumer-v2/*.go "$OUT/consumer-v2/" && cd "$OUT/consumer-v2"
printf 'module example.org/v2consumer\n\ngo 1.24.0\n' > go.mod
sim_env go get github.com/spiffe/go-spiffe/v2@main 2>&1 | grep added | tee "$OUT/logs/05-pin-v2-at-main.log"
sim_env go mod tidy >/dev/null 2>&1
sim_env go run . | tee -a "$OUT/logs/05-pin-v2-at-main.log"
printf 'gRPC/protobuf edges in go mod graph: %s\n' "$(sim_env go mod graph | grep -ciE 'grpc|protobuf' || true)"

step "7. History under this flow: renames are followed, blame stays attributed"
cd "$OUT/work"
git log --follow --oneline -- lite/spiffeid/id.go | tail -2
echo "commits on main whose go.mod still carries a replace directive:"
for c in $(git rev-list "$PR_BASE..origin/main"); do
	if git show "$c:go.mod" | grep -q '^replace'; then git log -1 --format='  %h %s' "$c"; fi
done

step "8. The PR's own CI checks at the fixed state"
make sync-check >/dev/null && echo "sync-check OK"
sim_env make lite-deps-check | tail -1
sim_env env GOWORK=off go build ./... && echo "GOWORK=off build OK"

printf '\nDone. Logs in %s/logs\n' "$OUT"
