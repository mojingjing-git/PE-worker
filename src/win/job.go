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
	jobExtLimitInfoSizeX86        = 112 // MSVC x86（8 字节对齐 LARGE_INTEGER）
	jobExtLimitInfoSizeX64        = 144 // MSVC x64
	jobLimitFlagsOffset           = 16  // 两种架构下 LimitFlags 都在偏移 16（两个 LARGE_INTEGER 之后）
	jobObjectLimitKillOnJobClose  = 0x00002000
)

var (
	ErrCreateJobObject         = errors.New("win: CreateJobObjectW failed")
	ErrSetInformationJobObject = errors.New("win: SetInformationJobObject failed")
	ErrAssignProcessToJobObject = errors.New("win: AssignProcessToJobObject failed")
	ErrTerminateJobObject       = errors.New("win: TerminateJobObject failed")
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

// IsProcessInJob 调 IsProcessInJob 检查 hProcess 是否已在某个 job 里。
// 返回 (inJob, error)。
// **Win7 没有嵌套 job**，如果当前进程已在父 job 里且父 job 不允许 breakaway，
// 那 AssignProcessToJobObject 会失败 —— 这正是 P0-5 spike 实测踩到的坑。
func IsProcessInJob(hProcess uintptr) (bool, error) {
	// 第二个参数 hJob = 0 表示"任何 job"
	r, _, e := pIsProcessInJob.Call(hProcess, 0)
	if r == 0 {
		// IsProcessInJob 失败（极罕见）
		return false, fmt.Errorf("IsProcessInJob: %v", e)
	}
	// r=1: in job; r=0: not in job
	return r == 1, nil
}
