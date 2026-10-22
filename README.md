# go-spiffe lite bootstrap PoC

This PoC exercises, in a sandbox, the full path from spiffe/go-spiffe#413 to a
state that satisfies the plan in
https://github.com/spiffe/spiffe/issues/414#issuecomment-5058648605. It
follows the branch flow proposed in the PR discussion (merge the PR into a
`lite` branch, fix the lite reference there, fast-forward main) and applies
the steady state requested in that same discussion (the root go.mod always
requires a released lite tag, no replace directive, go.work for in-repo
development, and the GOWORK=off CI build acting as a real consumer check). It
then proves the use case that motivated the issue with a consumer that imports
only lite.

Nothing here touches the real repositories. `github.com/spiffe/go-spiffe` is
simulated as a local bare repository, and the Go toolchain is pointed at it
with a git `insteadOf` rewrite plus `GOPRIVATE`, using an isolated module
cache. That makes every consumer step resolve modules the way it would against
GitHub, including ignoring replace directives in dependency go.mod files.

## Layout

- `run.sh` reproduces everything from scratch into `out/` (or a directory
  passed as the first argument). It needs Go, git and network access. It
  clones go-spiffe from GitHub, or from a local clone when `GO_SPIFFE_REPO`
  points at one, and takes a couple of minutes:

  ```
  ./run.sh
  GO_SPIFFE_REPO=~/src/go-spiffe ./run.sh   # reuse a local clone
  ```

  The script exits non-zero if any expectation fails (the pre-fix consumer
  must fail, the fast-forward must land, the lite consumer must have no gRPC
  or protobuf in its graph, and the PR's checks must pass afterwards).
- `fix-the-reference.patch` is the commit that the follow-up PR against the
  lite branch would carry (see "The flow", step 4).
- `consumer-lite/` is the Prometheus-shaped workload. It imports only
  `github.com/spiffe/go-spiffe/lite/...`, receives its SVIDs and trust bundle
  as PEM files on disk, and does an mTLS round trip authorizing both peers by
  SPIFFE ID.
- `consumer-v2/` is the contrast case, an existing v2 importer pinned at the
  tip of main by pseudo-version, which also shows lite-built values flowing
  through the v2 alias API.
- `out/logs/` holds the captured evidence from the last run, and
  `out/run.log` the full script output.

## The flow, as it would run upstream

Steps 1, 2 and 5 are the branch flow from the PR discussion. Steps 3 and 4
are its "PR against lite to fix the reference", with the steady state applied.

1. Review PR 413 as if it were going to main. Once approved, change its base
   branch to a new upstream branch `lite` (created from main) and merge it
   there. Nothing depends on the lite module yet, and main is untouched.
2. Tag the lite module on the lite branch and push the tag:
   `git tag -a lite/v0.1.0 <merge commit> && git push origin lite/v0.1.0`.
   Go resolves tags independently of branches, so the tag is usable
   immediately even though the commit is not on main yet.
3. Open a PR against `lite` carrying `fix-the-reference.patch`: the require
   moves from `v0.0.0` to `v0.1.0`, the replace directive goes away, go.sum
   gains the lite entries, go.work documents the steady state, and a new
   `make replace-check` target (wired into the checks job) fails CI if a
   replace directive ever comes back. CI on that PR is the first time the
   `GOWORK=off go build ./...` job builds the way a consumer resolves the
   module, because the replace no longer hides the tag.
4. Merge that PR into `lite`.
5. Fast-forward main to the lite branch. This has to be a true fast-forward,
   so a maintainer pushes it directly (`git push origin lite:main`). GitHub's
   merge button cannot do that: a merge commit is unnecessary, and "rebase and
   merge" would rewrite the SHAs, leaving the tagged commit off main's
   first-parent history. Branch protection on main may need a temporary
   allowance for the push.

Going forward, the release order is lite first (`lite/vX.Y.Z`), then bump the
root require if v2 needs the new lite code, then tag v2. A lite API addition
that v2 wants to re-export needs a lite tag before the v2 alias can land,
which is cheap since path-prefixed tags cost nothing.

## Results

Step 3 of `run.sh`, pinning v2 at the PR head before the fix, fails with:

```
reading github.com/spiffe/go-spiffe/lite/go.mod at revision lite/v0.0.0: unknown revision lite/v0.0.0
```

That is the concrete form of the problem raised on the PR. `go get` alone
succeeds because it records the require without loading packages, so the
breakage surfaces at the next `go mod tidy` or `go build`, which is when a
real consumer would see it.

After the fix and the fast-forward, upstream has `main` and `lite` at the same
commit and `lite/v0.1.0` at the PR's merge commit. The lite-only consumer then
prints:

```
client received "hello over SPIFFE mTLS\n" and authenticated the server as spiffe://example.org/server
server authenticated the client as spiffe://example.org/client
```

Its binary links a single third-party module, `github.com/spiffe/go-spiffe/lite`
(the X.509 code paths do not even reach go-jose), and its `go mod graph` has
zero edges mentioning gRPC or protobuf. The full graph is lite, go-jose, and
the test-only testify chain (testify, go-spew, go-difflib, yaml.v3), which is
what the `lite-deps-check` target in the PR asserts for the importable
packages.

The v2 contrast consumer, pinned with `go get github.com/spiffe/go-spiffe/v2@main`,
resolves to a pseudo-version of the fast-forwarded tip and builds. Its graph
contains gRPC and protobuf, as expected for the full module. It assigns a
bundle created with `lite/bundle/x509bundle` to a `v2/bundle/x509bundle.Source`
without conversion, confirming that the alias layer keeps a single set of
types across both import paths, so a mixed dependency tree needs no
coordinated migration.

The PR's own checks pass at the fixed state (`sync-check`, `lite-deps-check`,
the GOWORK=off build), the new `replace-check` passes, and the lite tests pass
(the fix commit touches no Go code).

