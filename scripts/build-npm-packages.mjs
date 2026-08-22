#!/usr/bin/env node

import { spawnSync } from "node:child_process";
import { createHash } from "node:crypto";
import {
  chmod,
  copyFile,
  mkdir,
  mkdtemp,
  readFile,
  readdir,
  rm,
  stat,
  writeFile,
} from "node:fs/promises";
import { tmpdir } from "node:os";
import { basename, dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { PLATFORM_KEYS, PLATFORMS } from "../npm/platform.mjs";

const semverPattern = /^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-[0-9A-Za-z]+(?:[.-][0-9A-Za-z]+)*)?$/;

export function validateVersion(version) {
  if (typeof version !== "string" || !semverPattern.test(version)) {
    throw new Error(`invalid release version: ${String(version)}`);
  }
  return version;
}

export function createRootManifest(source, version) {
  validateVersion(version);
  const manifest = structuredClone(source);
  manifest.version = version;
  manifest.optionalDependencies = Object.fromEntries(
    Object.keys(source.optionalDependencies)
      .sort()
      .map((name) => [name, version]),
  );
  return manifest;
}

export function createPlatformManifest(key, version) {
  validateVersion(version);
  const definition = PLATFORMS[key];
  if (!definition) {
    throw new Error(`unknown npm platform key: ${key}`);
  }
  const manifest = {
    name: definition.packageName,
    version,
    description: `Native light-tools binary for ${definition.platform}/${definition.arch}.`,
    license: "Apache-2.0",
    repository: {
      type: "git",
      url: "git+https://github.com/icediceice/light-tools.git",
    },
    os: [definition.platform],
    cpu: [definition.arch],
    files: [`bin/${definition.executable}`],
    publishConfig: { access: "public" },
  };
  if (definition.libc) {
    manifest.libc = [definition.libc];
  }
  return manifest;
}

async function collectNamedFiles(root, expectedName) {
  const matches = [];
  async function visit(current) {
    for (const entry of await readdir(current, { withFileTypes: true })) {
      const candidate = join(current, entry.name);
      if (entry.isDirectory()) {
        await visit(candidate);
      } else if (entry.isFile() && entry.name === expectedName) {
        matches.push(candidate);
      }
    }
  }
  await visit(root);
  return matches.sort();
}

async function digest(path, algorithm) {
  const hash = createHash(algorithm);
  hash.update(await readFile(path));
  return hash.digest();
}

async function pack(stagingDirectory, outputDirectory) {
  const npm = process.platform === "win32" ? "npm.cmd" : "npm";
  const result = spawnSync(
    npm,
    ["pack", stagingDirectory, "--pack-destination", outputDirectory, "--json"],
    { encoding: "utf8", shell: false },
  );
  if (result.error) {
    throw result.error;
  }
  if (result.status !== 0) {
    throw new Error(
      `npm pack failed for ${stagingDirectory}: ${result.stderr.trim() || result.stdout.trim()}`,
    );
  }
  let parsed;
  try {
    parsed = JSON.parse(result.stdout);
  } catch (error) {
    throw new Error(`npm pack returned malformed JSON for ${stagingDirectory}`, {
      cause: error,
    });
  }
  if (!Array.isArray(parsed) || parsed.length !== 1 || !parsed[0].filename) {
    throw new Error(`npm pack returned an unexpected result for ${stagingDirectory}`);
  }
  return parsed[0];
}

async function writeJson(path, value) {
  await writeFile(path, `${JSON.stringify(value, null, 2)}\n`, "utf8");
}

async function stageSharedFiles(repoRoot, stagingDirectory) {
  await copyFile(join(repoRoot, "LICENSE"), join(stagingDirectory, "LICENSE"));
  await copyFile(join(repoRoot, "README.md"), join(stagingDirectory, "README.md"));
}

