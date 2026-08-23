import assert from "node:assert/strict";
import { EventEmitter } from "node:events";
import test from "node:test";

import {
  NativeBinaryError,
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
      "file:///prefix/lib/node_modules/@factor-i-o/light-tools/npm/cli.mjs",
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
      return "/prefix/lib/node_modules/@factor-i-o/light-tools-linux-x64/package.json";
    },
  });
  assert.deepEqual(calls, ["@factor-i-o/light-tools-linux-x64/package.json"]);
  assert.equal(
    resolved,
    "/prefix/lib/node_modules/@factor-i-o/light-tools-linux-x64/bin/light-tools",
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
      assert.match(error.message, /@factor-i-o\/light-tools-linux-x64/);
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

test("native start failures are actionable and preserve their cause", async () => {
  for (const code of ["ENOENT", "EACCES", "ENOEXEC"]) {
    const child = new FakeChild();
    const pending = run({
      args: [],
      platform: "linux",
      arch: "arm64",
      resolve: () => "/native/package.json",
      spawn: () => child,
      signalSource: new EventEmitter(),
    });
    const failure = Object.assign(new Error(`start failed: ${code}`), { code });
    child.emit("error", failure);
    await assert.rejects(pending, (error) => {
      assert.ok(error instanceof NativeBinaryError);
      assert.equal(error.code, "E_NATIVE_BINARY_UNUSABLE");
      assert.equal(error.cause, failure);
      assert.match(error.message, /@factor-i-o\/light-tools-linux-arm64/);
      assert.match(error.message, /npm install --global --force @factor-i-o\/light-tools/);
      assert.match(error.message, /glibc/);
      return true;
    });
  }
});

test("synchronous native start failures are mapped, while unrelated errors pass through", async () => {
  const failure = Object.assign(new Error("missing executable"), { code: "ENOENT" });
  assert.throws(
    () => run({
      platform: "darwin",
      arch: "x64",
      resolve: () => "/native/package.json",
      spawn() {
        throw failure;
      },
    }),
    (error) => error instanceof NativeBinaryError && error.cause === failure,
  );

  const child = new FakeChild();
  const pending = run({
    platform: "linux",
    arch: "x64",
    resolve: () => "/native/package.json",
    spawn: () => child,
    signalSource: new EventEmitter(),
  });
  const unrelated = Object.assign(new Error("pipe failed"), { code: "EPIPE" });
  child.emit("error", unrelated);
  await assert.rejects(pending, (error) => error === unrelated);
});