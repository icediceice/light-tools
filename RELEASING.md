# Releasing light-tools

Releases are candidate-first and tag-last. Do not create or push a `v*` tag
manually: Go module proxies can make a semantic-version tag available immediately,
and cached module versions cannot be safely reused.

The process has two manual dispatches. The first builds and tests an unpublished
candidate. The second binds an operator's approval to one successful candidate run,
one version, and the SHA-256 of its `checksums.txt`.

## One-time environment setup

The promotion job targets the GitHub environment named `release`. Private
repositories on GitHub Pro can use deployment branch policies, but not required
reviewers. This environment intentionally has no reviewers; it restricts deployments
to `main` as an independent guard against dispatching promotion from a stale or
mistyped branch.

Inspect existing state before applying
[the seed](.github/release-environment.json):

```sh
gh api repos/icediceice/light-tools/environments/release
gh api repos/icediceice/light-tools/environments/release/deployment-branch-policies
```

Create or update the reviewer-free environment, then add the `main` policy if it
is absent:

```sh
gh api --method PUT \
  repos/icediceice/light-tools/environments/release \
  --input .github/release-environment.json

gh api --method POST \
  repos/icediceice/light-tools/environments/release/deployment-branch-policies \
  -f name=main -f type=branch
```

## 1. Build an unpublished candidate

Run the candidate workflow from `main`:

```sh
gh workflow run release.yml --ref main -f version=1.2.3
```

The run builds six native binaries, packages exactly six archives plus
`checksums.txt`, and installs and exercises each platform package through real MCP
calls. It creates no tag or GitHub release and has no write permission.

Wait for the entire run to succeed. Confirm these six exact jobs are green:

- `smoke-linux_amd64`
- `smoke-linux_arm64`
- `smoke-darwin_amd64`
- `smoke-darwin_arm64`
- `smoke-windows_amd64`
- `smoke-windows_arm64`

Copy the run ID and the `checksums.txt SHA-256` shown in the package job summary.
Candidate artifacts expire after seven days.

Artifact names include both run ID and attempt. Never use
`gh run rerun --failed` for a candidate because successful upstream jobs do not
re-upload artifacts under the new attempt number. Start a fresh candidate run, or
rerun all jobs.

## 2. Review and promote

Review the candidate's commit, six smoke results, version, and checksum digest.
Then dispatch promotion from `main`:

```sh
gh workflow run promote-release.yml --ref main \
  -f candidate_run_id=123456789 \
  -f version=1.2.3 \
  -f expected_checksums_sha256=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
```

Promotion independently asks the Actions API for the referenced run and exact
attempt. It requires a completed successful manual `release.yml` run on `main`,
a tested commit still reachable from `main`, and exactly the six named successful
smoke jobs. It downloads only the run-bound candidate, checks the typed manifest
digest, exact filenames, and every archive checksum, then atomically creates
`v1.2.3` at the tested commit. If another actor creates the tag first, promotion
fails instead of attaching assets to a foreign tag.

The final `gh release create --verify-tag` publishes exactly those six archives
and `checksums.txt`.

## Recovery and trust boundary

Canceling before atomic tag creation leaves no version tag or public release. If
tag creation succeeds but GitHub Release creation fails, do not delete or reuse the
tag. Verify that the tag still points to the tested candidate SHA and resume release
creation with `--verify-tag` using the same run-bound, checksum-verified assets.
Otherwise publish a corrected higher version. If the Go module must be hidden from
normal selection, add a `retract` directive and publish the retraction in a new
version.

This lifecycle defends against operator error and against publishing a red, partial,
stale, or substituted candidate. It is not a security boundary against a maintainer
who can edit workflows: write access to this repository is release capability. If
that threat matters, add a repository ruleset restricting creation of
`refs/tags/v*` after confirming the account plan supports it.