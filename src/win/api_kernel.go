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
	pGlobalMemoryStatusEx = kernel32.NewProc("GlobalMemoryStatusEx")
	pGetTickCount         = kernel32.NewProc("GetTickCount")
	// 注意：pGetTickCount64 故意不声明 —— AGENTS.md §2 禁用清单 / docs/02 §8 明确禁用
	// （Win7 PE 上 GetTickCount64 缺失，会运行时炸）。要 64-bit tick 用 GetTickCount
	// 配合 uint32 wraparound 处理。
	pGetNativeSystemInfo = kernel32.NewProc("GetNativeSystemInfo")
	pGetSystemInfo       = kernel32.NewProc("GetSystemInfo")
	pGetLogicalDrives    = kernel32.NewProc("GetLogicalDrives")
	pGetDriveTypeW       = kernel32.NewProc("GetDriveTypeW")
	// 盘容量 / 卷标（tools/sysinfo.go 的 diskinfo 用）。两个都是 kernel32
	// **核心导出**（kernel32.dll 与 advapi32.dll 分离之后仍留在 kernel32 的一批），
	// WinPE 3.x 必含 —— 不是 shell32/ws2_32 那类可选 DLL 的转发。
	// 参照 spike/hello 只验过 GetLogicalDrives+GetDriveTypeW，这两个是首次接线。
	pGetDiskFreeSpaceExW      = kernel32.NewProc("GetDiskFreeSpaceExW")
	pGetVolumeInformationW    = kernel32.NewProc("GetVolumeInformationW")
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
	// 双阶段 OEM(GBK)→UTF-8 转码（H-1：修 cmd.exe 输出中文乱码）。
	// CP_OEMCP(=1) 自动跟随控制台当前 OEM 代码页（中文 PE=936/GBK；
	// 若 chcp 65001 则自动按 UTF-8 解，无需特判）。
	pMultiByteToWideChar = kernel32.NewProc("MultiByteToWideChar")
	pWideCharToMultiByte = kernel32.NewProc("WideCharToMultiByte")
	// Job Object API（job.go 用）
	pCreateJobObjectW         = kernel32.NewProc("CreateJobObjectW")
	pSetInformationJobObject  = kernel32.NewProc("SetInformationJobObject")
	pAssignProcessToJobObject = kernel32.NewProc("AssignProcessToJobObject")
	pTerminateJobObject       = kernel32.NewProc("TerminateJobObject")
	pIsProcessInJob           = kernel32.NewProc("IsProcessInJob")
)

// ntdll procs
var (
	pRtlGetVersion             = ntdll.NewProc("RtlGetVersion")
	pNtQueryInformationProcess = ntdll.NewProc("NtQueryInformationProcess")
)

// advapi32 procs
var (
	pGetUserNameW = advapi32.NewProc("GetUserNameW")
)

// T2 新增：Job 杀树链路用到的 proc。
// 全部是 kernel32 核心导出，WinPE 3.x 必含（docs/12 §三 T2-1）。
// 可注入点见 jobexec.go 的 procXxx 变量。
var (
	pGetSystemDirectoryW   = kernel32.NewProc("GetSystemDirectoryW")
	pCreatePipe            = kernel32.NewProc("CreatePipe")
	pSetHandleInformation  = kernel32.NewProc("SetHandleInformation")
	pResumeThread          = kernel32.NewProc("ResumeThread")
	pGetProcessHandleCount = kernel32.NewProc("GetProcessHandleCount")
)
