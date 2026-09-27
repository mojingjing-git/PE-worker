@echo off
setlocal EnableExtensions EnableDelayedExpansion

rem ====================================================================
rem PE-agent build.cmd
rem
rem Usage:
rem   build.cmd            full build (vet + gofmt + dual arch build + PE verify)
rem   build.cmd test       ...plus go test ./src/... against the FRESH artifacts
rem   build.cmd clean      clean dist/
rem
rem !! THIS FILE MUST BE SAVED WITH CRLF LINE ENDINGS.
rem    cmd.exe requires CRLF. With LF-only line endings it consumes ~7 leading
rem    bytes of every line (the UTF-8 length of the project's Chinese path
rem    prefix "F:\AI\01_项目\") and spins forever in the parser with no child
rem    process. Symptom: the build hangs with climbing CPU and never gets past
rem    the first line. This cost two 400s timeouts before it was diagnosed.
rem
rem !! No unescaped parentheses in `echo` lines inside if-blocks.
rem    `echo foo (bar)` inside `if ... ( ... )` makes cmd's block parser lose
rem    its matching paren and abort the script with exit 255. Symptom: the
rem    script stops right before the offending echo and never runs the rest
rem    (`build.cmd test` silently ran ZERO tests this way).
rem
rem Keep comments ASCII for readability; em dashes are harmless, but the two
rem rules above are not. Also see .gitattributes for eol enforcement.
rem
rem Hard constraints (PLAN sec 6 + sec 0.7):
rem   - Go 1.20.x (must be 1.20, 1.21+ APIs not allowed)
rem   - CGO_ENABLED=0 (pure static, no extra DLL; PE image may lack them)
rem   - -H windowsgui (GUI mode, no black console)
rem   - -trimpath (strip local paths, reproducible output)
rem   - -ldflags "-s -w" (strip symbols + debug info, ~30% smaller)
rem   - No Go 1.21+ APIs (go -C / tls.VersionName / os/user /
rem     GetTickCount64 / GetVersionExA / RegGetValueA)
rem
rem Order matters (docs/11 S6-1/S6-2):
rem   vet -> gofmt -> build -> PE verify -> test
rem   `test` MUST come after `build`: smoke_bin_test.go asserts dist/smith.exe
rem   is not older than the sources. Testing first would always fail once.
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
rem NOTE: win/ uses uintptr<->unsafe.Pointer casts, which is required by the
rem       v1-M1 contract (Win32 lparam/wparam come back as uintptr but must be
rem       treated as Go pointers; win.KeepAlive() keeps them alive).
rem       vet's unsafeptr check flags this, so win/ uses -unsafeptr=false.
rem src/test/ MUST be vetted explicitly — `go test` only runs a weaker
rem built-in vet subset, so "6 packages vetted" was really 5.
echo [INFO] go vet ...
set "CGO_ENABLED=0"
set "GOOS=windows"
set "GOARCH=386"
go vet -unsafeptr=false ./src/win/...
if errorlevel 1 ( echo [FATAL] vet 386 win failed & exit /b 1 )
go vet ./src/agent/... ./src/cfg/... ./src/logx/... ./src/tools/... ./src/test/... ./src
if errorlevel 1 ( echo [FATAL] vet 386 non-win failed & exit /b 1 )

set "GOARCH=amd64"
go vet -unsafeptr=false ./src/win/...
if errorlevel 1 ( echo [FATAL] vet amd64 win failed & exit /b 1 )
go vet ./src/agent/... ./src/cfg/... ./src/logx/... ./src/tools/... ./src/test/... ./src
if errorlevel 1 ( echo [FATAL] vet amd64 non-win failed & exit /b 1 )
echo [OK] go vet clean

rem gofmt gate (docs/11 S6-6).
rem Check only, never auto-fix: `gofmt -w` would bury the real change in a
rem large unrelated diff.
set "GOFMT_HITS="
for /f "tokens=*" %%f in ('gofmt -l src\ 2^>nul') do (
    echo   [FAIL] gofmt: %%f
    set "GOFMT_HITS=1"
)
if defined GOFMT_HITS (
    echo [FATAL] gofmt not clean; run: gofmt -w src\
    exit /b 1
)
echo [OK] gofmt clean

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

rem ------------------------------------------------------------------
rem Artifact verification (PLAN Phase 5; added in docs/11 S6-1).
rem   1. PE Subsystem must be 2 (WINDOWS_GUI). Subsystem 3 (CONSOLE) means
rem      double-clicking smith.exe in a PE image pops a black console box,
rem      which is the least reliable thing in a minimal image.
rem   2. Import table must not contain msvcrt / api-ms-win-crt-* / ucrt* /
rem      vcruntime. A pure-Go static build needs none of them.
rem   3. Warn if size exceeds the cap (usually means missing -s -w).
rem
rem Why this exists: dist/ once held a binary built by something else with a
rem bare `go build` (CONSOLE subsystem, 47% larger, 3 commits stale) and the
rem only test touching it passed because it merely checked "file exists".
rem ------------------------------------------------------------------
set "PS_EXE="
where pwsh.exe >nul 2>&1 && set "PS_EXE=pwsh.exe"
if not defined PS_EXE where powershell.exe >nul 2>&1 && set "PS_EXE=powershell.exe"

if defined PS_EXE (
    echo [INFO] verifying PE headers via %PS_EXE% ...
    %PS_EXE% -NoProfile -ExecutionPolicy Bypass -File verify-pe.ps1 dist\smith.exe dist\smith64.exe
    if errorlevel 1 ( echo [FATAL] PE artifact verification failed & exit /b 1 )
) else (
    echo [WARN] pwsh.exe / powershell.exe not found; SKIPPING artifact verification
    echo [WARN] verify manually that dist\smith.exe has PE Subsystem == 2
)

rem ------------------------------------------------------------------
rem go test runs AFTER build (docs/11 S6-2).
rem smoke_bin_test.go asserts dist/smith.exe is not older than the sources.
rem Running tests first would fail on any source change; wrong order turns a
rem gate into permanent noise.
rem
rem Running `go test ./src/...` without building first also fails (correctly):
rem it means dist/ really is stale and needs a rebuild.
rem ------------------------------------------------------------------
if /I "%1"=="test" (
    echo [INFO] go test -- against freshly built artifacts ...
    set "GOARCH=386"
    go test -count=1 ./src/...
    if errorlevel 1 ( echo [FATAL] test 386 failed & exit /b 1 )
    set "GOARCH=amd64"
    go test -count=1 ./src/...
    if errorlevel 1 ( echo [FATAL] test amd64 failed & exit /b 1 )
)

echo [OK] build complete: dist\smith.exe + dist\smith64.exe
endlocal
