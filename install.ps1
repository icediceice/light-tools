param(
    [string]$Version = $env:LIGHT_TOOLS_VERSION,
    [string]$Destination = $env:LIGHT_TOOLS_INSTALL_DIR,
    [string]$Repository = $env:LIGHT_TOOLS_REPO,
    [string]$BaseUrl = $env:LIGHT_TOOLS_BASE_URL
)

$ErrorActionPreference = "Stop"
if ([string]::IsNullOrWhiteSpace($Repository)) {
    $Repository = "icediceice/light-tools"
}
if ([string]::IsNullOrWhiteSpace($Destination)) {
    $Destination = Join-Path $HOME ".local\bin"
}
if ([string]::IsNullOrWhiteSpace($Version)) {
    $release = Invoke-RestMethod -Uri ("https://api.github.com/repos/{0}/releases/latest" -f $Repository)
    $Version = [string]$release.tag_name
}
$Version = $Version.TrimStart("v")
if ([string]::IsNullOrWhiteSpace($Version)) {
    throw "Could not resolve a release version."
}

$architecture = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture
switch ($architecture) {
    "X64" { $arch = "amd64" }
    "Arm64" { $arch = "arm64" }
    default { throw "Unsupported Windows architecture: $architecture" }
}

$asset = "light-tools_{0}_windows_{1}.zip" -f $Version, $arch
if ([string]::IsNullOrWhiteSpace($BaseUrl)) {
    $base = "https://github.com/{0}/releases/download/v{1}" -f $Repository, $Version
}
else {
    $base = $BaseUrl.TrimEnd("/")
}
$tempDirectory = Join-Path ([System.IO.Path]::GetTempPath()) ("light-tools-" + [guid]::NewGuid().ToString("N"))
$archive = Join-Path $tempDirectory $asset
$checksumFile = Join-Path $tempDirectory "checksums.txt"

New-Item -ItemType Directory -Path $tempDirectory | Out-Null
try {
    Invoke-WebRequest -Uri "$base/$asset" -OutFile $archive
    Invoke-WebRequest -Uri "$base/checksums.txt" -OutFile $checksumFile

    $checksums = Get-Content -Raw $checksumFile
    $pattern = "(?mi)^([0-9a-f]{64})\s+\*?" + [regex]::Escape($asset) + "\s*$"
    $match = [regex]::Match($checksums, $pattern)
    if (-not $match.Success) {
        throw "Checksum missing for $asset."
    }
    $expected = $match.Groups[1].Value.ToLowerInvariant()
    $actual = (Get-FileHash -Algorithm SHA256 -Path $archive).Hash.ToLowerInvariant()
    if ($actual -ne $expected) {
        throw "Checksum mismatch for $asset."
    }

    $unpacked = Join-Path $tempDirectory "unpacked"
    Expand-Archive -Path $archive -DestinationPath $unpacked
    New-Item -ItemType Directory -Force -Path $Destination | Out-Null
    $installed = Join-Path $Destination "light-tools.exe"
    Copy-Item -Force (Join-Path $unpacked "light-tools.exe") $installed
    Write-Host "installed $installed"
    Write-Host "next: light-tools init"
}
finally {
    if (Test-Path $tempDirectory) {
        Remove-Item -Recurse -Force $tempDirectory
    }
}
