const SCOPE = "@icediceice";

const definitions = [
  {
    key: "darwin-arm64",
    platform: "darwin",
    arch: "arm64",
    artifactKey: "darwin_arm64",
    packageName: `${SCOPE}/light-tools-darwin-arm64`,
    executable: "light-tools",
  },
  {
    key: "darwin-x64",
    platform: "darwin",
    arch: "x64",
    artifactKey: "darwin_amd64",
    packageName: `${SCOPE}/light-tools-darwin-x64`,
    executable: "light-tools",
  },
  {
    key: "linux-arm64",
    platform: "linux",
    arch: "arm64",
    artifactKey: "linux_arm64",
    packageName: `${SCOPE}/light-tools-linux-arm64`,
    executable: "light-tools",
    libc: "glibc",
  },
  {
    key: "linux-x64",
    platform: "linux",
    arch: "x64",
    artifactKey: "linux_amd64",
    packageName: `${SCOPE}/light-tools-linux-x64`,
    executable: "light-tools",
    libc: "glibc",
  },
  {
    key: "win32-arm64",
    platform: "win32",
    arch: "arm64",
    artifactKey: "windows_arm64",
    packageName: `${SCOPE}/light-tools-win32-arm64`,
    executable: "light-tools.exe",
  },
  {
    key: "win32-x64",
    platform: "win32",
    arch: "x64",
    artifactKey: "windows_amd64",
    packageName: `${SCOPE}/light-tools-win32-x64`,
    executable: "light-tools.exe",
  },
];

export const PLATFORMS = Object.freeze(
  Object.fromEntries(definitions.map((definition) => [
    definition.key,
    Object.freeze({ ...definition }),
  ])),
);

export const PLATFORM_KEYS = Object.freeze(definitions.map(({ key }) => key));

export class UnsupportedPlatformError extends Error {
  constructor(platform, arch) {
    super(
      `unsupported platform ${platform}/${arch}; supported targets: ${PLATFORM_KEYS.join(", ")}`,
    );
    this.name = "UnsupportedPlatformError";
    this.code = "E_UNSUPPORTED_PLATFORM";
    this.platform = platform;
    this.arch = arch;
  }
}

export function resolvePlatform(
  platform = process.platform,
  arch = process.arch,
) {
  const definition = PLATFORMS[`${platform}-${arch}`];
  if (!definition) {
    throw new UnsupportedPlatformError(platform, arch);
  }
  return definition;
}

export function packageNameFor(platform = process.platform, arch = process.arch) {
  return resolvePlatform(platform, arch).packageName;
}

export function executableFor(platform = process.platform, arch = process.arch) {
  return resolvePlatform(platform, arch).executable;
}