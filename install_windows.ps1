Param()

$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$InstallLabel = 'com.routerforme.cli-cache-proxy'
$DefaultInstallRoot = Join-Path $env:USERPROFILE '.cli-cache-proxy'
$DefaultSourceConfig = if ($env:CLI_PROXY_INSTALLER_SOURCE_CONFIG) { $env:CLI_PROXY_INSTALLER_SOURCE_CONFIG } else { 'C:\temp\cli-proxy-api-test\config.yaml' }
$DefaultSourceStatsDir = if ($env:CLI_PROXY_INSTALLER_SOURCE_STATS) { $env:CLI_PROXY_INSTALLER_SOURCE_STATS } else { Join-Path $env:USERPROFILE 'Desktop\CLIProxyAPI\stats' }
$SkipPostgresProvision = $env:CLI_PROXY_INSTALLER_SKIP_POSTGRES_PROVISION
$PostgresMajorVersion = if ($env:CLI_PROXY_INSTALLER_POSTGRES_MAJOR_VERSION) { $env:CLI_PROXY_INSTALLER_POSTGRES_MAJOR_VERSION } else { '17' }
$PostgresInstallerVersion = if ($env:CLI_PROXY_INSTALLER_POSTGRES_INSTALLER_VERSION) { $env:CLI_PROXY_INSTALLER_POSTGRES_INSTALLER_VERSION } else { '17.9-1' }
$PostgresInstallerUrl = if ($env:CLI_PROXY_INSTALLER_POSTGRES_INSTALLER_URL) { $env:CLI_PROXY_INSTALLER_POSTGRES_INSTALLER_URL } else { "https://get.enterprisedb.com/postgresql/postgresql-$PostgresInstallerVersion-windows-x64.exe" }
$PostgresInstallRoot = if ($env:CLI_PROXY_INSTALLER_POSTGRES_INSTALL_ROOT) { $env:CLI_PROXY_INSTALLER_POSTGRES_INSTALL_ROOT } else { Join-Path $env:ProgramFiles "PostgreSQL\$PostgresMajorVersion" }
$PostgresDataDir = if ($env:CLI_PROXY_INSTALLER_POSTGRES_DATA_DIR) { $env:CLI_PROXY_INSTALLER_POSTGRES_DATA_DIR } else { Join-Path $PostgresInstallRoot 'data' }
$PostgresServiceName = if ($env:CLI_PROXY_INSTALLER_POSTGRES_SERVICE_NAME) { $env:CLI_PROXY_INSTALLER_POSTGRES_SERVICE_NAME } else { 'postgresql-cliproxyapi' }
$PostgresServiceAccount = if ($env:CLI_PROXY_INSTALLER_POSTGRES_SERVICE_ACCOUNT) { $env:CLI_PROXY_INSTALLER_POSTGRES_SERVICE_ACCOUNT } else { 'postgres' }
$PostgresServicePassword = if ($env:CLI_PROXY_INSTALLER_POSTGRES_SERVICE_PASSWORD) { $env:CLI_PROXY_INSTALLER_POSTGRES_SERVICE_PASSWORD } else { $null }
$PostgresAdminUser = if ($env:CLI_PROXY_INSTALLER_POSTGRES_ADMIN_USER) { $env:CLI_PROXY_INSTALLER_POSTGRES_ADMIN_USER } else { 'postgres' }
$PostgresAdminPassword = if ($env:CLI_PROXY_INSTALLER_POSTGRES_ADMIN_PASSWORD) { $env:CLI_PROXY_INSTALLER_POSTGRES_ADMIN_PASSWORD } else { $null }
$script:ManagedPostgresInstallRoot = $null
$script:ManagedPostgresServiceName = $PostgresServiceName
$script:ManagedPostgresAdminUser = $PostgresAdminUser
$script:ManagedPostgresAdminPassword = $PostgresAdminPassword
$script:PostgresPsqlPath = $null
$script:PostgresPgIsReadyPath = $null

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

