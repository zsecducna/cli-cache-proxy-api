Param()

$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$InstallLabel = 'com.routerforme.cli-cache-proxy'
$DefaultInstallRoot = Join-Path $env:USERPROFILE '.cli-cache-proxy'
$DefaultSourceConfig = if ($env:CLI_PROXY_INSTALLER_SOURCE_CONFIG) { $env:CLI_PROXY_INSTALLER_SOURCE_CONFIG } else { 'C:\temp\cli-proxy-api-test\config.yaml' }
$DefaultSourceStatsDir = if ($env:CLI_PROXY_INSTALLER_SOURCE_STATS) { $env:CLI_PROXY_INSTALLER_SOURCE_STATS } else { Join-Path $env:USERPROFILE 'Desktop\CLIProxyAPI\stats' }
$SkipPostgresProvision = $env:CLI_PROXY_INSTALLER_SKIP_POSTGRES_PROVISION

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

function Read-EnvScalar($Path, $Key) {
    if (-not (Test-Path $Path)) { return $null }
    $line = Get-Content $Path | Where-Object { $_ -match "^\s*$([regex]::Escape($Key))=" } | Select-Object -Last 1
    if (-not $line) { return $null }
    $value = ($line -split '=', 2)[1].Trim()
    if ($value.Length -ge 2) {
        if (($value.StartsWith('"') -and $value.EndsWith('"')) -or ($value.StartsWith("'") -and $value.EndsWith("'"))) {
            $value = $value.Substring(1, $value.Length - 2)
        }
    }
    return $value
}

