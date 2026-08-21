# Releasing light-tools

Releases are tag-last. Do not create or push a `v*` tag manually: Go module
proxies can make a semantic-version tag available before GitHub Release checks
finish, and cached module versions cannot be safely reused.

## One-time approval setup

The `publish` job targets the GitHub environment named `release`. That
environment must require the `icediceice` user as a reviewer. Self-review stays
enabled because this repository uses a single-operator release model.

[`.github/release-environment.json`](.github/release-environment.json) is a
create-only seed for that policy. Before applying it, inspect the existing
environment:

```sh
gh repo view icediceice/light-tools --json visibility,isPrivate
gh api repos/icediceice/light-tools/environments/release
```

If the environment exists, verify its complete policy and do not overwrite
unknown branch restrictions or protection rules. If it does not exist, create it
once:

```sh
gh api --method PUT \
  repos/icediceice/light-tools/environments/release \
  --input .github/release-environment.json
```

Required reviewers for private repositories require GitHub Pro, Team, or
Enterprise. Do not enable publication unless the API response confirms the
required `icediceice` reviewer.

## Candidate proof

Pull requests that change release machinery run the complete candidate path:
six native builds, one exact package set, and six real-installer/MCP smokes.
Publication is impossible on a pull request.

After the workflow is on `main`, an operator can run another unpublished
candidate without creating a tag:

```sh
gh workflow run release.yml --ref main -f version=1.2.3 -f publish=false
```

A green candidate proves all six archives install with their exact checksum,
report the normalized version, initialize isolated state, expose the expected MCP
profiles, execute `light_file` and `light_bash`, enforce the workspace root,
and provide the documented symbol behavior.

## Publish

Start a release only from the tested commit on `main`:

```sh
gh workflow run release.yml --ref main -f version=1.2.3 -f publish=true
```

The workflow rebuilds and packages once, carries the checksum-manifest hash
through every smoke, and pauses at the `release` environment only after all six
native jobs pass. Approval lets the final job recheck that same hash, create
`v1.2.3` at the tested commit, and upload exactly the six archives plus
`checksums.txt`.

Before approval, canceling the run leaves no version tag or public release. Once
the tag is created, never delete and reuse that version. Publish a corrected
higher version; if the Go module must be hidden from normal selection, add a
`retract` directive and publish the retraction in a new version.