function Set-ConfigAuthDir($Path, $AuthDir) {
    if (-not (Test-Path $Path)) { return }
    # YAML double-quoted scalars treat backslash as an escape character, so a Windows path like
    # C:\Users must be written C:\\Users (and embedded quotes escaped) to round-trip correctly.
    $yamlValue = $AuthDir.Replace('\', '\\').Replace('"', '\"')
    $line = "auth-dir: `"$yamlValue`""
    $content = Get-Content -Path $Path
    # auth-dir is a top-level key; anchor to column 0 so nested mapping keys are never rewritten.
    if ($content -match '^auth-dir\s*:') {
        $content = $content -replace '^auth-dir\s*:.*$', $line
    }
    else {
        $content = @($line) + $content
    }
    Set-Content -Path $Path -Value $content -Encoding utf8
}

function New-ManagementKey {
    # Cryptographically strong 16-byte hex key for control-panel login.
    $bytes = New-Object byte[] 16
    [System.Security.Cryptography.RandomNumberGenerator]::Create().GetBytes($bytes)
    return -join ($bytes | ForEach-Object { $_.ToString('x2') })
}

function Ensure-ManagementSecret($Path, $Key) {
    # Seed a management key so the control-panel routes are mounted. Without a secret-key (or
    # MANAGEMENT_PASSWORD / local password) the server returns 404 for all /v0/management routes and
    # panel login fails. Only seed when the config has no remote-management block, so configs copied
    # from config.example.yaml (which already ship a secret-key) stay untouched and no duplicate key
    # is written.
    if ([string]::IsNullOrWhiteSpace($Key)) { return }
    if (-not (Test-Path $Path)) { return }
    $content = Get-Content -Path $Path
    if ($content -match '^\s*remote-management\s*:') { return }
    $yamlValue = $Key.Replace('\', '\\').Replace('"', '\"')
    $block = @('remote-management:', "  secret-key: `"$yamlValue`"")
    Set-Content -Path $Path -Value ($block + $content) -Encoding utf8
    Say "Seeded management panel key into $Path"
}

function Get-ConfigSecretKey($Path) {
    if (-not (Test-Path $Path)) { return '' }
    foreach ($line in Get-Content -Path $Path) {
        if ($line -match '^\s*secret-key\s*:\s*"?([^"\s]+)"?') { return $Matches[1] }
    }
    return ''
}

function Prompt-RequiredValue($Label, $DefaultValue) {
    while ($true) {
        $value = (Prompt-WithDefault $Label $DefaultValue).Trim()
        if (-not [string]::IsNullOrWhiteSpace($value)) { return $value }
        Say 'A value is required.'
    }
}

function Parse-PostgresUri($Dsn) {
    try {
        $uri = [Uri]$Dsn
    }
    catch {
        return $null
    }
    if ($uri.Scheme -notin @('postgres', 'postgresql')) { return $null }
    return $uri
}

function Parse-PostgresHost($Dsn) {
    $uri = Parse-PostgresUri $Dsn
    if (-not $uri) { return $null }
    return $uri.Host
}

function Parse-PostgresPort($Dsn) {
    $uri = Parse-PostgresUri $Dsn
    if (-not $uri) { return 5432 }
    if ($uri.Port -gt 0) { return $uri.Port }
    return 5432
}

function Test-LocalPostgresDsn($Dsn) {
    $host = Parse-PostgresHost $Dsn
    return $host -in @('', 'localhost', '127.0.0.1', '::1')
}

function New-RandomPassword {
    $upper = 'ABCDEFGHJKLMNPQRSTUVWXYZ'
    $lower = 'abcdefghijkmnopqrstuvwxyz'
    $digit = '23456789'
    $special = '!@$%^*-_+='
    $all = ($upper + $lower + $digit + $special).ToCharArray()
    $passwordChars = [System.Collections.Generic.List[char]]::new()
    $passwordChars.Add($upper[(Get-Random -Maximum $upper.Length)])
    $passwordChars.Add($lower[(Get-Random -Maximum $lower.Length)])
    $passwordChars.Add($digit[(Get-Random -Maximum $digit.Length)])
    $passwordChars.Add($special[(Get-Random -Maximum $special.Length)])
    while ($passwordChars.Count -lt 24) {
        $passwordChars.Add($all[(Get-Random -Maximum $all.Length)])
    }

    return -join ($passwordChars | Sort-Object { Get-Random })
}

function Resolve-PostgresTool($ToolName) {
    $cachedPath = if ($ToolName -eq 'psql') { $script:PostgresPsqlPath } else { $script:PostgresPgIsReadyPath }
    if (-not [string]::IsNullOrWhiteSpace($cachedPath) -and (Test-Path $cachedPath)) {
        return $cachedPath
    }

    $command = Get-Command $ToolName -ErrorAction SilentlyContinue
    if ($command -and -not [string]::IsNullOrWhiteSpace($command.Source)) {
        if ($ToolName -eq 'psql') {
            $script:PostgresPsqlPath = $command.Source
        }
        else {
            $script:PostgresPgIsReadyPath = $command.Source
        }
        return $command.Source
    }

    foreach ($root in @($script:ManagedPostgresInstallRoot, $PostgresInstallRoot)) {
        if ([string]::IsNullOrWhiteSpace($root)) { continue }
        $candidate = Join-Path $root "bin\$ToolName.exe"
        if (Test-Path $candidate) {
            if ($ToolName -eq 'psql') {
                $script:PostgresPsqlPath = $candidate
            }
            else {
                $script:PostgresPgIsReadyPath = $candidate
            }
            return $candidate
        }
    }

    return $null
}

function Test-IsAdministrator {
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    $principal = New-Object Security.Principal.WindowsPrincipal($identity)
    return $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
}

function Get-PostgresQuerySuffix($Dsn) {
    $parts = $Dsn -split '\?', 2
    if ($parts.Length -gt 1 -and -not [string]::IsNullOrWhiteSpace($parts[1])) {
        return '?' + $parts[1]
    }
    return ''
}

function Build-PostgresConnectionDsn($Host, $Port, $Database, $UserName, $Password, $QuerySuffix) {
    if ([string]::IsNullOrWhiteSpace($Host)) { $Host = 'localhost' }
    if ([string]::IsNullOrWhiteSpace($Database)) { $Database = 'postgres' }
    if ([string]::IsNullOrWhiteSpace($Port)) { $Port = '5432' }
    $encodedUser = [Uri]::EscapeDataString($UserName)
    $encodedPassword = [Uri]::EscapeDataString($Password)
    $hostPart = if ($Host.Contains(':') -and -not $Host.StartsWith('[')) { "[$Host]" } else { $Host }
    return "postgresql://${encodedUser}:${encodedPassword}@${hostPart}:${Port}/${Database}${QuerySuffix}"
}

function Build-ManagedPostgresMaintenanceDsn($TargetDsn) {
    if ([string]::IsNullOrWhiteSpace($script:ManagedPostgresAdminUser) -or [string]::IsNullOrWhiteSpace($script:ManagedPostgresAdminPassword)) {
        return $null
    }
    $host = Parse-PostgresHost $TargetDsn
    if ([string]::IsNullOrWhiteSpace($host)) { $host = 'localhost' }
    $port = [string](Parse-PostgresPort $TargetDsn)
    return Build-PostgresConnectionDsn $host $port 'postgres' $script:ManagedPostgresAdminUser $script:ManagedPostgresAdminPassword (Get-PostgresQuerySuffix $TargetDsn)
}

function Wait-ManagedPostgresReady($TargetDsn) {
    $service = Get-Service -Name $script:ManagedPostgresServiceName -ErrorAction SilentlyContinue
    if (-not $service) {
        Fail "PostgreSQL Windows service $($script:ManagedPostgresServiceName) was not found after installation"
    }
    if ($service.Status -ne [System.ServiceProcess.ServiceControllerStatus]::Running) {
        Say "Starting PostgreSQL Windows service $($script:ManagedPostgresServiceName)"
        Start-Service -Name $script:ManagedPostgresServiceName
        try {
            $service.WaitForStatus([System.ServiceProcess.ServiceControllerStatus]::Running, [TimeSpan]::FromSeconds(60))
        }
        catch {
            Fail "PostgreSQL Windows service $($script:ManagedPostgresServiceName) did not reach Running state within 60 seconds"
        }
    }

    $pgIsReadyPath = Resolve-PostgresTool 'pg_isready'
    if (-not $pgIsReadyPath) {
        Fail 'PostgreSQL installation completed, but pg_isready.exe was not found'
    }

    $host = Parse-PostgresHost $TargetDsn
    if ([string]::IsNullOrWhiteSpace($host)) { $host = 'localhost' }
    $port = [string](Parse-PostgresPort $TargetDsn)
    $userName = $script:ManagedPostgresAdminUser
    if ([string]::IsNullOrWhiteSpace($userName)) {
        $userName = Parse-PostgresUserName $TargetDsn
    }
    if ([string]::IsNullOrWhiteSpace($userName)) { $userName = 'postgres' }
    $deadline = (Get-Date).AddSeconds(90)
    while ((Get-Date) -lt $deadline) {
        & $pgIsReadyPath -h $host -p $port -U $userName -q -t 3 *> $null
        if ($LASTEXITCODE -eq 0) {
            Say "PostgreSQL Windows service $($script:ManagedPostgresServiceName) is accepting connections on $host`:$port"
            return
        }
        Start-Sleep -Seconds 2
    }

    Fail "PostgreSQL Windows service $($script:ManagedPostgresServiceName) did not become ready on $host`:$port within 90 seconds"
}

function Install-ManagedPostgres($TargetDsn) {
    if (-not (Test-LocalPostgresDsn $TargetDsn)) {
        Fail 'Automatic PostgreSQL installation is only supported for localhost/127.0.0.1 DSNs'
    }
    if (-not (Test-IsAdministrator)) {
        Fail 'Automatic PostgreSQL installation on Windows requires running install_windows.ps1 from an elevated PowerShell session'
    }

    $script:ManagedPostgresInstallRoot = Expand-PathValue $PostgresInstallRoot
    $managedDataDir = Expand-PathValue $PostgresDataDir
    $script:ManagedPostgresServiceName = $PostgresServiceName
    if ([string]::IsNullOrWhiteSpace($script:ManagedPostgresAdminUser)) {
        $script:ManagedPostgresAdminUser = 'postgres'
    }
    if ([string]::IsNullOrWhiteSpace($script:ManagedPostgresAdminPassword)) {
        $script:ManagedPostgresAdminPassword = New-RandomPassword
    }
    $servicePassword = $PostgresServicePassword
    if ([string]::IsNullOrWhiteSpace($servicePassword)) {
        $servicePassword = New-RandomPassword
    }

    $existingService = Get-Service -Name $script:ManagedPostgresServiceName -ErrorAction SilentlyContinue
    $existingPsql = Resolve-PostgresTool 'psql'
    $existingPgIsReady = Resolve-PostgresTool 'pg_isready'
    if ($existingService -and $existingPsql -and $existingPgIsReady) {
        Say "Using existing PostgreSQL Windows service $($script:ManagedPostgresServiceName)"
        Wait-ManagedPostgresReady $TargetDsn
        return
    }

    $installerUri = [Uri]$PostgresInstallerUrl
    $installerPath = Join-Path ([System.IO.Path]::GetTempPath()) ([System.IO.Path]::GetFileName($installerUri.LocalPath))
    $installerLogPath = Join-Path ([System.IO.Path]::GetTempPath()) "cliproxy-postgres-install-$PostgresInstallerVersion.log"
    Ensure-Dir (Split-Path -Parent $script:ManagedPostgresInstallRoot)
    Ensure-Dir (Split-Path -Parent $managedDataDir)

    if (-not (Test-Path $installerPath)) {
        Say "Downloading PostgreSQL installer from $PostgresInstallerUrl"
        Invoke-WebRequest -Uri $PostgresInstallerUrl -OutFile $installerPath
    }

    Say "Installing PostgreSQL $PostgresInstallerVersion as Windows service $($script:ManagedPostgresServiceName)"
    $installerArgs = @(
        '--mode', 'unattended',
        '--unattendedmodeui', 'none',
        '--enable-components', 'server,commandlinetools',
        '--disable-components', 'pgAdmin,stackbuilder',
        '--prefix', $script:ManagedPostgresInstallRoot,
        '--datadir', $managedDataDir,
        '--serverport', ([string](Parse-PostgresPort $TargetDsn)),
        '--serviceaccount', $PostgresServiceAccount,
        '--servicename', $script:ManagedPostgresServiceName,
        '--servicepassword', $servicePassword,
        '--superaccount', $script:ManagedPostgresAdminUser,
        '--superpassword', $script:ManagedPostgresAdminPassword,
        '--enable_acledit', '1',
        '--install_runtimes', '1',
        '--debugtrace', $installerLogPath
    )
    $installer = Start-Process -FilePath $installerPath -ArgumentList $installerArgs -Wait -PassThru
    if ($installer.ExitCode -ne 0) {
        if (Test-Path $installerLogPath) {
            Warn "PostgreSQL installer trace: $installerLogPath"
        }
        Fail "PostgreSQL installer exited with code $($installer.ExitCode)"
    }

    $script:PostgresPsqlPath = Join-Path $script:ManagedPostgresInstallRoot 'bin\psql.exe'
    $script:PostgresPgIsReadyPath = Join-Path $script:ManagedPostgresInstallRoot 'bin\pg_isready.exe'
    if (-not (Test-Path $script:PostgresPsqlPath)) {
        Fail "PostgreSQL installation completed, but psql.exe was not found at $($script:PostgresPsqlPath)"
    }
    if (-not (Test-Path $script:PostgresPgIsReadyPath)) {
        Fail "PostgreSQL installation completed, but pg_isready.exe was not found at $($script:PostgresPgIsReadyPath)"
    }

    Wait-ManagedPostgresReady $TargetDsn
    Say "Installed PostgreSQL as Windows service $($script:ManagedPostgresServiceName)"
}

function Require-PostgresTools($TargetDsn) {
    $psqlPath = Resolve-PostgresTool 'psql'
    if ($psqlPath) { return }
    if (-not (Test-LocalPostgresDsn $TargetDsn)) {
        Fail 'Postgres setup requires psql. Install PostgreSQL client tools and rerun install_windows.ps1'
    }
    Install-ManagedPostgres $TargetDsn
}

function Parse-PostgresDbName($Dsn) {
    $prefix = ($Dsn -split '\?', 2)[0]
    if ($prefix -notmatch '^postgres(ql)?://') { return $null }
    $lastSlash = $prefix.LastIndexOf('/')
    if ($lastSlash -lt 0 -or $lastSlash -eq ($prefix.Length - 1)) { return $null }
    return $prefix.Substring($lastSlash + 1)
}

function Parse-PostgresUserName($Dsn) {
    $uri = Parse-PostgresUri $Dsn
    if ($uri -and -not [string]::IsNullOrWhiteSpace($uri.UserInfo)) {
        $parts = $uri.UserInfo.Split(':', 2)
        if ($parts.Length -ge 1 -and -not [string]::IsNullOrWhiteSpace($parts[0])) {
            return [Uri]::UnescapeDataString($parts[0])
        }
    }
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
    $uri = Parse-PostgresUri $Dsn
    if ($uri -and -not [string]::IsNullOrWhiteSpace($uri.UserInfo)) {
        $parts = $uri.UserInfo.Split(':', 2)
        if ($parts.Length -eq 2) {
            return [Uri]::UnescapeDataString($parts[1])
        }
    }
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

function Resolve-PostgresMaintenanceDsn($TargetDsn) {
    $managedDsn = $null
    if (Test-LocalPostgresDsn $TargetDsn) {
        $managedDsn = Build-ManagedPostgresMaintenanceDsn $TargetDsn
    }
    if (-not [string]::IsNullOrWhiteSpace($managedDsn)) {
        return $managedDsn
    }
    return Build-PostgresMaintenanceDsn $TargetDsn
}

function Quote-PgIdentifier($Value) {
    '"' + $Value.Replace('"', '""') + '"'
}

function Show-PostgresManualInitCommands($TargetDsn, $MaintenanceDsn, $UserName, $Password, $DbName) {
    Say 'Could not provision Postgres automatically with the provided DSN.'
    Say 'Run these commands, then rerun install_windows.ps1:'
    Say "  psql `"$MaintenanceDsn`" -Atqc `"SELECT 1;`""
    if (-not [string]::IsNullOrWhiteSpace($UserName)) {
        $createRoleSql = "DO `$`$ BEGIN IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = '$($UserName.Replace("'", "''"))') THEN CREATE ROLE $(Quote-PgIdentifier $UserName) LOGIN"
        if (-not [string]::IsNullOrWhiteSpace($Password)) {
            $createRoleSql += " PASSWORD '$($Password.Replace("'", "''"))'"
        }
        $createRoleSql += "; END IF; END `$`$;"
        Say "  psql `"$MaintenanceDsn`" -v ON_ERROR_STOP=1 -c `"$createRoleSql`""
        $createDbSql = "SELECT 'CREATE DATABASE $(Quote-PgIdentifier $DbName) OWNER $(Quote-PgIdentifier $UserName)' WHERE NOT EXISTS (SELECT 1 FROM pg_database WHERE datname = '$($DbName.Replace("'", "''"))') \gexec"
    }
    else {
        $createDbSql = "SELECT 'CREATE DATABASE $(Quote-PgIdentifier $DbName)' WHERE NOT EXISTS (SELECT 1 FROM pg_database WHERE datname = '$($DbName.Replace("'", "''"))') \gexec"
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
    & (Resolve-PostgresTool 'psql') $TargetDsn -Atqc 'SELECT 1;' *> $null
    if ($LASTEXITCODE -eq 0) {
        Say "Validated Postgres DSN and detected existing Postgres database $dbName"
        return
    }
    $maintenanceDsn = Resolve-PostgresMaintenanceDsn $TargetDsn
    if ([string]::IsNullOrWhiteSpace($maintenanceDsn)) {
        Fail "Could not derive maintenance DSN from Postgres DSN: $TargetDsn"
    }
    & (Resolve-PostgresTool 'psql') $maintenanceDsn -Atqc 'SELECT 1;' *> $null
    if ($LASTEXITCODE -ne 0 -and (Test-LocalPostgresDsn $TargetDsn)) {
        Say 'Local PostgreSQL service is not reachable. Installing or starting the managed PostgreSQL service.'
        Install-ManagedPostgres $TargetDsn
        $maintenanceDsn = Resolve-PostgresMaintenanceDsn $TargetDsn
        & (Resolve-PostgresTool 'psql') $maintenanceDsn -Atqc 'SELECT 1;' *> $null
    }
    if ($LASTEXITCODE -ne 0) {
        Show-PostgresManualInitCommands $TargetDsn $maintenanceDsn $userName $password $dbName
        Fail "Could not reach PostgreSQL maintenance database using $maintenanceDsn"
    }
    if (-not [string]::IsNullOrWhiteSpace($userName)) {
        $roleExists = (& (Resolve-PostgresTool 'psql') $maintenanceDsn -Atqc "SELECT 1 FROM pg_roles WHERE rolname = '$($userName.Replace("'", "''"))';" 2>$null)
        if ($LASTEXITCODE -ne 0 -or $roleExists -notmatch '^1\s*$') {
            Say "Creating Postgres role $userName"
            $createRoleSql = "DO `$`$ BEGIN IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = '$($userName.Replace("'", "''"))') THEN CREATE ROLE $(Quote-PgIdentifier $userName) LOGIN"
            if (-not [string]::IsNullOrWhiteSpace($password)) {
                $createRoleSql += " PASSWORD '$($password.Replace("'", "''"))'"
            }
            $createRoleSql += "; END IF; END `$`$;"
            & (Resolve-PostgresTool 'psql') $maintenanceDsn -v ON_ERROR_STOP=1 -c $createRoleSql *> $null
            if ($LASTEXITCODE -ne 0) {
                Show-PostgresManualInitCommands $TargetDsn $maintenanceDsn $userName $password $dbName
                Fail "Failed creating Postgres role $userName using $maintenanceDsn"
            }
        }
    }
    $exists = (& (Resolve-PostgresTool 'psql') $maintenanceDsn -Atqc "SELECT 1 FROM pg_database WHERE datname = '$($dbName.Replace("'", "''"))';" 2>$null)
    if ($LASTEXITCODE -eq 0 -and $exists -match '^1$') {
        Show-PostgresManualInitCommands $TargetDsn $maintenanceDsn $userName $password $dbName
        Fail "Postgres database $dbName already exists but the target DSN is not reachable: $TargetDsn"
    }
    Say "Creating Postgres database $dbName"
    $createDbSql = "CREATE DATABASE $(Quote-PgIdentifier $dbName)"
    if (-not [string]::IsNullOrWhiteSpace($userName)) {
        $createDbSql += " OWNER $(Quote-PgIdentifier $userName)"
    }
    & (Resolve-PostgresTool 'psql') $maintenanceDsn -v ON_ERROR_STOP=1 -c "$createDbSql;" *> $null
    if ($LASTEXITCODE -ne 0) {
        Show-PostgresManualInitCommands $TargetDsn $maintenanceDsn $userName $password $dbName
        Fail "Failed creating Postgres database $dbName using $maintenanceDsn"
    }
    & (Resolve-PostgresTool 'psql') $TargetDsn -Atqc 'SELECT 1;' *> $null
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
$authDir = Expand-PathValue (Prompt-WithDefault 'Auth folder' (Join-Path $installRoot 'auths'))
$managementKey = Prompt-WithDefault 'Management panel key (control-panel login)' (New-ManagementKey)
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

# Recover settings from a prior Postgres-backed install when no flat config exists yet, so that
# declining Postgres later does not lose the operator's configuration.
$pgStoreConfig = Join-Path $installRoot 'pgstore\config\config.yaml'
if ((-not (Test-Path $configPath)) -and (Test-Path $pgStoreConfig)) {
    Copy-Item -Force $pgStoreConfig $configPath
}
Copy-IfMissing $DefaultSourceConfig $configPath
if (-not (Test-Path $configPath)) {
    @(
        'port: 8317',
        "auth-dir: `"$authDir`"",
        'usage-statistics-enabled: true'
    ) | Set-Content -Path $configPath
}
# Point the active config at the chosen auth folder regardless of the template/imported default.
Set-ConfigAuthDir $configPath $authDir
Ensure-ManagementSecret $configPath $managementKey

if (Test-Path $DefaultSourceStatsDir) {
    Copy-IfMissing (Join-Path $DefaultSourceStatsDir 'cache-statistics.sqlite') $statsDbPath
}

# Migrate credentials from prior auth layouts into the (now plural) auths target. Merge-AuthDir
# is a no-op when the source is missing or equals the target, so these are always safe to call:
#   <bundled default config folder>  upgrade from the shipped default location
#   <root>\auth                       legacy flat layout (singular)
#   <root>\pgstore\auths              Postgres-backed seed layout
$defaultAuthDir = Split-Path -Parent $DefaultSourceConfig
Merge-AuthDir $defaultAuthDir $authDir
Merge-AuthDir (Join-Path $installRoot 'auth') $authDir
Merge-AuthDir (Join-Path $installRoot 'pgstore\auths') $authDir

$existingPgStoreDsn = Read-EnvScalar $envPath 'PGSTORE_DSN'
$existingPgStoreSchema = Read-EnvScalar $envPath 'PGSTORE_SCHEMA'
$existingPgStoreLocalPath = Read-EnvScalar $envPath 'PGSTORE_LOCAL_PATH'
if ([string]::IsNullOrWhiteSpace($existingPgStoreSchema)) { $existingPgStoreSchema = 'public' }
if ([string]::IsNullOrWhiteSpace($existingPgStoreLocalPath)) { $existingPgStoreLocalPath = $installRoot }
$postgresDefault = -not [string]::IsNullOrWhiteSpace($existingPgStoreDsn)
if (Confirm-YesNo 'Configure Postgres-backed auth/config/statistics store?' $postgresDefault) {
    $pgStoreDsn = Prompt-RequiredValue 'Postgres DSN' $existingPgStoreDsn
    $pgStoreSchema = Prompt-RequiredValue 'Postgres schema' $existingPgStoreSchema
    $pgStoreLocalPath = Expand-PathValue (Prompt-RequiredValue 'Local migration seed path' $existingPgStoreLocalPath)
    Require-PostgresTools $pgStoreDsn
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
$mgmtKey = Get-ConfigSecretKey $configPath
if (-not [string]::IsNullOrWhiteSpace($mgmtKey)) {
    Say "  Management key (panel login): $mgmtKey"
}
Say "  Stats DB: $statsDbPath"
if (Test-Path $envPath) {
    Say "  Postgres env: $envPath"
}
