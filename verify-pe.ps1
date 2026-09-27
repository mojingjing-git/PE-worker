<#
.SYNOPSIS
    PE-agent artifact verification: subsystem + import table + size.

.DESCRIPTION
    PLAN Phase 5 requires "build-time artifact check: dependency table must not
    contain msvcrt / api-ms-win-crt-*, and the subsystem must be GUI" — but
    build.cmd never implemented it.

    The direct motivation (docs/11 S6-1): dist/ once held a binary produced by
    something else via a bare `go build` — PE subsystem = 3 (CONSOLE), symbols
    not stripped, 47% larger, and 3 commits stale. It sat there while the only
    test that touched it passed, because that test merely checked "file exists".
    A "GUI single exe" that pops a black console in a PE image got shipped.

    Had this gate existed, it would have surfaced in 30 seconds.

    NOTE: output strings are deliberately ASCII-only. The Chinese notes above
    are comments (never emitted); cmd.exe renders non-ASCII output as mojibake
    and that noise makes build logs hard to read.

.PARAMETER Path
    One or more .exe paths to verify.

.OUTPUTS
    exit 0 if all checks pass, exit 1 otherwise.
#>

[CmdletBinding()]
param(
    [Parameter(Mandatory = $true, ValueFromRemainingArguments = $true)]
    [string[]]$Path
)

$ErrorActionPreference = 'Stop'

# ---- Constants ----
$SUBSYSTEM_WINDOWS_GUI = 2

# CRT runtimes that must never appear in the import table.
# A pure-Go static build needs none of them; minimal WinPE 3.x images may not
# ship them, and a missing DLL is the most common cause of "flashes and dies".
$BannedDllPrefixes = @(
    'msvcrt', 'mscvcr', 'ucrtbase', 'vcruntime', 'msvcpwin', 'concrt',
    'api-ms-win-crt-', 'api-ms-win-core-'
)

# Size cap in bytes. Current correct artifacts: 386 ~5.44MB, amd64 ~5.58MB.
# Anything over 8MB usually means -ldflags "-s -w" was dropped.
$MaxSizeBytes = 8MB

function Read-PeInfo {
    param([string]$File)

    $bytes = [System.IO.File]::ReadAllBytes($File)
    if ($bytes.Length -lt 0x40) { throw "file too small to be a PE: $File" }
    if ($bytes[0] -ne 0x4D -or $bytes[1] -ne 0x5A) { throw "missing MZ header: $File" }

    $eLfanew = [BitConverter]::ToUInt32($bytes, 0x3C)
    if ($eLfanew + 24 -ge $bytes.Length) { throw "e_lfanew out of range: $File" }
    if ($bytes[$eLfanew] -ne 0x50 -or $bytes[$eLfanew + 1] -ne 0x45 -or
        $bytes[$eLfanew + 2] -ne 0x00 -or $bytes[$eLfanew + 3] -ne 0x00) {
        throw "missing PE\0\0 signature: $File"
    }

    $numSections   = [BitConverter]::ToUInt16($bytes, $eLfanew + 6)
    $optHeaderSize = [BitConverter]::ToUInt16($bytes, $eLfanew + 20)
    $optOff        = [int]$eLfanew + 24
    $magic         = [BitConverter]::ToUInt16($bytes, $optOff)

    switch ($magic) {
        0x10B { $dataDirOff = $optOff + 96;  $arch = 'PE32 (386)' }
        0x20B { $dataDirOff = $optOff + 112; $arch = 'PE32+ (amd64)' }
        default { throw ("unknown OptionalHeader magic 0x{0:X}: {1}" -f $magic, $File) }
    }

    # Subsystem sits at OptionalHeader offset 68 for both PE32 and PE32+.
    $subsystem = [BitConverter]::ToUInt16($bytes, $optOff + 68)

    # Section table, for RVA -> file offset mapping.
    $secOff = $optOff + $optHeaderSize
    $sections = @()
    for ($i = 0; $i -lt $numSections; $i++) {
        $o = $secOff + $i * 40
        if ($o + 40 -gt $bytes.Length) { break }
        $sections += [pscustomobject]@{
            VirtualSize    = [BitConverter]::ToUInt32($bytes, $o + 8)
            VirtualAddress = [BitConverter]::ToUInt32($bytes, $o + 12)
            RawSize        = [BitConverter]::ToUInt32($bytes, $o + 16)
            RawAddress     = [BitConverter]::ToUInt32($bytes, $o + 20)
        }
    }
    $rva2off = {
        param([uint32]$rva)
        foreach ($s in $sections) {
            $span = [Math]::Max($s.VirtualSize, $s.RawSize)
            if ($rva -ge $s.VirtualAddress -and $rva -lt ($s.VirtualAddress + $span)) {
                return [int]($s.RawAddress + ($rva - $s.VirtualAddress))
            }
        }
        return -1
    }

    # DataDirectory[1] = Import Table
    $importRva = [BitConverter]::ToUInt32($bytes, $dataDirOff + 8)
    $dlls = @()
    if ($importRva -ne 0) {
        $off = & $rva2off $importRva
        if ($off -ge 0) {
            for ($d = 0; $d -lt 64; $d++) {   # 64 = sanity cap, avoids runaway on a corrupt file
                $base = $off + $d * 20
                if ($base + 20 -gt $bytes.Length) { break }
                $nameRva = [BitConverter]::ToUInt32($bytes, $base + 12)
                if ($nameRva -eq 0) { break }
                $no = & $rva2off $nameRva
                if ($no -lt 0 -or $no -ge $bytes.Length) { break }
                $end = $no
                while ($end -lt $bytes.Length -and $bytes[$end] -ne 0) { $end++ }
                $dlls += [System.Text.Encoding]::ASCII.GetString($bytes, $no, $end - $no)
            }
        }
    }

    [pscustomobject]@{
        Path      = $File
        Arch      = $arch
        Subsystem = $subsystem
        Imports   = $dlls
        Size      = $bytes.Length
    }
}

