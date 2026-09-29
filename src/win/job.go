// Package win: job.go 实现 Job Object 包装。
//
// v2 复审 + spike 实测结果：JOBOBJECT_EXTENDED_LIMIT_INFORMATION 在 386 上
// **必须**用字节缓冲 + 显式偏移（PLAN §0.9 §1 / MEMORY §1 / spike/job/main.go:194-221）。
//
// 历史教训（spike 实测抓出）：
//   Go 在 386 上把 int64/uint64 对齐到 4 字节（Go 默认是"小对齐"），
//   MSVC 默认对齐到 8 字节。直接翻译 C struct 定义会得到 108 字节（MSVC 要 112），
//   SetInformationJobObject 返回 ERROR_BAD_LENGTH 但**不抛错**（cbSize 校验返回 0），
//   结果 KILL_ON_JOB_CLOSE 静默失效 —— 只在 32 位出现，极难发现。
//   PE 里会变成僵死的 diskpart / dism 还锁着磁盘。
//
// 用字节缓冲 + 显式偏移完全绕开这个问题。

package win

import (
	"encoding/binary"
	"errors"
	"fmt"
	"unsafe"
)

// 来自 spike 实测 + Win32 头文件
const (
	jobExtLimitInfoSizeX86       = 112 // MSVC x86（8 字节对齐 LARGE_INTEGER）
	jobExtLimitInfoSizeX64       = 144 // MSVC x64
	jobLimitFlagsOffset          = 16  // 两种架构下 LimitFlags 都在偏移 16（两个 LARGE_INTEGER 之后）
	jobObjectLimitKillOnJobClose = 0x00002000
)

var (
	ErrCreateJobObject          = errors.New("win: CreateJobObjectW failed")
	ErrSetInformationJobObject  = errors.New("win: SetInformationJobObject failed")
	ErrAssignProcessToJobObject = errors.New("win: AssignProcessToJobObject failed")
	ErrTerminateJobObject       = errors.New("win: TerminateJobObject failed")
	// ErrQueryProcessJob 已删（P3-27）：IsProcessInJob 恒返 false，从不返错。
)

// buildJobExtLimitInfo 手工构造字节缓冲，按架构选 112 (x86) / 144 (x64) 字节。
// LimitFlags 在偏移 16 字节（两个 LARGE_INTEGER 之后）。
// 来自 spike 实测：386 下写 108 字节 SetInformationJobObject 静默失败，写 112 字节 OK。
func buildJobExtLimitInfo(killOnJobClose bool) []byte {
	size := jobExtLimitInfoSizeX64
	if unsafe.Sizeof(uintptr(0)) == 4 {
		size = jobExtLimitInfoSizeX86
	}
	buf := make([]byte, size)
	if killOnJobClose {
		binary.LittleEndian.PutUint32(buf[jobLimitFlagsOffset:], jobObjectLimitKillOnJobClose)
	}
	return buf
}

// CreateJobObject 调 CreateJobObjectW 创建一个 Job Object。
// 返回的 handle 必须用 CloseHandle 关闭（调用方责任）。
func CreateJobObject() (uintptr, error) {
	h, _, e := pCreateJobObjectW.Call(0, 0)
	if h == 0 {
		return 0, fmt.Errorf("%w: %v", ErrCreateJobObject, e)
	}
	return h, nil
}

// SetKillOnJobClose 给 hJob 设置 JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE ——
// 关键标志：job 内所有进程在最后一个 handle 关闭时**自动被 kill**。
// 不设这个，job 内进程会变成孤儿。
// 内部用字节缓冲（v2-M2 硬规则），不是 Go struct。
func SetKillOnJobClose(hJob uintptr) error {
	buf := buildJobExtLimitInfo(true)
	r, _, e := pSetInformationJobObject.Call(
		hJob,
		9, // JobObjectExtendedLimitInformation
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)),
	)
	KeepAlive(&buf[0]) // 防止 buf 在 .Call 期间被 GC
	if r == 0 {
		return fmt.Errorf("%w: %v (cbSize=%d, **386 必须用 112 字节，不要用 Go struct**)",
			ErrSetInformationJobObject, e, len(buf))
	}
	return nil
}

