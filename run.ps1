# run.ps1 - smith 开发用启动脚本
#
# 用法：
#   .\run.ps1                 # 默认：编译（如需要）+ 启动 GUI
#   .\run.ps1 -NoGui          # headless 烟雾测试 (发一条 "ver" → 退)
#   .\run.ps1 -Rebuild        # 强制重新编译
#   .\run.ps1 -Arch amd64     # 编译 64 位（默认 32 位，主战场是 Win7 PE）
#   .\run.ps1 -Key sk-...     # 临时 API key（覆盖 smith.ini；不推荐）
#   .\run.ps1 -- --extra      # 透传额外参数给 smith.exe
#
# 行为：
#   1. 把 Go 1.20.14 加到 PATH（默认 workbuddy 位置，失败则报清晰错）
#   2. 检查源码 mtime；比 .tmp/smith.exe 新则自动 rebuild
#   3. 启动 smith.exe，PID + 启动时间打印到控制台
#   4. smith.exe 退出后透传 exit code
#
# 设计原则：
#   - **不**编译到 dist/（dist/ 是发布产物，由 build.cmd 管）
#   - **不**污染用户环境（PATH 只在当前进程加，脚本结束就丢）
#   - **不**锁住窗口：smith.exe 自己开窗口，你 Ctrl+C 中断此脚本不会强杀 smith
#     （smith 收到 Ctrl+C 会 graceful 退出，因为有 WM_CLOSE handler）

[CmdletBinding()]
param(
    [switch]$NoGui = $false,
    [switch]$Rebuild = $false,
    [ValidateSet('386', 'amd64')]
    [string]$Arch = '386',
    [string]$Key = '',
    [Parameter(ValueFromRemainingArguments = $true)]
    [string[]]$ExtraArgs = @()
)

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

# ---- 2. 项目根 + 构建输出 ----
$scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$projectRoot = $scriptDir
Set-Location $projectRoot

$buildDir = Join-Path $projectRoot '.tmp'
if (-not (Test-Path $buildDir)) {
    New-Item -ItemType Directory -Path $buildDir -Force | Out-Null
}
$bin = Join-Path $buildDir "smith_$Arch.exe"

# ---- 3. 检查是否需要 rebuild ----
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

# ---- 4. 准备启动参数 ----
$args = @()
if ($NoGui) { $args += '--no-gui' }
if ($Key) { $args += '--key'; $args += $Key }
if ($ExtraArgs) { $args += $ExtraArgs }

# ---- 5. 启动 ----
Write-Host "[run] $bin $($args -join ' ')" -ForegroundColor Yellow
$proc = Start-Process -FilePath $bin -ArgumentList $args -PassThru -NoNewWindow
Write-Host "[run] PID=$($proc.Id) started at $(Get-Date -Format 'HH:mm:ss')"
Write-Host "[run] waiting for exit (Ctrl+C sends WM_CLOSE; smith 收到会 graceful 退出)..."
try {
    $proc.WaitForExit()
} catch {
    Write-Host "[run] interrupted, killing PID $($proc.Id)" -ForegroundColor Red
    Stop-Process -Id $proc.Id -Force
    exit 130
}
Write-Host "[run] smith exited with code $($proc.ExitCode)" -ForegroundColor Magenta
exit $proc.ExitCode