$failures = 0

foreach ($p in $Path) {
    if (-not (Test-Path -LiteralPath $p)) {
        Write-Host "[FAIL] file not found: $p" -ForegroundColor Red
        $failures++
        continue
    }

    Write-Host "[INFO] verifying $p ..."
    try {
        $pe = Read-PeInfo -File $p
    } catch {
        Write-Host "[FAIL] PE parse failed: $($_.Exception.Message)" -ForegroundColor Red
        $failures++
        continue
    }

    $subsysName = switch ($pe.Subsystem) {
        2 { 'WINDOWS_GUI' }
        3 { 'CONSOLE' }
        default { "UNKNOWN($($pe.Subsystem))" }
    }
    Write-Host ("       arch={0}  subsystem={1} ({2})  size={3:N0}  imports=[{4}]" -f `
            $pe.Arch, $pe.Subsystem, $subsysName, $pe.Size, ($pe.Imports -join ', '))

    # Check 1: subsystem must be GUI.
    if ($pe.Subsystem -ne $SUBSYSTEM_WINDOWS_GUI) {
        Write-Host ("[FAIL] subsystem is {0}, must be WINDOWS_GUI(2)" -f $subsysName) -ForegroundColor Red
        Write-Host "       In a PE image this pops a black console box, and console behaviour is the least reliable part of a minimal image." -ForegroundColor Red
        Write-Host "       Check that build.cmd ldflags contain -H windowsgui." -ForegroundColor Red
        $failures++
    }

    # Check 2: no CRT in the import table.
    $banned = @($pe.Imports | Where-Object {
            $n = $_
            $BannedDllPrefixes | Where-Object { $n -like "$_*" }
        })
    if ($banned.Count -gt 0) {
        Write-Host ("[FAIL] import table contains CRT runtime: {0}" -f ($banned -join ', ')) -ForegroundColor Red
        Write-Host "       A pure-Go static build must not depend on these. Minimal WinPE 3.x images may lack them." -ForegroundColor Red
        $failures++
    }

    # Check 3: size cap (usually means -ldflags "-s -w" was dropped).
    if ($pe.Size -gt $MaxSizeBytes) {
        Write-Host ("[WARN] size {0:N0} exceeds cap {1:N0} - probably missing -ldflags -s -w" -f $pe.Size, $MaxSizeBytes) -ForegroundColor Yellow
    }
}

if ($failures -gt 0) {
    Write-Host ""
    Write-Host "[FATAL] artifact verification failed: $failures problem(s)" -ForegroundColor Red
    exit 1
}

Write-Host "[OK] artifact verification passed ($($Path.Count) file(s))"
exit 0