// AssignProcessToJobObject 把 hProcess 加入 hJob。
// hProcess 通常由 CreateProcess 的 PROCESS_INFORMATION 给出。
func AssignProcessToJobObject(hJob, hProcess uintptr) error {
	r, _, e := pAssignProcessToJobObject.Call(hJob, hProcess)
	if r == 0 {
		return fmt.Errorf("%w: %v", ErrAssignProcessToJobObject, e)
	}
	return nil
}

// TerminateJobObject 终止 hJob 内所有进程（exitCode 传给每个进程）。
// 用于"中止机制"——杀整棵进程树。
func TerminateJobObject(hJob uintptr, exitCode uint32) error {
	r, _, e := pTerminateJobObject.Call(hJob, uintptr(exitCode))
	if r == 0 {
		return fmt.Errorf("%w: %v", ErrTerminateJobObject, e)
	}
	return nil
}

// IsProcessInJob 检查 hProcess 是否已在某个 job 里。返回 (inJob, error)。
//
// **为什么不探测：本函数是诚实降级，恒返 (false, nil)。**
//
// 底层 kernel32!IsProcessInJob 在本机实测会**猝死进程**（Exception 0xc0000005，
// Go 层没有 error / panic / 日志）。探针实测（go1.20.14 / CGO_ENABLED=0 / 非管理员 /
// Win11 build 26200 / 386 与 amd64 双架构，每个场景独立进程）：
//
//	hProcess=OpenProcess(PROCESS_QUERY_INFORMATION)  hJob=NULL        → 崩 0xc0000005
//	hProcess=OpenProcess(PROCESS_QUERY_LIMITED_INFO) hJob=NULL        → 崩 0xc0000005
//	hProcess=GetCurrentProcess() (pseudo -1)         hJob=NULL        → 崩 0xc0000005
//	hProcess=OpenProcess(PROCESS_QUERY_INFORMATION)  hJob=有效 job    → 崩 0xc0000005
//	hProcess=OpenProcess(PROCESS_QUERY_INFORMATION)  hJob=0x1234 假句柄 → 正常返回 "The handle is invalid."
//	hProcess=OpenProcess(PROCESS_QUERY_INFORMATION)  hJob=Event 句柄  → 正常返回 "The handle is invalid."
//
// 崩溃发生在 kernel32 内部（PC=0x7ffd6c7465b9，读地址 0x0），栈顶是
// runtime.cgocall → syscall.SyscallN → Proc.Call。
//
// **修正前一版的错误结论**：旧注释写"真凶是 NULL-Job 快路径"，那是错的 ——
// 传 NULL 会崩，但传**有效 job handle 同样崩**。真正的规律是：只要 hJob 通过了
// IsProcessInJob 的句柄校验（NULL 或真 job 句柄都算通过），就会崩；hJob 是个它能
// 立刻判定为无效的句柄时反而干净返回。也就是崩点在"真正去比对进程/job 归属"的
// 代码路径上，而不是"能不能接受 pseudo-handle"—— 换成 OpenProcess 拿的真句柄
// （含 PROCESS_QUERY_INFORMATION 全权限）一样崩。
//
// 试过但**不能用**的两条替代路径（均为实测结论）：
//  1. ntdll!NtQueryInformationProcess(ProcessJobObjectInformation=3)：确定性失败
//     （前一轮审计 0/20），class=3 不在官方 SDK 的 PROCESSINFOCLASS 枚举里。
//  2. ntdll!NtIsProcessInJob：**不崩**（上述全部崩溃场景都安全返回），但实测
//     它不能区分归属 —— AssignProcessToJobObject 把当前进程成功塞进 job 之后
//     （ret=1），NtIsProcessInJob(hReal, NULL) 仍返回 STATUS_OBJECT_NOT_EXISTS
//     (0x124) 且 inJob=false，拿它当归属判据会给出错误结论。
//
// 所以这里**不探测**。一个诚实返回 false 的函数，远好于一个会崩进程的函数。
// 调用方若需要"是否已在 job 里"，请走 Plan 的降级路径（docs/11 S0-4 三层兜底：
// IsProcessInJob 检测 → CREATE_BREAKAWAY_FROM_JOB → taskkill /T /F），
// 并对本函数恒 false 做防御，不要把 true 当成"已验证在 job 里"。
func IsProcessInJob(hProcess uintptr) (bool, error) {
	_ = hProcess // 故意不探测，见上方实测记录
	return false, nil
}
