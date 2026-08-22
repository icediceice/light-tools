import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { mkdir, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import {
  createPlatformManifest,
  createRootManifest,
  validateVersion,
} from "../scripts/build-npm-packages.mjs";

const rootPackage = JSON.parse(
  await readFile(new URL("../package.json", import.meta.url), "utf8"),
);

const platformNames = [
  "@icediceice/light-tools-darwin-arm64",
  "@icediceice/light-tools-darwin-x64",
  "@icediceice/light-tools-linux-arm64",
  "@icediceice/light-tools-linux-x64",
  "@icediceice/light-tools-win32-arm64",
  "@icediceice/light-tools-win32-x64",
];

test("the committed root manifest is inert and publish-safe", () => {
  assert.equal(rootPackage.name, "@icediceice/light-tools");
  assert.equal(rootPackage.version, "0.0.0-development");
  assert.deepEqual(rootPackage.bin, { "light-tools": "npm/cli.mjs" });
  assert.deepEqual(rootPackage.files, ["npm/cli.mjs", "npm/platform.mjs"]);
  assert.equal(rootPackage.engines.node, ">=18.17.0");
  assert.equal(rootPackage.engines.npm, ">=10.0.0");
  assert.equal(rootPackage.publishConfig.access, "public");
  assert.equal(rootPackage.scripts?.postinstall, undefined);
  assert.equal(rootPackage.dependencies, undefined);
  assert.deepEqual(Object.keys(rootPackage.optionalDependencies).sort(), platformNames);
  assert.ok(Object.values(rootPackage.optionalDependencies).every(
    (version) => version === rootPackage.version,
  ));
});

test("candidate root manifests pin every optional package to the release version", () => {
  const manifest = createRootManifest(rootPackage, "1.2.3-rc.4");
  assert.equal(manifest.version, "1.2.3-rc.4");
  assert.deepEqual(Object.keys(manifest.optionalDependencies).sort(), platformNames);
  assert.ok(Object.values(manifest.optionalDependencies).every(
    (version) => version === "1.2.3-rc.4",
  ));
  assert.equal(manifest.scripts?.postinstall, undefined);
});

test("platform manifests carry npm compatibility gates and no executable shim", () => {
  const linux = createPlatformManifest("linux-x64", "1.2.3");
  assert.equal(linux.name, "@icediceice/light-tools-linux-x64");
  assert.deepEqual(linux.os, ["linux"]);
  assert.deepEqual(linux.cpu, ["x64"]);
  assert.deepEqual(linux.libc, ["glibc"]);
  assert.equal(linux.engines.npm, ">=10.0.0");
  assert.deepEqual(linux.files, ["bin/light-tools"]);
  assert.equal(linux.bin, undefined);
  assert.equal(linux.scripts, undefined);

  const windows = createPlatformManifest("win32-arm64", "1.2.3");
  assert.deepEqual(windows.os, ["win32"]);
  assert.deepEqual(windows.cpu, ["arm64"]);
  assert.equal(windows.libc, undefined);
  assert.deepEqual(windows.files, ["bin/light-tools.exe"]);
});

test("glibc platform packages are rejected on musl instead of failing at first run", {
  skip: process.platform !== "linux" ||
    Boolean(process.report.getReport().header.glibcVersionRuntime),
}, async () => {
  const root = await mkdtemp(join(tmpdir(), "light-tools-musl-"));
  try {
    const key = `linux-${process.arch}`;
    const staging = join(root, "package");
    const binaryDirectory = join(staging, "bin");
    await mkdir(binaryDirectory, { recursive: true });
    await writeFile(
      join(staging, "package.json"),
      `${JSON.stringify(createPlatformManifest(key, "1.2.3"), null, 2)}\n`,
      "utf8",
    );
    await writeFile(join(binaryDirectory, "light-tools"), "not-an-executable", "utf8");
    const packed = spawnSync(
      "npm",
      ["pack", staging, "--pack-destination", root, "--json"],
      { encoding: "utf8" },
    );
    assert.equal(packed.status, 0, packed.stderr);
    const [{ filename }] = JSON.parse(packed.stdout);
    const installed = spawnSync(
      "npm",
      [
        "install",
        "--global",
        "--ignore-scripts",
        "--prefix",
        join(root, "prefix"),
        join(root, filename),
      ],
      { encoding: "utf8" },
    );
    assert.notEqual(installed.status, 0);
    assert.match(
      `${installed.stdout}\n${installed.stderr}`,
      /EBADPLATFORM|Unsupported platform|libc/i,
    );
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test("release versions accept SemVer releases and reject path-like input", () => {
  for (const version of ["0.1.0", "0.1.1-oidc.0", "12.34.56-rc.1"]) {
    assert.equal(validateVersion(version), version);
  }
  for (const version of ["v0.1.0", "../0.1.0", "0.1", "latest", "1.2.3/asset"]) {
    assert.throws(() => validateVersion(version), /invalid release version/i);
  }
});