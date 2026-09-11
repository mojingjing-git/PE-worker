@echo off
setlocal EnableExtensions EnableDelayedExpansion

rem ====================================================================
rem PE-agent build.cmd
rem
rem Usage:
rem   build.cmd            full build (vet + dual arch build)
rem   build.cmd test       also run go test ./...
rem   build.cmd clean      clean dist/
rem
rem Hard constraints (PLAN sec 6 + sec 0.7):
rem   - Go 1.20.x (must be 1.20, 1.21+ APIs not allowed)
rem   - CGO_ENABLED=0 (pure static, no extra DLL; PE image may lack them)
rem   - -H windowsgui (GUI mode, no black console)
rem   - -trimpath (strip local paths, reproducible output)
rem   - -ldflags "-s -w" (strip symbols + debug info, ~30% smaller)
rem   - No Go 1.21+ APIs (go -C / tls.VersionName / os/user /
rem     GetTickCount64 / GetVersionExA / RegGetValueA)
rem ====================================================================

rem change to script directory
cd /d "%~dp0"

rem GOROOT probe: env first, then workbuddy default
if not defined GOROOT (
    if exist "C:\Users\wrz20\.workbuddy\binaries\go\versions\1.20.14\bin\go.exe" (
        set "GOROOT=C:\Users\wrz20\.workbuddy\binaries\go\versions\1.20.14"
    ) else if exist "C:\go1.20.14\bin\go.exe" (
        set "GOROOT=C:\go1.20.14"
    ) else (
        echo [FATAL] Go 1.20 not found. Set GOROOT or install to default path.
        exit /b 1
    )
)
set "PATH=%GOROOT%\bin;%PATH%"

rem version assertion: must be 1.20 (not 1.21 / 2.x)
for /f "tokens=3" %%v in ('go version') do set "GOVER=%%v"
echo [INFO] using %GOVER% at %GOROOT%
echo %GOVER% | findstr /B "go1.20" >nul
if errorlevel 1 (
    echo [FATAL] need Go 1.20.x, got %GOVER%
    exit /b 1
)

rem banned API list (PLAN sec 0.7 / Go 1.20 to 1.21 differences)
rem 1.21+ APIs would fail to compile on 1.20; pre-grep to fail early.
rem Patterns require "(" or ".Call" after the name to skip "we don't use this" comments.
echo [INFO] scanning for Go 1.21+ API usage...
set "BANNED_HITS="
rem (1) Go 1.21+ stdlib additions
findstr /S /R /C:"tls\.VersionName" src\*.go >nul 2>&1
if not errorlevel 1 ( echo   [FAIL] tls.VersionName & set "BANNED_HITS=!BANNED_HITS! tls.VersionName" )
findstr /S /R /C:"os/user" src\*.go >nul 2>&1
if not errorlevel 1 ( echo   [FAIL] os/user import & set "BANNED_HITS=!BANNED_HITS! os/user" )
rem (2) Win32 APIs that fail in Win7 PE / return bad data without manifest
rem    Pattern: "GetTickCount64(" or "pGetTickCount64.Call" — skip comment-only mentions.
findstr /S /R /C:"GetTickCount64[ ]*(" src\*.go >nul 2>&1
if not errorlevel 1 ( echo   [FAIL] GetTickCount64( & set "BANNED_HITS=!BANNED_HITS! GetTickCount64" )
findstr /S /R /C:"pGetTickCount64\.Call" src\*.go >nul 2>&1
if not errorlevel 1 ( echo   [FAIL] pGetTickCount64.Call & set "BANNED_HITS=!BANNED_HITS! GetTickCount64" )
findstr /S /R /C:"GetVersionExA[ ]*(" src\*.go >nul 2>&1
if not errorlevel 1 ( echo   [FAIL] GetVersionExA( & set "BANNED_HITS=!BANNED_HITS! GetVersionExA" )
findstr /S /R /C:"RegGetValueA[ ]*(" src\*.go >nul 2>&1
if not errorlevel 1 ( echo   [FAIL] RegGetValueA( & set "BANNED_HITS=!BANNED_HITS! RegGetValueA" )
if defined BANNED_HITS (
    echo [FATAL] Banned APIs found:%BANNED_HITS%
    exit /b 1
)
echo [OK] no banned APIs

rem go vet (dual arch)
rem 注意：win/ 包用 uintptr<->unsafe.Pointer 互转是 v1-M1 契约必须的
rem （Win32 lparam/wparam 通过 syscall 返回的是 uintptr，但要当 Go 指针用）。
rem 配套的 win.KeepAlive() 保证 GC 不回收。vet 的 unsafeptr 检查对此
rem 不友好（认为"uintptr 可能不是 Go 指针"），但这里确实是安全的。
rem 所以 win/ 走 -unsafeptr=false，其它包严格走。
echo [INFO] go vet ...
set "CGO_ENABLED=0"
set "GOOS=windows"
set "GOARCH=386"
go vet -unsafeptr=false ./src/win/...
if errorlevel 1 ( echo [FATAL] vet 386 win failed & exit /b 1 )
go vet ./src/agent/... ./src/cfg/... ./src/logx/... ./src/tools/...
if errorlevel 1 ( echo [FATAL] vet 386 non-win failed & exit /b 1 )

set "GOARCH=amd64"
go vet -unsafeptr=false ./src/win/...
if errorlevel 1 ( echo [FATAL] vet amd64 win failed & exit /b 1 )
go vet ./src/agent/... ./src/cfg/... ./src/logx/... ./src/tools/...
if errorlevel 1 ( echo [FATAL] vet amd64 non-win failed & exit /b 1 )

if /I "%1"=="test" (
    echo [INFO] go test ...
    set "GOARCH=386"
    go test -count=1 ./src/...
    if errorlevel 1 ( echo [FATAL] test 386 failed & exit /b 1 )
    set "GOARCH=amd64"
    go test -count=1 ./src/...
    if errorlevel 1 ( echo [FATAL] test amd64 failed & exit /b 1 )
)

if /I "%1"=="clean" (
    if exist dist rmdir /S /Q dist
    mkdir dist
    echo [OK] dist/ cleaned
    exit /b 0
)

rem dual arch build
if not exist dist mkdir dist

echo [INFO] building 386 ...
set "GOARCH=386"
go build -trimpath -ldflags "-s -w -H windowsgui" -o dist\smith.exe .\src
if errorlevel 1 ( echo [FATAL] build 386 failed & exit /b 1 )

echo [INFO] building amd64 ...
set "GOARCH=amd64"
go build -trimpath -ldflags "-s -w -H windowsgui" -o dist\smith64.exe .\src
if errorlevel 1 ( echo [FATAL] build amd64 failed & exit /b 1 )

rem size self-report
for %%A in (dist\smith.exe dist\smith64.exe) do (
    for %%S in (%%~zA) do echo [INFO] %%~nxA = %%S bytes
)

echo [OK] build complete: dist\smith.exe + dist\smith64.exe
endlocal
