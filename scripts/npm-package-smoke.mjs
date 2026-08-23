#!/usr/bin/env node

import { spawnSync } from "node:child_process";
import { createHash } from "node:crypto";
import {
  chmod,
  copyFile,
  mkdir,
  mkdtemp,
  readFile,
  rm,
  writeFile,
} from "node:fs/promises";
import { homedir, tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { PLATFORM_KEYS, PLATFORMS, resolvePlatform } from "../npm/platform.mjs";
import { buildPackages } from "./build-npm-packages.mjs";

const repoRoot = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const version = "0.0.0-smoke";
const requirePwsh = process.argv.slice(2).includes("--require-pwsh");

function run(command, args, options = {}) {
  const result = spawnSync(command, args, {
    cwd: repoRoot,
    encoding: "utf8",
    ...options,
  });
  if (result.error) {
    throw result.error;
  }
  if (result.status !== 0 && !options.allowFailure) {
    throw new Error(
      `${command} ${args.join(" ")} failed with ${result.status}: ` +
      `${result.stderr?.trim() || result.stdout?.trim() || "<no output>"}`,
    );
  }
  return result;
}

function runNpm(args, options = {}) {
  return run(
    process.platform === "win32" ? "npm.cmd" : "npm",
    args,
    {
      ...options,
      shell: process.platform === "win32",
    },
  );
}

function commandAvailable(command, args = []) {
  const result = spawnSync(command, args, {
    cwd: repoRoot,
    encoding: "utf8",
  });
  if (result.error?.code === "ENOENT") {
    return false;
  }
  if (result.error) {
    throw result.error;
  }
  return result.status === 0;
}

async function sha256(path) {
  return createHash("sha256").update(await readFile(path)).digest("hex");
}

function globalLayout(prefix) {
  if (process.platform === "win32") {
    return {
      shim: join(prefix, "light-tools.cmd"),
      modules: join(prefix, "node_modules"),
    };
  }
  return {
    shim: join(prefix, "bin", "light-tools"),
    modules: join(prefix, "lib", "node_modules"),
  };
}

function invokeShim(shim, args, options = {}) {
  if (process.platform === "win32") {
    return run(
      process.env.ComSpec || "cmd.exe",
      ["/d", "/s", "/c", shim, ...args],
      options,
    );
  }
  return run(shim, args, options);
}

function assertExactFiles(actual, expected, label) {
  const sortedActual = [...actual].sort();
  const sortedExpected = [...expected].sort();
  if (JSON.stringify(sortedActual) !== JSON.stringify(sortedExpected)) {
    throw new Error(
      `${label} files differ: expected ${JSON.stringify(sortedExpected)}, got ${JSON.stringify(sortedActual)}`,
    );
  }
}

async function main() {
  const root = await mkdtemp(join(tmpdir(), "light-tools-npm-smoke-"));
  const artifacts = join(root, "artifacts");
  const packages = join(root, "packages");
  const prefix = join(root, "prefix");
  const missingPrefix = join(root, "missing-prefix");
  const npmCache = join(root, "npm-cache");
  const npmrc = join(root, "empty.npmrc");
  const workspace = join(root, "workspace");
  const stateRoot = join(root, "state");

  try {
    await Promise.all([
      mkdir(artifacts, { recursive: true }),
      mkdir(packages, { recursive: true }),
      mkdir(prefix, { recursive: true }),
      mkdir(missingPrefix, { recursive: true }),
      mkdir(workspace, { recursive: true }),
      writeFile(npmrc, "", "utf8"),
    ]);

    const host = resolvePlatform();
    const builtBinary = join(
      root,
      process.platform === "win32" ? "light-tools.exe" : "light-tools",
    );
    const goArguments = ["build"];
    if (!(process.platform === "win32" && process.arch === "arm64")) {
      goArguments.push("-tags=treesitter");
    }
    goArguments.push(
      "-ldflags",
      `-s -w -X main.version=${version}`,
      "-o",
      builtBinary,
      "./cmd/light-tools",
    );
    run("go", goArguments);
    if (process.platform !== "win32") {
      await chmod(builtBinary, 0o755);
    }

    for (const key of PLATFORM_KEYS) {
      const definition = PLATFORMS[key];
      const directory = join(artifacts, definition.artifactKey);
      await mkdir(directory, { recursive: true });
      const destination = join(directory, definition.executable);
      await copyFile(builtBinary, destination);
      if (definition.platform !== "win32") {
        await chmod(destination, 0o755);
      }
    }

    const packed = await buildPackages({
      version,
      artifactsDirectory: artifacts,
      outputDirectory: packages,
      repoRoot,
    });
    if (packed.length !== 7) {
      throw new Error(`expected seven npm tarballs, found ${packed.length}`);
    }

    const rootPackage = packed.find(({ key }) => key === "root");
    const nativePackage = packed.find(({ key }) => key === host.key);
    if (!rootPackage || !nativePackage) {
      throw new Error(`missing root or host package for ${host.key}`);
    }
    if (rootPackage.size > 1_048_576) {
      throw new Error(`root package exceeds 1 MiB: ${rootPackage.size}`);
    }
    for (const artifact of packed.filter(({ key }) => key !== "root")) {
      if (artifact.size > 33_554_432) {
        throw new Error(`${artifact.filename} exceeds 32 MiB: ${artifact.size}`);
      }
    }

    assertExactFiles(
      rootPackage.files,
      ["AGENT-SETUP.md", "LICENSE", "README.md", "npm/cli.mjs", "npm/platform.mjs", "package.json"],
      "root package",
    );
    assertExactFiles(
      nativePackage.files,
      ["LICENSE", "README.md", `bin/${host.executable}`, "package.json"],
      "native package",
    );

    const npmEnvironment = {
      ...process.env,
      npm_config_cache: npmCache,
      npm_config_userconfig: npmrc,
      npm_config_audit: "false",
      npm_config_fund: "false",
      npm_config_update_notifier: "false",
    };
    const rootTarball = join(packages, rootPackage.filename);
    const nativeTarball = join(packages, nativePackage.filename);

    runNpm(
      [
        "install",
        "--global",
        "--ignore-scripts",
        "--offline",
        "--prefix",
        prefix,
        rootTarball,
        nativeTarball,
      ],
      { env: npmEnvironment },
    );

    const layout = globalLayout(prefix);
    const installedBinary = join(
      layout.modules,
      host.packageName,
      "bin",
      host.executable,
    );
    if (await sha256(installedBinary) !== nativePackage.binarySha256) {
      throw new Error("installed native binary differs from the packed release binary");
    }

    const versionResult = invokeShim(layout.shim, ["version"], {
      env: npmEnvironment,
      allowFailure: true,
    });
    if (versionResult.status !== 0 || versionResult.stdout.trim() !== version) {
      throw new Error(JSON.stringify({
        message: "wrapper version mismatch",
        expected: version,
        status: versionResult.status,
        stdout: versionResult.stdout.trim(),
        stderr: versionResult.stderr.trim(),
      }));
    }

    const transcript = [
      JSON.stringify({ jsonrpc: "2.0", id: 1, method: "initialize", params: {} }),
      JSON.stringify({ jsonrpc: "2.0", id: 2, method: "tools/list", params: {} }),
      "",
    ].join("\n");
    const wrapperMcp = invokeShim(layout.shim, [], {
      cwd: workspace,
      env: {
        ...npmEnvironment,
        XDG_CONFIG_HOME: join(stateRoot, "wrapper-config"),
        XDG_DATA_HOME: join(stateRoot, "wrapper-data"),
        XDG_RUNTIME_DIR: join(stateRoot, "wrapper-runtime"),
      },
      input: transcript,
    });
    const responses = wrapperMcp.stdout
      .trim()
      .split(/\r?\n/)
      .map((line) => JSON.parse(line));
    if (
      responses.length !== 2 ||
      responses[0]?.result?.protocolVersion !== "2025-06-18" ||
      responses[1]?.result?.tools?.[0]?.name !== "light_file"
    ) {
      throw new Error("npm wrapper did not preserve the MCP stdio transcript");
    }

    runNpm(
      [
        "install",
        "--global",
        "--ignore-scripts",
        "--offline",
        "--omit=optional",
        "--prefix",
        missingPrefix,
        rootTarball,
      ],
      { env: npmEnvironment },
    );
    const missing = invokeShim(globalLayout(missingPrefix).shim, ["version"], {
      env: npmEnvironment,
      allowFailure: true,
    });
    if (
      missing.status !== 1 ||
      !missing.stderr.includes("--omit=optional") ||
      !missing.stderr.includes(host.packageName)
    ) {
      throw new Error("missing optional package did not fail with the documented error");
    }

    const symbolMode =
      process.platform === "win32" && process.arch === "arm64"
        ? "no-symbol"
        : "symbols";
    await Promise.all([
      mkdir(join(stateRoot, "config"), { recursive: true }),
      mkdir(join(stateRoot, "data"), { recursive: true }),
      mkdir(join(stateRoot, "runtime"), { recursive: true }),
    ]);
    const hasPwsh = commandAvailable("pwsh", ["-NoProfile", "-Command", "exit 0"]);
    if (!hasPwsh && requirePwsh) {
      throw new Error("pwsh is required for the full package MCP smoke");
    }
    if (hasPwsh) {
      run(
        "pwsh",
        [
          "-NoProfile",
          "-File",
          join(repoRoot, "scripts", "mcp-smoke.ps1"),
          "-Binary",
          layout.shim,
          "-ExpectedVersion",
          version,
          "-Workspace",
          workspace,
          "-SymbolMode",
          symbolMode,
          "-RealHome",
          homedir(),
        ],
        {
          env: {
            ...npmEnvironment,
            XDG_CONFIG_HOME: join(stateRoot, "config"),
            XDG_DATA_HOME: join(stateRoot, "data"),
            XDG_RUNTIME_DIR: join(stateRoot, "runtime"),
          },
        },
      );
    }

    process.stdout.write(
      `npm package smoke passed: ${host.key}, seven tarballs, ${symbolMode}, ` +
      `full_mcp=${hasPwsh ? "passed" : "skipped-no-pwsh"}\n`,
    );
  } finally {
    await rm(root, { recursive: true, force: true });
  }
}

try {
  await main();
} catch (error) {
  process.stderr.write(`npm-package-smoke: ${error?.message ?? String(error)}\n`);
  process.exitCode = 1;
}