# Releasing light-tools

Releases are candidate-first and tag-last. Do not create or push a `v*` tag
manually: Go module proxies and npm versions are immutable, so a failed version
must be corrected with a higher version rather than deleted and reused.

The distribution set contains six native archives, six platform-specific npm
tarballs, one top-level npm tarball, and `checksums.txt`. The manifest has exactly
thirteen entries; the promoted GitHub release has exactly fourteen files.

## One-time environment setup

The promotion workflow targets the GitHub environment named `release`. Keep its
deployment branch policy restricted to `main`. The repository is private, so npm
trusted publishing works but npm provenance is not generated; do not claim an
attestation. Do not add `NPM_TOKEN` to GitHub.

Inspect and, if needed, apply [the environment seed](.github/release-environment.json):

```sh
gh api repos/icediceice/light-tools/environments/release
gh api --method PUT repos/icediceice/light-tools/environments/release \
  --input .github/release-environment.json
```

## 1. Build an unpublished candidate

Run the candidate workflow from `main`:

```sh
gh workflow run release.yml --ref main -f version=1.2.3
```

The run builds six native binaries and all thirteen payloads without creating a
tag or release. Each of the six `smoke-<os>_<arch>` jobs tests both paths:

- the checksum-verifying native installer;
- a script-disabled, offline npm install of the exact root and matching platform
  tarballs;
- byte equality between the npm native binary and the native archive;
- version, initialization, all five tools, image output, deterministic terse
  output, and TSX symbols;
- the documented no-grammar fallback on Windows ARM64.

CI separately proves the npm allowlist, omitted-optional error, symlinked global
launcher, process forwarding, and Alpine/musl refusal.

Wait for all six smoke jobs. Copy the run ID and the `checksums.txt SHA-256` from
the package summary. Candidate artifacts expire after seven days. Artifact names
include both run ID and attempt; do not use a failed-only candidate rerun because
successful upstream artifacts retain the old attempt name. Start a fresh run or
rerun all jobs.

## 2. Promote the exact candidate

For the one-time `0.1.0` bootstrap, promote with npm publishing disabled:

```sh
gh workflow run promote-release.yml --ref main \
  -f candidate_run_id=123456789 \
  -f version=0.1.0 \
  -f expected_checksums_sha256=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef \
  -f publish_npm=false
```

For normal releases, use the same command with `publish_npm=true`. Promotion
re-reads the Actions API, requires the exact successful manual candidate on
`main`, checks all six named smoke jobs, verifies the manifest digest, exact
filenames, and every checksum, then creates the tag and GitHub release.

The npm job runs only after GitHub promotion. It publishes six platform packages
first and the top-level package last. Stable versions use the `latest` dist-tag;
versions containing a prerelease suffix use `next`.

Before each publish, the job compares the release tarball's SHA-512 SRI with
`npm view <package>@<version> dist.integrity`. A byte-identical existing version
is a successful no-op; different bytes at the same immutable version are a hard
failure.

## 3. Bootstrap the npm namespace once

The seven packages must exist before trusted publishing can be attached. After
the promoted `0.1.0` release is complete:

1. Download only its seven npm tarballs and `checksums.txt` to the authenticated
   Mac mini. Verify the manifest SHA-256 and each tarball before publishing.
2. Publish these six platform packages first with `npm publish --access public
   --tag latest <tarball>`:
   - `@icediceice/light-tools-darwin-arm64`
   - `@icediceice/light-tools-darwin-x64`
   - `@icediceice/light-tools-linux-arm64`
   - `@icediceice/light-tools-linux-x64`
   - `@icediceice/light-tools-win32-arm64`
   - `@icediceice/light-tools-win32-x64`
3. Publish `@icediceice/light-tools` last from its exact promoted tarball.
4. With npm 11.19 or newer, create the publisher relationship for every package:

   ```sh
   npm trust github <package> --file promote-release.yml \
     --repo icediceice/light-tools --env release --allow-publish --yes
   ```

   If npm removes that command, configure the same GitHub Actions relationship
   in each package's npmjs.com Trusted Publishing settings. The workflow filename
   is case-sensitive and the environment is exactly `release`.
5. Build and promote `0.1.1-oidc.0` with `publish_npm=true`. This permanent
   prerelease is the proof that all seven OIDC bindings work. Confirm `next` points
   to it while `latest` remains `0.1.0`, then rerun the npm job once to prove the
   byte-identical no-op path.
6. Require 2FA and disallow token publication in each package's publishing
   settings where npm supports it, then run `npm logout` on the Mac.

Never print, copy, commit, upload, or record an npm credential during bootstrap.

## Recovery

- GitHub release succeeds and npm fails: rerun only the failed `publish-npm` job.
  Existing byte-identical packages are skipped and missing packages continue.
- Registry has different bytes for the same version: stop. Deprecate the bad
  version if appropriate and publish a corrected higher version.
- Platform packages publish but the root does not: rerun the npm job; root-last
  ordering prevents a public root package from pointing at missing native packages.
- Tag creation succeeds but GitHub release creation fails: do not delete or reuse
  the tag. Verify it still names the tested SHA and complete the same release with
  the original run-bound assets, or publish a corrected higher version.
- Prereleases are created with GitHub's prerelease flag, so `releases/latest` and
  both unpinned native installers continue to resolve the newest stable version.
- A prerelease was promoted accidentally: mark the GitHub release as a prerelease,
  do not move npm `latest`, keep prereleases on `next`, and deprecate the
  mistaken version if consumers need a warning.
- Trusted publishing must be revoked: use `npm trust list <package>` followed by
  `npm trust revoke <package> --id=<trust-id>` for every affected package, or use
  npmjs.com package settings.

Repository write access remains release capability because workflow changes can
change what is published. The release environment, exact-run binding, manifest,
platform smoke matrix, npm integrity comparison, and immutable-version rules
defend against stale, partial, red, or substituted artifacts.