History under this flow is preserved. `git log --follow` on
`lite/spiffeid/id.go` walks back to the 2020 scaffolding commits, and
rename-aware blame attributes its lines to their original commits. A copy-first
split would have stopped both at the copy commit, which was the concern raised
on the PR.

## Caveats worth stating on the PR

- Only the branch tips are guaranteed resolvable. After the fast-forward, the
  PR's own commit (with the replace and the `v0.0.0` require) is reachable
  from main, and anyone pinning that specific SHA still fails. Step 7 of
  `run.sh` lists it. This seems acceptable, since pinning targets tips, but it
  is the trade-off against the copy-first split.
- The fast-forward requires that the lite branch contain main. Main has moved
  since the PR branched (v2.9.0 landed), so PR 413 needs a rebase or a merge
  from main before step 1, and nothing else may land on main between step 1
  and step 5, or the lite branch has to take main again before the push.
- Starting lite at `v0.1.0` leaves room to iterate on lite-only API such as
  the filesystem delivery implementation. Everything lite re-exports into v2
  is already frozen by v2's compatibility promise regardless of lite's major
  version, so `v1.0.0` becomes the honest signal once filesystem delivery
  settles.
- The Filesystem Delivery standard is still an open proposal
  (spiffe/spiffe#376), so this PoC models its consumption pattern with the
  existing file loaders (`x509svid.Load`, `x509bundle.Load`). Implementing the
  standard itself in lite is the follow-up the issue plan calls for, and it
  needs nothing beyond the lite module's current dependency set.

## Mapping to the plan in the issue comment

- Dependency-light module in the same repository: yes, `lite/` with go-jose
  as its only importable-package dependency.
- Core types in the light module, main module depends on it: yes, v2 requires
  lite `v0.1.0` and re-exports every symbol at the original import paths, so
  existing importers need no changes.
- Consumers like Prometheus import only the light module and never see gRPC:
  demonstrated by `consumer-lite/`.
- Path-prefixed tags and a small release process: `lite/v0.1.0`, release
  order documented in go.work, replace directives forbidden by CI.
- Filesystem delivery implementation in the light module: follow-up, pending
  the standard.
