import assert from "node:assert/strict";
import test from "node:test";

import {
  PLATFORM_KEYS,
  UnsupportedPlatformError,
  executableFor,
  packageNameFor,
  resolvePlatform,
} from "./platform.mjs";

const expected = [
  ["darwin", "arm64", "darwin-arm64", "@factor-i-o/light-tools-darwin-arm64", "light-tools"],
  ["darwin", "x64", "darwin-x64", "@factor-i-o/light-tools-darwin-x64", "light-tools"],
  ["linux", "arm64", "linux-arm64", "@factor-i-o/light-tools-linux-arm64", "light-tools"],
  ["linux", "x64", "linux-x64", "@factor-i-o/light-tools-linux-x64", "light-tools"],
  ["win32", "arm64", "win32-arm64", "@factor-i-o/light-tools-win32-arm64", "light-tools.exe"],
  ["win32", "x64", "win32-x64", "@factor-i-o/light-tools-win32-x64", "light-tools.exe"],
];

test("the public package matrix is exact and deterministic", () => {
  assert.deepEqual(PLATFORM_KEYS, expected.map((entry) => entry[2]));
  for (const [platform, arch, key, packageName, executable] of expected) {
    assert.equal(resolvePlatform(platform, arch).key, key);
    assert.equal(packageNameFor(platform, arch), packageName);
    assert.equal(executableFor(platform, arch), executable);
  }
});

test("unsupported operating systems and architectures fail actionably", () => {
  for (const [platform, arch] of [["freebsd", "x64"], ["linux", "ia32"], ["win32", "ppc64"]]) {
    assert.throws(
      () => resolvePlatform(platform, arch),
      (error) => {
        assert.ok(error instanceof UnsupportedPlatformError);
        assert.match(error.message, new RegExp(platform));
        assert.match(error.message, new RegExp(arch));
        assert.match(error.message, /supported targets/i);
        return true;
      },
    );
  }
});