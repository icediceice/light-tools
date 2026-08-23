# Releasing light-tools

Candidate-first, tag-last. Never create or push a `v*` tag by hand: Go module
proxies and npm versions are immutable, so a bad version is corrected with a
higher version, never deleted and reused.

The distribution set is six native archives, six platform npm tarballs, one
top-level npm tarball and `checksums.txt` — thirteen manifest entries, fourteen
files on the promoted release.

## Environment

The promotion workflow targets the GitHub environment `release`; keep its
deployment branch policy restricted to `main`. While the repository is private,
npm trusted publishing works but provenance is not generated — do not claim an
attestation. Once it is public, provenance is generated. Do not add `NPM_TOKEN`
to GitHub either way.

```sh
gh api repos/icediceice/light-tools/environments/release
gh api --method PUT repos/icediceice/light-tools/environments/release \
  --input .github/release-environment.json
```

## 1. Build a candidate

```sh
gh workflow run release.yml --ref main -f version=1.2.3
```

Builds all thirteen payloads with no tag and no release. Each of the six
`smoke-<os>_<arch>` jobs tests the checksum-verifying native installer, an
offline script-disabled npm install, byte equality between the npm binary and
the native archive, all five tools, terse output, and the Windows ARM64
no-grammar fallback.

Wait for all six. Copy the run ID and the `checksums.txt SHA-256` from the
package summary. Artifacts expire after seven days. Never promote a
failed-only rerun — successful upstream artifacts keep the old attempt name, so
start a fresh run or rerun every job.

## 2. Promote that exact candidate

```sh
gh workflow run promote-release.yml --ref main \
  -f candidate_run_id=123456789 \
  -f version=1.2.3 \
  -f expected_checksums_sha256=<sha> \
  -f publish_npm=true
```

Promotion re-reads the Actions API, requires the exact successful manual
candidate on `main`, checks all six smoke jobs, verifies the manifest digest,
filenames and every checksum, then tags and releases.

The npm job runs only after GitHub promotion: six platform packages first, the
top-level package last. Stable versions take `latest`; prerelease suffixes take
`next`. Before each publish it compares the tarball SHA-512 SRI against
`npm view <package>@<version> dist.integrity` — byte-identical is a no-op,
different bytes at the same version is a hard failure.

## Recovery

| Situation | Do |
| --- | --- |
| GitHub release ok, npm failed | Rerun only the failed `publish-npm` job |
| Registry has different bytes at that version | Stop. Deprecate if needed, publish a higher version |
| Platform packages published, root did not | Rerun the npm job; root-last ordering is why this is safe |
| Tag created, release creation failed | Do not delete or reuse the tag. Verify it names the tested SHA and finish with the original run-bound assets |
| Prerelease promoted by accident | Mark it prerelease, keep npm `latest` where it was, keep prereleases on `next` |
| Trusted publishing must be revoked | `npm trust list <package>`, then `npm trust revoke <package> --id=<trust-id>` |

Repository write access is release capability, because changing a workflow
changes what gets published.