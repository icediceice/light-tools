import assert from "node:assert/strict";
import { EventEmitter } from "node:events";
import test from "node:test";

import {
  NativePackageError,
  isMain,
  resolveNativeBinary,
  run,
  signalExitCode,
} from "./cli.mjs";

class FakeChild extends EventEmitter {
  killedWith = [];
  exitCode = null;
  signalCode = null;

  kill(signal) {
    this.killedWith.push(signal);
    return true;
  }
}

test("npm bin symlinks still execute the installed module as main", () => {
  const seen = [];
  const canonicalize = (candidate) => {
    seen.push(candidate);
    if (candidate.endsWith("/bin/light-tools")) {
      return "/real/package/npm/cli.mjs";
    }
    if (candidate.endsWith("/npm/cli.mjs")) {
      return "/real/package/npm/cli.mjs";
    }
    return candidate;
  };
  assert.equal(
    isMain(
      "file:///prefix/lib/node_modules/@icediceice/light-tools/npm/cli.mjs",
      "/prefix/bin/light-tools",
      canonicalize,
    ),
    true,
  );
  assert.equal(seen.length, 2);
  assert.equal(isMain(import.meta.url, "", canonicalize), false);
});

test("native resolution is anchored at the exact optional package", () => {
  const calls = [];
  const resolved = resolveNativeBinary({
    platform: "linux",
    arch: "x64",
    resolve(specifier) {
      calls.push(specifier);
      return "/prefix/lib/node_modules/@icediceice/light-tools-linux-x64/package.json";
    },
  });
  assert.deepEqual(calls, ["@icediceice/light-tools-linux-x64/package.json"]);
  assert.equal(
    resolved,
    "/prefix/lib/node_modules/@icediceice/light-tools-linux-x64/bin/light-tools",
  );
});

test("a missing optional package gives one actionable deterministic error", () => {
  assert.throws(
    () => resolveNativeBinary({
      platform: "linux",
      arch: "x64",
      resolve() {
        const error = new Error("not found");
        error.code = "MODULE_NOT_FOUND";
        throw error;
      },
    }),
    (error) => {
      assert.ok(error instanceof NativePackageError);
      assert.match(error.message, /@icediceice\/light-tools-linux-x64/);
      assert.match(error.message, /--omit=optional/);
      assert.match(error.message, /Alpine|musl/);
      return true;
    },
  );
});

test("the launcher preserves arguments, cwd, environment, stdio, and exit code", async () => {
  const child = new FakeChild();
  const signalSource = new EventEmitter();
  const environment = { LIGHT_TERSE_OUTPUT: "1" };
  const spawned = [];
  const pending = run({
    args: ["--enable-shell", "argument with spaces"],
    platform: "darwin",
    arch: "arm64",
    cwd: "/workspace with spaces",
    env: environment,
    resolve: () => "/packages/native/package.json",
    spawn(binary, args, options) {
      spawned.push({ binary, args, options });
      return child;
    },
    signalSource,
  });
  queueMicrotask(() => child.emit("close", 7, null));

  assert.equal(await pending, 7);
  assert.deepEqual(spawned, [{
    binary: "/packages/native/bin/light-tools",
    args: ["--enable-shell", "argument with spaces"],
    options: {
      cwd: "/workspace with spaces",
      env: environment,
      stdio: "inherit",
      windowsHide: true,
    },
  }]);
  assert.equal(signalSource.listenerCount("SIGINT"), 0);
  assert.equal(signalSource.listenerCount("SIGTERM"), 0);
  assert.equal(signalSource.listenerCount("SIGHUP"), 0);
});

test("termination signals are forwarded and signal exits are conventional", async () => {
  const child = new FakeChild();
  const signalSource = new EventEmitter();
  const pending = run({
    args: [],
    platform: "win32",
    arch: "x64",
    resolve: () => "C:\\npm\\native\\package.json",
    spawn: () => child,
    signalSource,
  });
  signalSource.emit("SIGTERM");
  child.emit("close", null, "SIGTERM");

  assert.equal(await pending, signalExitCode("SIGTERM"));
  assert.deepEqual(child.killedWith, ["SIGTERM"]);
});

test("spawn failures reject without rewriting their cause", async () => {
  const child = new FakeChild();
  const pending = run({
    args: [],
    platform: "linux",
    arch: "arm64",
    resolve: () => "/native/package.json",
    spawn: () => child,
    signalSource: new EventEmitter(),
  });
  const failure = Object.assign(new Error("permission denied"), { code: "EACCES" });
  child.emit("error", failure);
  await assert.rejects(pending, failure);
});