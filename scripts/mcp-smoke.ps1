param(
    [Parameter(Mandatory = $true)]
    [string]$Binary,
    [Parameter(Mandatory = $true)]
    [string]$ExpectedVersion,
    [Parameter(Mandatory = $true)]
    [string]$Workspace,
    [Parameter(Mandatory = $true)]
    [ValidateSet("symbols", "no-symbol")]
    [string]$SymbolMode,
    [string]$RealHome
)

$ErrorActionPreference = "Stop"
Remove-Item Env:LIGHT_TERSE_OUTPUT -ErrorAction SilentlyContinue
$Binary = [System.IO.Path]::GetFullPath($Binary)
$Workspace = [System.IO.Path]::GetFullPath($Workspace)
New-Item -ItemType Directory -Force -Path $Workspace | Out-Null

foreach ($name in @("XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_RUNTIME_DIR")) {
    if ([string]::IsNullOrWhiteSpace([Environment]::GetEnvironmentVariable($name))) {
        throw "$name must point at the isolated candidate state root."
    }
}

function Assert-Equal {
    param($Actual, $Expected, [string]$Label)
    $actualJson = ConvertTo-Json -Compress -Depth 20 -InputObject @($Actual)
    $expectedJson = ConvertTo-Json -Compress -Depth 20 -InputObject @($Expected)
    if ($actualJson -ne $expectedJson) {
        throw "$Label mismatch: expected $expectedJson, got $actualJson"
    }
}

function Invoke-McpTranscript {
    param(
        [string[]]$ServerArguments,
        [object[]]$Requests
    )

    $requestLines = @($Requests | ForEach-Object {
        ConvertTo-Json -Compress -Depth 20 -InputObject $_
    })
    Push-Location $Workspace
    try {
        $responses = @($requestLines | & $Binary @ServerArguments | ForEach-Object {
            ConvertFrom-Json -InputObject $_
        })
        if ($LASTEXITCODE -ne 0) {
            throw "MCP server exited with code $LASTEXITCODE"
        }
        return $responses
    }
    finally {
        Pop-Location
    }
}

function Get-ToolValue {
    param($Response, [string]$Label)
    if ($null -ne $Response.error) {
        throw "$Label returned a JSON-RPC error: $($Response.error.message)"
    }
    if ($Response.result.isError) {
        throw "$Label returned a tool error: $($Response.result.content[0].text)"
    }
    return ConvertFrom-Json -InputObject $Response.result.content[0].text
}

$version = (& $Binary version).Trim()
if ($LASTEXITCODE -ne 0 -or $version -ne $ExpectedVersion) {
    throw "version mismatch: expected $ExpectedVersion, got $version"
}

$beforeHomeEntries = @()
if (-not [string]::IsNullOrWhiteSpace($RealHome) -and (Test-Path -LiteralPath $RealHome)) {
    $beforeHomeEntries = @(Get-ChildItem -Force -LiteralPath $RealHome -Filter "light-tools*" |
        ForEach-Object { $_.FullName } | Sort-Object)
}

& $Binary init --client print | Out-Null
if ($LASTEXITCODE -ne 0) {
    throw "light-tools init failed with code $LASTEXITCODE"
}

$defaultRequests = @(
    [ordered]@{ jsonrpc = "2.0"; id = 1; method = "initialize"; params = @{} },
    [ordered]@{ jsonrpc = "2.0"; id = 2; method = "tools/list"; params = @{} }
)
$defaultResponses = @(Invoke-McpTranscript -ServerArguments @() -Requests $defaultRequests)
if ($defaultResponses.Count -ne 2 -or $defaultResponses[0].result.protocolVersion -ne "2025-06-18") {
    throw "default profile did not initialize"
}
$defaultTools = @($defaultResponses[1].result.tools | ForEach-Object { $_.name })
Assert-Equal $defaultTools @("light_file") "default tools/list"

$statePaths = @(
    (Join-Path $env:XDG_CONFIG_HOME "light-tools"),
    (Join-Path $env:XDG_DATA_HOME "light-tools-secrets"),
    (Join-Path $env:XDG_DATA_HOME "light-tools-snapshots"),
    (Join-Path $env:XDG_RUNTIME_DIR "light-tools-spills")
)
foreach ($statePath in $statePaths) {
    if (-not (Test-Path -LiteralPath (Join-Path $statePath "SCHEMA") -PathType Leaf)) {
        throw "isolated state store was not initialized: $statePath"
    }
}

if (-not [string]::IsNullOrWhiteSpace($RealHome) -and (Test-Path -LiteralPath $RealHome)) {
    $afterHomeEntries = @(Get-ChildItem -Force -LiteralPath $RealHome -Filter "light-tools*" |
        ForEach-Object { $_.FullName } | Sort-Object)
    Assert-Equal $afterHomeEntries $beforeHomeEntries "real-home light-tools entries"
}

