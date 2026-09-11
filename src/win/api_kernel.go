// Package win: api_kernel.go 集中声明 kernel32 / ntdll / advapi32 的 LazyProc。
//
// 这些 proc 是 sysinfo.go (P1-3) 和 proc.go / job.go (P1-5) 用到的。
// **不**包含 user32 / gdi32（那在 P1-4 api_user_gdi.go）。
//
// 不预声明不用的 —— 按 spike 实战 + docs/02 列的需求。

package win

import "syscall"

var (
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	ntdll    = syscall.NewLazyDLL("ntdll.dll")
	advapi32 = syscall.NewLazyDLL("advapi32.dll")
)

// kernel32 procs
var (
	pGlobalMemoryStatusEx     = kernel32.NewProc("GlobalMemoryStatusEx")
	pGetTickCount             = kernel32.NewProc("GetTickCount")
	// 注意：pGetTickCount64 故意不声明 —— MEMORY §10 / docs/02 §8 明确禁用
	// （Win7 PE 上 GetTickCount64 缺失，会运行时炸）。要 64-bit tick 用 GetTickCount
	// 配合 uint32 wraparound 处理。
	pGetNativeSystemInfo      = kernel32.NewProc("GetNativeSystemInfo")
	pGetSystemInfo            = kernel32.NewProc("GetSystemInfo")
	pGetLogicalDrives         = kernel32.NewProc("GetLogicalDrives")
	pGetDriveTypeW            = kernel32.NewProc("GetDriveTypeW")
	pGetComputerNameW         = kernel32.NewProc("GetComputerNameW")
	pGetModuleHandleW         = kernel32.NewProc("GetModuleHandleW")
	pCloseHandle              = kernel32.NewProc("CloseHandle")
	pCreateProcessW           = kernel32.NewProc("CreateProcessW")
	pGetCurrentProcessId      = kernel32.NewProc("GetCurrentProcessId")
	pGetCurrentProcess        = kernel32.NewProc("GetCurrentProcess")
	pGetExitCodeProcess       = kernel32.NewProc("GetExitCodeProcess")
	pWaitForSingleObject      = kernel32.NewProc("WaitForSingleObject")
	pTerminateProcess         = kernel32.NewProc("TerminateProcess")
	pOpenProcess              = kernel32.NewProc("OpenProcess")
	pCreateToolhelp32Snapshot = kernel32.NewProc("CreateToolhelp32Snapshot")
	pProcess32FirstW          = kernel32.NewProc("Process32FirstW")
	pProcess32NextW           = kernel32.NewProc("Process32NextW")
	pSleep                    = kernel32.NewProc("Sleep")
	pGetExitCodeThread        = kernel32.NewProc("GetExitCodeThread")
	// Job Object API（job.go 用）
	pCreateJobObjectW         = kernel32.NewProc("CreateJobObjectW")
	pSetInformationJobObject  = kernel32.NewProc("SetInformationJobObject")
	pAssignProcessToJobObject = kernel32.NewProc("AssignProcessToJobObject")
	pTerminateJobObject       = kernel32.NewProc("TerminateJobObject")
	pIsProcessInJob           = kernel32.NewProc("IsProcessInJob")
)

// ntdll procs
var (
	pRtlGetVersion            = ntdll.NewProc("RtlGetVersion")
	pNtQueryInformationProcess = ntdll.NewProc("NtQueryInformationProcess")
)

// advapi32 procs
var (
	pGetUserNameW = advapi32.NewProc("GetUserNameW")
)
