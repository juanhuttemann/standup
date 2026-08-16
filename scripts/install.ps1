# standup installer - Windows (native PowerShell).
# Downloads the latest release binary from GitHub into %LOCALAPPDATA%\standup
# and adds that directory to the user PATH.
$ErrorActionPreference = 'Stop'

$repo = 'juanhuttemann/standup'
$arch = if ($env:PROCESSOR_ARCHITECTURE -eq 'ARM64') { 'arm64' } else { 'amd64' }
$dir = if ($env:STANDUP_BIN_DIR) { $env:STANDUP_BIN_DIR } else { Join-Path $env:LOCALAPPDATA 'standup' }
$url = "https://github.com/$repo/releases/latest/download/standup_windows_$arch.zip"
$sumUrl = "https://github.com/$repo/releases/latest/download/standup_checksums.txt"
$asset = "standup_windows_$arch.zip"

New-Item -ItemType Directory -Force -Path $dir | Out-Null
$zip = Join-Path $env:TEMP "standup_windows_$arch.zip"
Invoke-WebRequest -Uri $url -OutFile $zip

# A release without a matching checksum is not safe to install.
$sumPath = Join-Path $env:TEMP 'standup_checksums.txt'
Invoke-WebRequest -Uri $sumUrl -OutFile $sumPath
$matches = @(Get-Content $sumPath | Where-Object { ($_ -split '\s+')[-1] -eq $asset })
if ($matches.Count -ne 1) { throw "expected exactly one checksum for $asset" }
$want = (($matches[0] -split '\s+')[0]).ToLower()
if ($want -notmatch '^[0-9a-f]{64}$') { throw "invalid checksum for $asset" }
$got = (Get-FileHash -Path $zip -Algorithm SHA256).Hash.ToLower()
if ($got -ne $want) { throw "checksum mismatch for $asset" }

Expand-Archive -Path $zip -DestinationPath $dir -Force
Remove-Item $zip
Remove-Item $sumPath

# Check the persisted User PATH (not just this session's) so a directory
# already configured but missing from the current session is not appended
# twice.
$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
if ((($userPath -split ';') -notcontains $dir) -and ($dir -ne '')) {
    [Environment]::SetEnvironmentVariable('Path', $userPath + ';' + $dir, 'User')
}
if (($env:Path -split ';') -notcontains $dir) {
    $env:Path += ';' + $dir
}

Write-Host "installed $dir\standup.exe"