export async function buildPackages({
  version,
  artifactsDirectory,
  outputDirectory,
  repoRoot = resolve(dirname(fileURLToPath(import.meta.url)), ".."),
} = {}) {
  validateVersion(version);
  if (!artifactsDirectory || !outputDirectory) {
    throw new Error("artifactsDirectory and outputDirectory are required");
  }

  const artifactsRoot = resolve(artifactsDirectory);
  const outputRoot = resolve(outputDirectory);
  await mkdir(outputRoot, { recursive: true });
  const temporaryRoot = await mkdtemp(join(tmpdir(), "light-tools-npm-"));
  const packed = [];

  try {
    for (const key of PLATFORM_KEYS) {
      const definition = PLATFORMS[key];
      const sourceRoot = join(artifactsRoot, definition.artifactKey);
      const matches = await collectNamedFiles(sourceRoot, definition.executable);
      if (matches.length !== 1) {
        throw new Error(
          `expected one ${definition.executable} for ${definition.artifactKey}, found ${matches.length}`,
        );
      }

      const staging = join(temporaryRoot, key);
      const binaryDirectory = join(staging, "bin");
      await mkdir(binaryDirectory, { recursive: true });
      const stagedBinary = join(binaryDirectory, definition.executable);
      await copyFile(matches[0], stagedBinary);
      if (definition.platform !== "win32") {
        await chmod(stagedBinary, 0o755);
      }
      await stageSharedFiles(repoRoot, staging);
      await writeJson(
        join(staging, "package.json"),
        createPlatformManifest(key, version),
      );
      const result = await pack(staging, outputRoot);
      const tarball = join(outputRoot, result.filename);
      packed.push({
        key,
        packageName: definition.packageName,
        filename: result.filename,
        size: (await stat(tarball)).size,
        sha256: (await digest(tarball, "sha256")).toString("hex"),
        integrity: `sha512-${(await digest(tarball, "sha512")).toString("base64")}`,
        binarySha256: (await digest(stagedBinary, "sha256")).toString("hex"),
      });
    }

    const rootStaging = join(temporaryRoot, "root");
    await mkdir(join(rootStaging, "npm"), { recursive: true });
    await stageSharedFiles(repoRoot, rootStaging);
    await copyFile(join(repoRoot, "npm", "cli.mjs"), join(rootStaging, "npm", "cli.mjs"));
    await copyFile(join(repoRoot, "npm", "platform.mjs"), join(rootStaging, "npm", "platform.mjs"));
    const sourceManifest = JSON.parse(
      await readFile(join(repoRoot, "package.json"), "utf8"),
    );
    await writeJson(
      join(rootStaging, "package.json"),
      createRootManifest(sourceManifest, version),
    );
    const result = await pack(rootStaging, outputRoot);
    const tarball = join(outputRoot, result.filename);
    packed.push({
      key: "root",
      packageName: sourceManifest.name,
      filename: result.filename,
      size: (await stat(tarball)).size,
      sha256: (await digest(tarball, "sha256")).toString("hex"),
      integrity: `sha512-${(await digest(tarball, "sha512")).toString("base64")}`,
    });

    return packed;
  } finally {
    await rm(temporaryRoot, { recursive: true, force: true });
  }
}

function parseArguments(argv) {
  const options = {};
  for (let index = 0; index < argv.length; index += 2) {
    const name = argv[index];
    const value = argv[index + 1];
    if (!name?.startsWith("--") || value === undefined) {
      throw new Error(`invalid argument sequence near ${name ?? "<end>"}`);
    }
    options[name.slice(2)] = value;
  }
  return options;
}

export async function main(argv = process.argv.slice(2)) {
  const options = parseArguments(argv);
  const packed = await buildPackages({
    version: options.version,
    artifactsDirectory: options.artifacts,
    outputDirectory: options.output,
  });
  process.stdout.write(`${JSON.stringify(packed, null, 2)}\n`);
}

if (
  process.argv[1] &&
  resolve(process.argv[1]) === resolve(fileURLToPath(import.meta.url))
) {
  try {
    await main();
  } catch (error) {
    process.stderr.write(`build-npm-packages: ${error?.message ?? String(error)}\n`);
    process.exitCode = 1;
  }
}