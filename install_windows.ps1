Param()

$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$InstallLabel = 'com.routerforme.cli-cache-proxy'
$DefaultInstallRoot = Join-Path $env:USERPROFILE '.cli-cache-proxy'
$DefaultSourceConfig = 'C:\temp\cli-proxy-api-test\config.yaml'
$DefaultSourceStatsDir = Join-Path $env:USERPROFILE 'Desktop\CLIProxyAPI\stats'

function Say($Message) {
    Write-Host $Message
}

function Warn($Message) {
    Write-Warning $Message
}

function Fail($Message) {
    throw $Message
}

function Expand-PathValue($Value) {
    if ([string]::IsNullOrWhiteSpace($Value)) { return $Value }
    return [System.IO.Path]::GetFullPath([Environment]::ExpandEnvironmentVariables($Value.Trim()))
}

function Prompt-WithDefault($Label, $DefaultValue) {
    $reply = Read-Host "$Label [$DefaultValue]"
    if ([string]::IsNullOrWhiteSpace($reply)) { return $DefaultValue }
    return $reply
}

function Confirm-YesNo($Prompt, $DefaultYes = $true) {
    $suffix = if ($DefaultYes) { '[Y/n]' } else { '[y/N]' }
    while ($true) {
        $reply = Read-Host "$Prompt $suffix"
        if ([string]::IsNullOrWhiteSpace($reply)) { return $DefaultYes }
        switch -Regex ($reply.Trim()) {
            '^(y|yes)$' { return $true }
            '^(n|no)$' { return $false }
            default { Say 'Please answer yes or no.' }
        }
    }
}

function Ensure-Dir($Path) {
    New-Item -ItemType Directory -Force -Path $Path | Out-Null
}

function Copy-IfMissing($Source, $Target) {
    if (-not (Test-Path $Source) -or (Test-Path $Target)) { return }
    Ensure-Dir (Split-Path -Parent $Target)
    Copy-Item -Force $Source $Target
}

function Merge-AuthDir($SourceDir, $TargetDir) {
    if (-not (Test-Path $SourceDir) -or $SourceDir -eq $TargetDir) { return }
    Ensure-Dir $TargetDir
    Get-ChildItem -Recurse -File $SourceDir | ForEach-Object {
        $relative = $_.FullName.Substring($SourceDir.Length).TrimStart('\\')
        $target = Join-Path $TargetDir $relative
        if (-not (Test-Path $target)) {
            Ensure-Dir (Split-Path -Parent $target)
            Copy-Item -Force $_.FullName $target
        }
    }
}

function Build-Binary($RepoRoot, $OutputPath, $ConfigPath) {
    $version = (git -C $RepoRoot describe --tags --always --dirty)
    $commit = (git -C $RepoRoot rev-parse HEAD)
    $buildDate = (Get-Date).ToUniversalTime().ToString('yyyy-MM-ddTHH:mm:ssZ')
    $ldflags = @(
        '-s -w',
        "-X main.Version=$version",
        "-X main.Commit=$commit",
        "-X main.BuildDate=$buildDate",
        "-X main.DefaultConfigPath=$ConfigPath"
    ) -join ' '
    Push-Location $RepoRoot
    try {
        & go build -trimpath -ldflags $ldflags -o $OutputPath ./cmd/server
        if ($LASTEXITCODE -ne 0) { Fail 'go build failed' }
    }
    finally {
        Pop-Location
    }
}

function Register-InstallerTask($BinaryPath, $ConfigPath) {
    $taskName = 'CLIProxyAPI'
    $action = New-ScheduledTaskAction -Execute $BinaryPath -Argument "-config `"$ConfigPath`""
    $trigger = New-ScheduledTaskTrigger -AtLogOn
    Register-ScheduledTask -TaskName $taskName -Action $action -Trigger $trigger -Force | Out-Null
    return $taskName
}

$installRoot = Expand-PathValue (Prompt-WithDefault 'Install location' $DefaultInstallRoot)
$authDir = Expand-PathValue (Prompt-WithDefault 'Auth folder' (Join-Path $installRoot 'auth'))
$buildNow = Confirm-YesNo 'Build binary from source now?' $true
$createTask = Confirm-YesNo 'Create startup task?' $true
$startTask = $false
if ($createTask) {
    $startTask = Confirm-YesNo 'Start after install?' $true
}

Ensure-Dir $installRoot
Ensure-Dir $authDir
Ensure-Dir (Join-Path $installRoot 'stats')
Ensure-Dir (Join-Path $installRoot 'logs')

$configPath = Join-Path $installRoot 'config.yaml'
$binaryPath = Join-Path $installRoot 'cli-proxy-api.exe'
$statsDbPath = Join-Path $installRoot 'stats\cache-statistics.sqlite'

Copy-IfMissing $DefaultSourceConfig $configPath
if (-not (Test-Path $configPath)) {
    @(
        "auth-dir: `"$authDir`"",
        'usage-statistics-enabled: true'
    ) | Set-Content -Path $configPath
}

if (Test-Path $DefaultSourceStatsDir) {
    Copy-IfMissing (Join-Path $DefaultSourceStatsDir 'cache-statistics.sqlite') $statsDbPath
}

$defaultAuthDir = Split-Path -Parent $DefaultSourceConfig
if (Test-Path $defaultAuthDir) {
    Merge-AuthDir $defaultAuthDir $authDir
}

if ($buildNow) {
    Build-Binary -RepoRoot $ScriptDir -OutputPath $binaryPath -ConfigPath $configPath
} elseif (Test-Path $binaryPath) {
    Fail "Build was skipped, but the existing binary at $binaryPath would hide source changes. Re-run install_windows.ps1 and choose to build from source."
} else {
    Fail "No existing binary found at $binaryPath and build was skipped."
}

if ($createTask) {
    $taskName = Register-InstallerTask -BinaryPath $binaryPath -ConfigPath $configPath
    if ($startTask) {
        Start-ScheduledTask -TaskName $taskName
    }
}

Say ''
Say 'Installation complete.'
Say "  Install root: $installRoot"
Say "  Binary: $binaryPath"
Say "  Config: $configPath"
Say "  Auth dir: $authDir"
Say "  Stats DB: $statsDbPath"