function Quote-EnvValue($Value) {
    '"' + $Value.Replace('\', '\\').Replace('"', '\"') + '"'
}

function Set-EnvScalar($Path, $Key, $Value) {
    $lines = @()
    if (Test-Path $Path) {
        $lines = [System.Collections.Generic.List[string]]::new()
        foreach ($line in Get-Content $Path) {
            if ($line -match "^\s*$([regex]::Escape($Key))=") {
                $lines.Add("$Key=$(Quote-EnvValue $Value)")
            }
            else {
                $lines.Add($line)
            }
        }
        if (-not ($lines | Where-Object { $_ -match "^\s*$([regex]::Escape($Key))=" })) {
            $lines.Add("$Key=$(Quote-EnvValue $Value)")
        }
    }
    else {
        $lines = @("$Key=$(Quote-EnvValue $Value)")
    }
    Set-Content -Path $Path -Value $lines
}

function Remove-EnvKeys($Path, [string[]]$Keys) {
    if (-not (Test-Path $Path)) { return }
    $lines = @(Get-Content $Path | Where-Object {
        $line = $_
        -not ($Keys | Where-Object { $line -match "^\s*$([regex]::Escape($_))=" })
    })
    if ($lines.Count -eq 0) {
        Remove-Item -Force $Path
        return
    }
    Set-Content -Path $Path -Value $lines
}

function Write-PgStoreEnv($Path, $Dsn, $Schema, $LocalPath) {
    Set-EnvScalar $Path 'PGSTORE_DSN' $Dsn
    Set-EnvScalar $Path 'PGSTORE_SCHEMA' $Schema
    Set-EnvScalar $Path 'PGSTORE_LOCAL_PATH' $LocalPath
}

function Clear-PgStoreEnv($Path) {
    Remove-EnvKeys $Path @('PGSTORE_DSN', 'PGSTORE_SCHEMA', 'PGSTORE_LOCAL_PATH')
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

function Prompt-RequiredValue($Label, $DefaultValue) {
    while ($true) {
        $value = (Prompt-WithDefault $Label $DefaultValue).Trim()
        if (-not [string]::IsNullOrWhiteSpace($value)) { return $value }
        Say 'A value is required.'
    }
}

function Require-PostgresTools {
    if (-not (Get-Command psql -ErrorAction SilentlyContinue)) {
        Fail 'Postgres setup requires psql. Install PostgreSQL client tools and rerun install_windows.ps1'
    }
}

function Parse-PostgresDbName($Dsn) {
    $prefix = ($Dsn -split '\?', 2)[0]
    if ($prefix -notmatch '^postgres(ql)?://') { return $null }
    $lastSlash = $prefix.LastIndexOf('/')
    if ($lastSlash -lt 0 -or $lastSlash -eq ($prefix.Length - 1)) { return $null }
    return $prefix.Substring($lastSlash + 1)
}

function Parse-PostgresUserName($Dsn) {
    $prefix = ($Dsn -split '\?', 2)[0]
    if ($prefix -notmatch '^postgres(ql)?://') { return $null }
    $schemeSep = $prefix.IndexOf('://')
    if ($schemeSep -lt 0) { return $null }
    $authority = $prefix.Substring($schemeSep + 3)
    $slashIndex = $authority.IndexOf('/')
    if ($slashIndex -ge 0) {
        $authority = $authority.Substring(0, $slashIndex)
    }
    if ($authority -notmatch '@') { return $null }
    $credentials = $authority.Substring(0, $authority.LastIndexOf('@'))
    if ([string]::IsNullOrWhiteSpace($credentials)) { return $null }
    $parts = $credentials.Split(':', 2)
    if ($parts.Length -lt 1 -or [string]::IsNullOrWhiteSpace($parts[0])) { return $null }
    return $parts[0]
}

function Parse-PostgresPassword($Dsn) {
    $prefix = ($Dsn -split '\?', 2)[0]
    if ($prefix -notmatch '^postgres(ql)?://') { return $null }
    $schemeSep = $prefix.IndexOf('://')
    if ($schemeSep -lt 0) { return $null }
    $authority = $prefix.Substring($schemeSep + 3)
    $slashIndex = $authority.IndexOf('/')
    if ($slashIndex -ge 0) {
        $authority = $authority.Substring(0, $slashIndex)
    }
    if ($authority -notmatch '@') { return $null }
    $credentials = $authority.Substring(0, $authority.LastIndexOf('@'))
    if ($credentials -notmatch ':') { return $null }
    return ($credentials.Split(':', 2))[1]
}

function Build-PostgresMaintenanceDsn($Dsn) {
    $parts = $Dsn -split '\?', 2
    $prefix = $parts[0]
    $query = if ($parts.Length -gt 1) { '?' + $parts[1] } else { '' }
    $lastSlash = $prefix.LastIndexOf('/')
    if ($lastSlash -lt 0) { return $null }
    return $prefix.Substring(0, $lastSlash + 1) + 'postgres' + $query
}

function Quote-PgIdentifier($Value) {
    '"' + $Value.Replace('"', '""') + '"'
}

function Show-PostgresManualInitCommands($TargetDsn, $MaintenanceDsn, $UserName, $Password, $DbName) {
    Say 'Could not provision Postgres automatically with the provided DSN.'
    Say 'Run these bash commands, then rerun install_windows.ps1:'
    Say "  psql `"$MaintenanceDsn`" -Atqc `"SELECT 1;`""
    if (-not [string]::IsNullOrWhiteSpace($UserName)) {
        $createRoleSql = "DO `$`$ BEGIN IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = '$($UserName.Replace(\"'\", \"''\"))') THEN CREATE ROLE $(Quote-PgIdentifier $UserName) LOGIN"
        if (-not [string]::IsNullOrWhiteSpace($Password)) {
            $createRoleSql += " PASSWORD '$($Password.Replace(\"'\", \"''\"))'"
        }
        $createRoleSql += "; END IF; END `$`$;"
        Say "  psql `"$MaintenanceDsn`" -v ON_ERROR_STOP=1 -c `"$createRoleSql`""
        $createDbSql = "SELECT 'CREATE DATABASE $(Quote-PgIdentifier $DbName) OWNER $(Quote-PgIdentifier $UserName)' WHERE NOT EXISTS (SELECT 1 FROM pg_database WHERE datname = '$($DbName.Replace(\"'\", \"''\"))') \gexec"
    }
    else {
        $createDbSql = "SELECT 'CREATE DATABASE $(Quote-PgIdentifier $DbName)' WHERE NOT EXISTS (SELECT 1 FROM pg_database WHERE datname = '$($DbName.Replace(\"'\", \"''\"))') \gexec"
    }
    Say "  psql `"$MaintenanceDsn`" -v ON_ERROR_STOP=1 -c `"$createDbSql`""
    Say "  psql `"$TargetDsn`" -Atqc `"SELECT 1;`""
}

function Ensure-PostgresDatabase($TargetDsn) {
    if ($SkipPostgresProvision -eq '1') {
        Say 'Skipping Postgres provisioning because CLI_PROXY_INSTALLER_SKIP_POSTGRES_PROVISION=1'
        return
    }
    $dbName = Parse-PostgresDbName $TargetDsn
    if ([string]::IsNullOrWhiteSpace($dbName)) {
        Fail "Could not determine database name from Postgres DSN: $TargetDsn"
    }
    $userName = Parse-PostgresUserName $TargetDsn
    $password = Parse-PostgresPassword $TargetDsn
    & psql $TargetDsn -Atqc 'SELECT 1;' *> $null
    if ($LASTEXITCODE -eq 0) {
        Say "Validated Postgres DSN and detected existing Postgres database $dbName"
        return
    }
    $maintenanceDsn = Build-PostgresMaintenanceDsn $TargetDsn
    if ([string]::IsNullOrWhiteSpace($maintenanceDsn)) {
        Fail "Could not derive maintenance DSN from Postgres DSN: $TargetDsn"
    }
    & psql $maintenanceDsn -Atqc 'SELECT 1;' *> $null
    if ($LASTEXITCODE -ne 0) {
        Show-PostgresManualInitCommands $TargetDsn $maintenanceDsn $userName $password $dbName
        Fail "Could not reach PostgreSQL maintenance database using $maintenanceDsn"
    }
    if (-not [string]::IsNullOrWhiteSpace($userName)) {
        $roleExists = (& psql $maintenanceDsn -Atqc "SELECT 1 FROM pg_roles WHERE rolname = '$($userName.Replace(\"'\", \"''\"))';" 2>$null)
        if ($LASTEXITCODE -ne 0 -or $roleExists -notmatch '^1\s*$') {
            Say "Creating Postgres role $userName"
            $createRoleSql = "DO `$`$ BEGIN IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = '$($userName.Replace(\"'\", \"''\"))') THEN CREATE ROLE $(Quote-PgIdentifier $userName) LOGIN"
            if (-not [string]::IsNullOrWhiteSpace($password)) {
                $createRoleSql += " PASSWORD '$($password.Replace(\"'\", \"''\"))'"
            }
            $createRoleSql += "; END IF; END `$`$;"
            & psql $maintenanceDsn -v ON_ERROR_STOP=1 -c $createRoleSql *> $null
            if ($LASTEXITCODE -ne 0) {
                Show-PostgresManualInitCommands $TargetDsn $maintenanceDsn $userName $password $dbName
                Fail "Failed creating Postgres role $userName using $maintenanceDsn"
            }
        }
    }
    $exists = (& psql $maintenanceDsn -Atqc "SELECT 1 FROM pg_database WHERE datname = '$($dbName.Replace(\"'\", \"''\"))';" 2>$null)
    if ($LASTEXITCODE -eq 0 -and $exists -match '^1$') {
        Show-PostgresManualInitCommands $TargetDsn $maintenanceDsn $userName $password $dbName
        Fail "Postgres database $dbName already exists but the target DSN is not reachable: $TargetDsn"
    }
    Say "Creating Postgres database $dbName"
    $createDbSql = "CREATE DATABASE $(Quote-PgIdentifier $dbName)"
    if (-not [string]::IsNullOrWhiteSpace($userName)) {
        $createDbSql += " OWNER $(Quote-PgIdentifier $userName)"
    }
    & psql $maintenanceDsn -v ON_ERROR_STOP=1 -c "$createDbSql;" *> $null
    if ($LASTEXITCODE -ne 0) {
        Show-PostgresManualInitCommands $TargetDsn $maintenanceDsn $userName $password $dbName
        Fail "Failed creating Postgres database $dbName using $maintenanceDsn"
    }
    & psql $TargetDsn -Atqc 'SELECT 1;' *> $null
    if ($LASTEXITCODE -ne 0) {
        Show-PostgresManualInitCommands $TargetDsn $maintenanceDsn $userName $password $dbName
        Fail "Created Postgres database $dbName but failed to connect using target DSN"
    }
    Say "Validated Postgres DSN after provisioning $dbName"
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

function Write-LauncherScript($InstallRoot, $BinaryPath, $ConfigPath) {
    $launcherPath = Join-Path $InstallRoot 'start-cli-proxy-api.cmd'
    @(
        '@echo off'
        'setlocal'
        "cd /d `"$InstallRoot`""
        "`"$BinaryPath`" -config `"$ConfigPath`""
    ) | Set-Content -Path $launcherPath
    return $launcherPath
}

function Register-InstallerTask($LauncherPath) {
    $taskName = 'CLIProxyAPI'
    $action = New-ScheduledTaskAction -Execute $LauncherPath
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
$envPath = Join-Path $installRoot '.env'
$launcherPath = Join-Path $installRoot 'start-cli-proxy-api.cmd'

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

$existingPgStoreDsn = Read-EnvScalar $envPath 'PGSTORE_DSN'
$existingPgStoreSchema = Read-EnvScalar $envPath 'PGSTORE_SCHEMA'
$existingPgStoreLocalPath = Read-EnvScalar $envPath 'PGSTORE_LOCAL_PATH'
if ([string]::IsNullOrWhiteSpace($existingPgStoreSchema)) { $existingPgStoreSchema = 'public' }
if ([string]::IsNullOrWhiteSpace($existingPgStoreLocalPath)) { $existingPgStoreLocalPath = $installRoot }
$postgresDefault = -not [string]::IsNullOrWhiteSpace($existingPgStoreDsn)
if (Confirm-YesNo 'Configure Postgres-backed auth/config/statistics store?' $postgresDefault) {
    Require-PostgresTools
    $pgStoreDsn = Prompt-RequiredValue 'Postgres DSN' $existingPgStoreDsn
    $pgStoreSchema = Prompt-RequiredValue 'Postgres schema' $existingPgStoreSchema
    $pgStoreLocalPath = Expand-PathValue (Prompt-RequiredValue 'Local migration seed path' $existingPgStoreLocalPath)
    Ensure-PostgresDatabase $pgStoreDsn
    Write-PgStoreEnv $envPath $pgStoreDsn $pgStoreSchema $pgStoreLocalPath
}
else {
    Clear-PgStoreEnv $envPath
}

if ($buildNow) {
    Build-Binary -RepoRoot $ScriptDir -OutputPath $binaryPath -ConfigPath $configPath
} elseif (Test-Path $binaryPath) {
    Fail "Build was skipped, but the existing binary at $binaryPath would hide source changes. Re-run install_windows.ps1 and choose to build from source."
} else {
    Fail "No existing binary found at $binaryPath and build was skipped."
}

$launcherPath = Write-LauncherScript -InstallRoot $installRoot -BinaryPath $binaryPath -ConfigPath $configPath

if ($createTask) {
    $taskName = Register-InstallerTask -LauncherPath $launcherPath
    if ($startTask) {
        Start-ScheduledTask -TaskName $taskName
    }
}

Say ''
Say 'Installation complete.'
Say "  Install root: $installRoot"
Say "  Binary: $binaryPath"
Say "  Launcher: $launcherPath"
Say "  Config: $configPath"
Say "  Auth dir: $authDir"
Say "  Stats DB: $statsDbPath"
if (Test-Path $envPath) {
    Say "  Postgres env: $envPath"
}
