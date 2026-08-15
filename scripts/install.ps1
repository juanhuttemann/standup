# standup installer - Windows (native PowerShell).
# Downloads the latest release binary from GitHub into %LOCALAPPDATA%\standup
# and adds that directory to the user PATH.
$ErrorActionPreference = 'Stop'

$repo = 'juanhuttemann/standup'
$arch = if ($env:PROCESSOR_ARCHITECTURE -eq 'ARM64') { 'arm64' } else { 'amd64' }
$dir = if ($env:STANDUP_BIN_DIR) { $env:STANDUP_BIN_DIR } else { Join-Path $env:LOCALAPPDATA 'standup' }
$url = "https://github.com/$repo/releases/latest/download/standup_windows_$arch.zip"
$sumUrl = "https://github.com/$repo/releases/latest/download/standup_checksums.txt"

New-Item -ItemType Directory -Force -Path $dir | Out-Null
$zip = Join-Path $env:TEMP "standup_windows_$arch.zip"
Invoke-WebRequest -Uri $url -OutFile $zip

# Verify the archive checksum when this release publishes one (older
# releases have only a versioned checksums file — skip with a note).
$sumPath = Join-Path $env:TEMP 'standup_checksums.txt'
try { Invoke-WebRequest -Uri $sumUrl -OutFile $sumPath -ErrorAction Stop } catch { $sumPath = $null }
if ($sumPath) {
    $want = (((Select-String -Path $sumPath -Pattern ([regex]::Escape("standup_windows_$arch.zip")) |
        Select-Object -First 1).Line -split '\s+')[0]).ToLower()
    $got = (Get-FileHash -Path $zip -Algorithm SHA256).Hash.ToLower()
    if ($got -ne $want) { throw "checksum mismatch for standup_windows_$arch.zip" }
} else {
    Write-Host 'note: no checksums file on this release, skipping verification'
}

Expand-Archive -Path $zip -DestinationPath $dir -Force
Remove-Item $zip

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
