# run.ps1 - smith dev launcher
#
# Usage:
#   .\run.ps1                 # build (if needed) + launch GUI
#   .\run.ps1 -NoGui          # headless smoke test
#   .\run.ps1 -Rebuild        # force rebuild
#   .\run.ps1 -Arch amd64     # 64-bit (default 32-bit)
#   .\run.ps1 -Key sk-...     # one-shot API key
#   .\run.ps1 -- --extra      # pass extra args to smith.exe

# Fix zh mojibake: Chinese Windows console is GBK (936) by default, Write-Host
# output shows as mojibake. Switch to UTF-8. (Note: must come AFTER param block
# because PowerShell requires param() to be the first non-comment statement.)
param(
    [switch]$NoGui = $false,
    [switch]$Rebuild = $false,
    [ValidateSet('386', 'amd64')]
    [string]$Arch = '386',
    [string]$Key = ''
)
# Pass-through extra args (avoid ValueFromRemainingArguments, that triggers
# a parameter binding error on empty invocation).
$ExtraArgs = @($args)

$OutputEncoding = [System.Text.Encoding]::UTF8
$psver = $PSVersionTable.PSVersion.Major
if ($psver -ge 6) {
    $console = [System.Console]
    $console.OutputEncoding = [System.Text.Encoding]::UTF8
}

$ErrorActionPreference = 'Stop'

# ---- 1. Go 1.20.14 ----
$goRootCandidates = @(
    'C:\Users\wrz20\.workbuddy\binaries\go\versions\1.20.14',
    'C:\go1.20.14',
    $env:GOROOT
)
$goRoot = $null
foreach ($p in $goRootCandidates) {
    if ($p -and (Test-Path (Join-Path $p 'bin\go.exe'))) {
        $goRoot = $p
        break
    }
}
if (-not $goRoot) {
    Write-Error "Go 1.20 not found. Tried: $($goRootCandidates -join ', '). Set GOROOT env or install to default path."
    exit 1
}
$env:Path = "$goRoot\bin;$env:Path"
$env:GOROOT = $goRoot

# ---- 2. project root + build output ----
$scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$projectRoot = $scriptDir
Set-Location $projectRoot

$buildDir = Join-Path $projectRoot '.tmp'
if (-not (Test-Path $buildDir)) {
    New-Item -ItemType Directory -Path $buildDir -Force | Out-Null
}
$bin = Join-Path $buildDir "smith_$Arch.exe"

# ---- 3. rebuild check ----
$env:GOOS = 'windows'
$env:GOARCH = $Arch
$env:CGO_ENABLED = '0'

$needBuild = $Rebuild -or -not (Test-Path $bin)
if (-not $needBuild) {
    $binTime = (Get-Item $bin).LastWriteTime
    $srcFiles = Get-ChildItem -Path 'src' -Recurse -Filter '*.go' -ErrorAction SilentlyContinue
    foreach ($f in $srcFiles) {
        if ($f.LastWriteTime -gt $binTime) {
            $needBuild = $true
            break
        }
    }
}
if (-not $needBuild) {
    foreach ($f in @('build.cmd', 'go.mod', 'go.sum', '.gitignore', 'smith.ini.example')) {
        if ((Test-Path $f) -and ((Get-Item $f).LastWriteTime -gt (Get-Item $bin).LastWriteTime)) {
            $needBuild = $true
            break
        }
    }
}

if ($needBuild) {
    Write-Host "[build] compiling smith ($Arch, CGO=0) -> $bin" -ForegroundColor Cyan
    go build -trimpath -ldflags '-s -w -H windowsgui' -o $bin .\src
    if ($LASTEXITCODE -ne 0) {
        Write-Error "build failed (exit $LASTEXITCODE)"
        exit $LASTEXITCODE
    }
    $size = (Get-Item $bin).Length
    Write-Host "[build] OK: $size bytes" -ForegroundColor Green
} else {
    Write-Host "[build] up to date, skip" -ForegroundColor DarkGray
}

# ---- 4. build arg list ----
$argList = @()
if ($NoGui) { $argList += '--no-gui' }
if ($Key) { $argList += '--key'; $argList += $Key }
if ($ExtraArgs) { $argList += $ExtraArgs }

# ---- 5. launch ----
# NOTE: Start-Process -ArgumentList @() throws "ParameterArgumentValidationError"
# in PowerShell. Workaround: omit -ArgumentList when empty.
Write-Host "[run] $bin $($argList -join ' ')" -ForegroundColor Yellow
if ($argList.Count -eq 0) {
    $proc = Start-Process -FilePath $bin -PassThru -NoNewWindow
} else {
    $proc = Start-Process -FilePath $bin -ArgumentList $argList -PassThru -NoNewWindow
}
Write-Host "[run] PID=$($proc.Id) started at $(Get-Date -Format 'HH:mm:ss')"
Write-Host "[run] waiting for exit (Ctrl+C sends WM_CLOSE; smith exits gracefully)..."
try {
    $proc.WaitForExit()
} catch {
    Write-Host "[run] interrupted, killing PID $($proc.Id)" -ForegroundColor Red
    Stop-Process -Id $proc.Id -Force
    exit 130
}
Write-Host "[run] smith exited with code $($proc.ExitCode)" -ForegroundColor Magenta
exit $proc.ExitCode
