#!/usr/bin/env node

import { createRequire } from "node:module";
import { constants as osConstants } from "node:os";
import path, { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { spawn as spawnChild } from "node:child_process";

import { resolvePlatform } from "./platform.mjs";

const ownRequire = createRequire(import.meta.url);
const forwardedSignals = ["SIGINT", "SIGTERM", "SIGHUP"];

export class NativePackageError extends Error {
  constructor(definition, cause) {
    super(
      `native package ${definition.packageName} is unavailable for ` +
      `${definition.platform}/${definition.arch}; reinstall without ` +
      `--omit=optional. Alpine/musl is unsupported because release binaries require glibc.`,
      { cause },
    );
    this.name = "NativePackageError";
    this.code = "E_NATIVE_PACKAGE_MISSING";
    this.packageName = definition.packageName;
  }
}

export function resolveNativeBinary({
  platform = process.platform,
  arch = process.arch,
  resolve = ownRequire.resolve,
} = {}) {
  const definition = resolvePlatform(platform, arch);
  let manifestPath;
  try {
    manifestPath = resolve(`${definition.packageName}/package.json`);
  } catch (error) {
    if (error?.code !== "MODULE_NOT_FOUND") {
      throw error;
    }
    throw new NativePackageError(definition, error);
  }
  const pathApi = platform === "win32" ? path.win32 : path;
  return pathApi.join(pathApi.dirname(manifestPath), "bin", definition.executable);
}

export function signalExitCode(signal) {
  const number = osConstants.signals?.[signal];
  return Number.isInteger(number) ? 128 + number : 1;
}

export function run({
  args = process.argv.slice(2),
  platform = process.platform,
  arch = process.arch,
  cwd = process.cwd(),
  env = process.env,
  resolve = ownRequire.resolve,
  spawn = spawnChild,
  signalSource = process,
} = {}) {
  const binary = resolveNativeBinary({ platform, arch, resolve });
  const child = spawn(binary, args, {
    cwd,
    env,
    stdio: "inherit",
    windowsHide: true,
  });

  return new Promise((resolveRun, rejectRun) => {
    let settled = false;
    const handlers = new Map();

    const cleanup = () => {
      for (const [signal, handler] of handlers) {
        signalSource.removeListener(signal, handler);
      }
    };
    const settle = (callback) => {
      if (settled) {
        return;
      }
      settled = true;
      cleanup();
      callback();
    };

    for (const signal of forwardedSignals) {
      const handler = () => {
        if (child.exitCode === null && child.signalCode === null) {
          child.kill(signal);
        }
      };
      handlers.set(signal, handler);
      signalSource.on(signal, handler);
    }

    child.once("error", (error) => settle(() => rejectRun(error)));
    child.once("close", (code, signal) => {
      settle(() => resolveRun(
        Number.isInteger(code) ? code : signalExitCode(signal),
      ));
    });
  });
}

export async function main() {
  try {
    process.exitCode = await run();
  } catch (error) {
    process.stderr.write(`light-tools: ${error?.message ?? String(error)}\n`);
    process.exitCode = 1;
  }
}

const entryPath = process.argv[1] ? path.resolve(process.argv[1]) : "";
if (entryPath === path.resolve(fileURLToPath(import.meta.url))) {
  await main();
}