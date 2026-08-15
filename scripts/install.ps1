# standup installer - Windows (native PowerShell).
# Downloads the latest release binary from GitHub into %LOCALAPPDATA%\standup
# and adds that directory to the user PATH.
$ErrorActionPreference = 'Stop'

$repo = 'juanhuttemann/standup'
$arch = if ($env:PROCESSOR_ARCHITECTURE -eq 'ARM64') { 'arm64' } else { 'amd64' }
$dir = if ($env:STANDUP_BIN_DIR) { $env:STANDUP_BIN_DIR } else { Join-Path $env:LOCALAPPDATA 'standup' }
$url = "https://github.com/$repo/releases/latest/download/standup_windows_$arch.zip"

New-Item -ItemType Directory -Force -Path $dir | Out-Null
$zip = Join-Path $env:TEMP "standup_windows_$arch.zip"
Invoke-WebRequest -Uri $url -OutFile $zip
Expand-Archive -Path $zip -DestinationPath $dir -Force
Remove-Item $zip

if (($env:Path -split ';') -notcontains $dir) {
    [Environment]::SetEnvironmentVariable('Path',
        [Environment]::GetEnvironmentVariable('Path', 'User') + ';' + $dir, 'User')
    $env:Path += ';' + $dir
}

Write-Host "installed $dir\standup.exe"