$sourcePath = Join-Path $Workspace "release_probe.go"
@"
package releaseprobe

func releaseProbe() string {
	return "candidate-package"
}
"@ | Set-Content -Encoding utf8 -Path $sourcePath
$tsxPath = Join-Path $Workspace "release_probe.tsx"
@"
interface Props { label: string }
export const Badge = (p: Props) => <span>{p.label}</span>;
"@ | Set-Content -Encoding utf8 -Path $tsxPath
$outsidePath = Join-Path (Split-Path -Parent $Workspace) "outside-release-probe.txt"
"outside" | Set-Content -Encoding utf8 -Path $outsidePath
$imagePath = Join-Path $Workspace "release-probe.png"
[System.IO.File]::WriteAllBytes(
    $imagePath,
    [Convert]::FromBase64String("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAusB9Wl2nZ0AAAAASUVORK5CYII=")
)
$tersePath = Join-Path $Workspace "terse-probe.txt"
(("word " * 120).TrimEnd()) | Set-Content -NoNewline -Encoding utf8 -Path $tersePath

$enabledRequests = @(
    [ordered]@{ jsonrpc = "2.0"; id = 1; method = "initialize"; params = @{} },
    [ordered]@{ jsonrpc = "2.0"; id = 2; method = "tools/list"; params = @{} },
    [ordered]@{
        jsonrpc = "2.0"; id = 3; method = "tools/call"
        params = [ordered]@{
            name = "light_file"
            arguments = [ordered]@{ verb = "read"; path = $sourcePath; offset = 0; limit = 20 }
        }
    },
    [ordered]@{
        jsonrpc = "2.0"; id = 4; method = "tools/call"
        params = [ordered]@{
            name = "light_bash"
            arguments = [ordered]@{
                command = "echo release-smoke"; cwd = $Workspace; timeout_ms = 30000
            }
        }
    },
    [ordered]@{
        jsonrpc = "2.0"; id = 5; method = "tools/call"
        params = [ordered]@{
            name = "light_file"
            arguments = [ordered]@{ verb = "symbol"; path = $sourcePath; name = "releaseProbe" }
        }
    },
    [ordered]@{
        jsonrpc = "2.0"; id = 6; method = "tools/call"
        params = [ordered]@{
            name = "light_file"
            arguments = [ordered]@{ verb = "read"; path = $outsidePath; offset = 0; limit = 5 }
        }
    },
    [ordered]@{
        jsonrpc = "2.0"; id = 7; method = "tools/call"
        params = [ordered]@{
            name = "light_ops"
            arguments = [ordered]@{ verb = "probe_file"; path = $sourcePath }
        }
    },
    [ordered]@{
        jsonrpc = "2.0"; id = 8; method = "tools/call"
        params = [ordered]@{
            name = "light_ssh"
            arguments = [ordered]@{ command = "must-not-execute" }
        }
    },
    [ordered]@{
        jsonrpc = "2.0"; id = 9; method = "tools/call"
        params = [ordered]@{
            name = "light_scp"
            arguments = [ordered]@{ src = $sourcePath; dst = (Join-Path $Workspace "also-local.txt") }
        }
    },
    [ordered]@{
        jsonrpc = "2.0"; id = 10; method = "tools/call"
        params = [ordered]@{
            name = "light_file"
            arguments = [ordered]@{ verb = "read"; path = $imagePath }
        }
    },
    [ordered]@{
        jsonrpc = "2.0"; id = 11; method = "tools/call"
        params = [ordered]@{
            name = "light_file"
            arguments = [ordered]@{ verb = "symbol"; path = $tsxPath; name = "Props" }
        }
    },
    [ordered]@{
        jsonrpc = "2.0"; id = 12; method = "tools/call"
        params = [ordered]@{
            name = "light_file"
            arguments = [ordered]@{ verb = "symbol"; path = $tsxPath; name = "Badge" }
        }
    }
$enabledArguments = @("--enable-shell", "--enable-remote", "--enable-ops")
$enabledResponses = @(Invoke-McpTranscript -ServerArguments $enabledArguments -Requests $enabledRequests)
if ($enabledResponses.Count -ne 12 -or $enabledResponses[0].result.protocolVersion -ne "2025-06-18") {
    throw "fully enabled profile did not initialize"
}
$enabledTools = @($enabledResponses[1].result.tools | ForEach-Object { $_.name })
Assert-Equal $enabledTools @("light_bash", "light_file", "light_ops", "light_scp", "light_ssh") "enabled tools/list"

$fileValue = Get-ToolValue $enabledResponses[2] "light_file read"
if ($fileValue.content -notmatch "candidate-package") {
    throw "installed light_file did not read the workspace probe"
}

$bashValue = Get-ToolValue $enabledResponses[3] "light_bash"
if ($bashValue.exit_code -ne 0 -or $bashValue.stdout.Trim() -ne "release-smoke") {
    throw "installed light_bash returned an unexpected result"
}

$symbolValue = Get-ToolValue $enabledResponses[4] "light_file symbol"
if ($SymbolMode -eq "symbols") {
    if (@($symbolValue.matches).Count -lt 1) {
        throw "tree-sitter package did not extract releaseProbe"
    }
    $propsValue = Get-ToolValue $enabledResponses[10] "light_file TSX interface symbol"
    $badgeValue = Get-ToolValue $enabledResponses[11] "light_file TSX JSX symbol"
    if (@($propsValue.matches).Count -ne 1 -or @($badgeValue.matches).Count -ne 1) {
        throw "tree-sitter package did not prove dedicated TSX grammar with Props and Badge"
    }
}
else {
    if ($symbolValue.tree_sitter -ne $false -or @($symbolValue.matches).Count -ne 0) {
        throw "Windows ARM64 package did not return the documented no-symbol fallback"
    }
}

if (-not $enabledResponses[5].result.isError) {
    throw "light_file unexpectedly read outside the isolated workspace"
}

$opsValue = Get-ToolValue $enabledResponses[6] "light_ops"
if (-not $opsValue.exists -or $opsValue.size -le 0) {
    throw "installed light_ops probe_file returned an unexpected result"
}
if (-not $enabledResponses[7].result.isError -or
    $enabledResponses[7].result.content[0].text -notmatch "remote or profile is required") {
    throw "installed light_ssh did not refuse before execution"
}
if (-not $enabledResponses[8].result.isError -or
    $enabledResponses[8].result.content[0].text -notmatch "exactly one of src and dst must be remote") {
    throw "installed light_scp did not refuse before execution"
}
$imageContent = $enabledResponses[9].result.content[0]
if ($enabledResponses[9].result.isError -or $imageContent.type -ne "image" -or
    $imageContent.mimeType -ne "image/png" -or [string]::IsNullOrWhiteSpace($imageContent.data)) {
    throw "installed light_file image response was not preserved"
}

$terseRequests = @(
    [ordered]@{ jsonrpc = "2.0"; id = 1; method = "initialize"; params = @{} },
    [ordered]@{
        jsonrpc = "2.0"; id = 2; method = "tools/call"
        params = [ordered]@{
            name = "light_file"
            arguments = [ordered]@{ verb = "read"; path = $tersePath; offset = 0; limit = 20 }
        }
    }
)
$rawTerseResponses = @(Invoke-McpTranscript -ServerArguments $enabledArguments -Requests $terseRequests)
if ($rawTerseResponses.Count -ne 2 -or $rawTerseResponses[1].result.isError) {
    throw "raw formatter probe failed"
}
$rawTerseText = [string]$rawTerseResponses[1].result.content[0].text
$null = ConvertFrom-Json -InputObject $rawTerseText

try {
    $env:LIGHT_TERSE_OUTPUT = "1"
    $firstTerseResponses = @(Invoke-McpTranscript -ServerArguments $enabledArguments -Requests $terseRequests)
    $secondTerseResponses = @(Invoke-McpTranscript -ServerArguments $enabledArguments -Requests $terseRequests)
}
finally {
    Remove-Item Env:LIGHT_TERSE_OUTPUT -ErrorAction SilentlyContinue
}
if ($firstTerseResponses[1].result.isError -or $secondTerseResponses[1].result.isError) {
    throw "opt-in formatter probe returned a tool error"
}
$firstTerseText = [string]$firstTerseResponses[1].result.content[0].text
$secondTerseText = [string]$secondTerseResponses[1].result.content[0].text
if ($firstTerseText -cne $secondTerseText) {
    throw "opt-in formatter output was not deterministic"
}
if ($firstTerseText -ceq $rawTerseText) {
    throw "LIGHT_TERSE_OUTPUT did not engage: the installed binary ignored the opt-in gate"
}
if (-not $firstTerseText.StartsWith("~")) {
    throw "opt-in formatter emitted non-terse text: $($firstTerseText.Substring(0, [Math]::Min(40, $firstTerseText.Length)))"
}
$rawByteCount = [Text.Encoding]::UTF8.GetByteCount($rawTerseText)
$terseByteCount = [Text.Encoding]::UTF8.GetByteCount($firstTerseText)
if ($terseByteCount -ge $rawByteCount) {
    throw "opt-in formatter output was not strictly smaller"
}

Write-Host "release package smoke passed: $ExpectedVersion ($SymbolMode)"