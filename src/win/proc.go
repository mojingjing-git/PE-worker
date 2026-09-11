// Package win: proc.go 实现进程枚举、查询、终止、杀树。
//
// v1-M2 双层 PID 复用防护：
//   1) 整轮中止：KillTreeSelfContained 入口 + 每轮开头用 expectName 校验
//      root 的映像名（v2 修法，spike/job/main.go:309-330 复刻）
//   2) 子节点 InheritedFromUniqueProcessId 校验：每个非 root pid OpenProcess 后
//      调 NtQueryInformationProcess 拿 InheritedFromUniqueProcessId，
//      与 snapshot 记录的 PPID 不一致 skip + log（v1 第四轮修法，spike/job/main.go:375-385 复刻）
//
// 两层并存不互相替代：
//   - 整轮中止只防 root 本身被复用
//   - 子节点校验防子节点被回收给同一父链下的无关新进程
// 少任何一层就有"静默误杀无辜进程"风险（PLAN §0.9 §6 / MEMORY §13）。

package win

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"syscall"
	"unsafe"
)

// 常量（按 docs/02 §7 危险分级 + spike 实测）
const (
	th32csSnapProcess = 0x00000002
	errorNoMoreFiles  = 18 // ERROR_NO_MORE_FILES
	errnoNone         = 0

	// 进程访问权限
	processTerminate       = 0x0001
	processQueryLimited    = 0x1000 // PROCESS_QUERY_LIMITED_INFORMATION
	processQueryInformation = 0x0400

	// CreateProcess 标志
	createSuspended         = 0x00000004
	createBreakawayFromJob  = 0x01000000
	createNoWindow          = 0x08000000
)

// ProcessInfo 是 SnapshotProcesses 返回的最小信息（PPID + Name）。
type ProcessInfo struct {
	Name string
	PPID uint32
}

// 错误集
var (
	ErrSnapshotCreate  = errors.New("win: CreateToolhelp32Snapshot failed")
	ErrProcess32First  = errors.New("win: Process32FirstW failed")
	ErrProcess32Next   = errors.New("win: Process32NextW failed")
	ErrOpenProcess     = errors.New("win: OpenProcess failed")
	ErrTerminateProc   = errors.New("win: TerminateProcess failed")
	ErrQueryInherited  = errors.New("win: NtQueryInformationProcess failed")
)

// processEntry32 是 Process32FirstW/NextW 的输出。
// 字段顺序与 Win32 头文件一致：所有 uint32/int32 自然对齐，
// ULONG_PTR（th32DefaultHeapID）按平台字长变。ExeFile 是 MAX_PATH=260 个 uint16。
type processEntry32 struct {
	Size            uint32
	Usage           uint32
	ProcessID       uint32
	DefaultHeapID   uintptr
	ModuleID        uint32
	Threads         uint32
	ParentProcessID uint32
	PriClassBase    int32
	Flags           uint32
	ExeFile         [260]uint16
}

// processBasicInformation 是 NtQueryInformationProcess 第 0 类返回。
// 全部字段大小 = 平台指针大小（386=4, amd64=8），不含混合大小，可直接用 Go struct。
// 386: 24 bytes, amd64: 48 bytes.
type processBasicInformation struct {
	ExitStatus                   int32
	PebBaseAddress               uintptr
	AffinityMask                 uintptr
	BasePriority                 int32
	UniqueProcessId              uintptr
	InheritedFromUniqueProcessId uintptr
}

// Process32NextW 返回 0 时可能是"枚举完了"也可能是真出错。
// 用 errno 区分：ERROR_NO_MORE_FILES = 枚举正常结束，其他 = 真出错。
func snapshotProcs() (map[uint32]ProcessInfo, error) {
	out := map[uint32]ProcessInfo{}
	h, _, _ := pCreateToolhelp32Snapshot.Call(th32csSnapProcess, 0)
	if h == 0 {
		return out, fmt.Errorf("%w", ErrSnapshotCreate)
	}
	defer pCloseHandle.Call(h)

	var pe processEntry32
	pe.Size = uint32(unsafe.Sizeof(pe))
	r, _, errno := pProcess32FirstW.Call(h, uintptr(unsafe.Pointer(&pe)))
	if r == 0 {
		if errno != nil && errno != syscall.Errno(errnoNone) && errno != syscall.Errno(errorNoMoreFiles) {
			return out, fmt.Errorf("%w: %v", ErrProcess32First, errno)
		}
		return out, nil
	}
	for {
		out[pe.ProcessID] = ProcessInfo{
			Name: syscall.UTF16ToString(pe.ExeFile[:]),
			PPID: pe.ParentProcessID,
		}
		r, _, errno = pProcess32NextW.Call(h, uintptr(unsafe.Pointer(&pe)))
		if r == 0 {
			if errno != nil && errno != syscall.Errno(errnoNone) && errno != syscall.Errno(errorNoMoreFiles) {
				return out, fmt.Errorf("%w after %d entries: %v", ErrProcess32Next, len(out), errno)
			}
			break
		}
	}
	return out, nil
}

// QueryInheritedFromPid 通过 ntdll!NtQueryInformationProcess 拿 hProcess 的父 PID。
// 用于 v1-M2 子节点 PID 复用防护：snapshot 记录的 PPID 来自父链映射，但 PID 是
// 可复用的 —— 如果子节点被回收给同一父链下的新进程，InheritedFromUniqueProcessId
// 与 snapshot 不一致，**绝不杀**。
func QueryInheritedFromPid(hProcess uintptr) (uint32, error) {
	var pbi processBasicInformation
	ret, _, _ := pNtQueryInformationProcess.Call(
		hProcess,
		0, // ProcessBasicInformation
		uintptr(unsafe.Pointer(&pbi)),
		uintptr(unsafe.Sizeof(pbi)),
		0, // ReturnLength
	)
	if ret != 0 {
		return 0, fmt.Errorf("%w: 0x%x", ErrQueryInherited, ret)
	}
	return uint32(pbi.InheritedFromUniqueProcessId), nil
}

// OpenProcess 包装 kernel32!OpenProcess。要 processTerminate + processQueryLimited
// 才能 TerminateProcess + QueryInheritedFromPid。
func OpenProcess(pid uint32) (uintptr, error) {
	h, _, e := pOpenProcess.Call(processTerminate|processQueryLimited, 0, uintptr(pid))
	if h == 0 {
		return 0, fmt.Errorf("%w pid=%d: %v", ErrOpenProcess, pid, e)
	}
	return h, nil
}

// TerminateProcess 包装 kernel32!TerminateProcess（exitCode 传给进程）。
func TerminateProcess(hProcess uintptr, exitCode uint32) error {
	r, _, e := pTerminateProcess.Call(hProcess, uintptr(exitCode))
	if r == 0 {
		return fmt.Errorf("%w: %v", ErrTerminateProc, e)
	}
	return nil
}

// CloseHandle 包装 kernel32!CloseHandle，**忽略** ERROR_INVALID_HANDLE
// （重复 CloseHandle 在 spike 里常见，不算错）。
func CloseHandle(h uintptr) {
	_, _, _ = pCloseHandle.Call(h)
}

// treeOf 从 all snapshot 出发按 parent 关系递归收集整棵进程树（含 root 自己），按 BFS 层序返回。
// PID 升序排序保证可重复性。
func treeOf(all map[uint32]ProcessInfo, root uint32) []uint32 {
	children := map[uint32][]uint32{}
	for pid, info := range all {
		children[info.PPID] = append(children[info.PPID], pid)
	}
	for k := range children {
		sort.Slice(children[k], func(i, j int) bool { return children[k][i] < children[k][j] })
	}
	var order []uint32
	seen := map[uint32]bool{root: true}
	queue := []uint32{root}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		order = append(order, cur)
		for _, k := range children[cur] {
			if !seen[k] {
				seen[k] = true
				queue = append(queue, k)
			}
		}
	}
	return order
}

// KillTreeSelfContained 自实现进程树终止（v1-M2 双层防护）。
//
// 入口校验 + 每轮开头都校验 root 映像名（v2 修法）：如果 PID 已被回收给别的进程，
// 名字对不上 → **整轮 abort + 不 retry**，宁可留着残留子进程也不再误杀。
// 每轮里非 root 节点 OpenProcess 后用 InheritedFromUniqueProcessId 校验（v1 第四轮修法）。
//
// 返回 killed（成功 TerminateProcess 的进程数）+ errs（每个失败的 err 信息）。
func KillTreeSelfContained(root uint32, expectName string) (int, []string) {
	var errs []string
	killRounds := 3

	// 入口校验：root 必须在 snapshot 里且名字匹配
	first, err := snapshotProcs()
	if err != nil {
		return 0, []string{fmt.Sprintf("snapshot failed: %v", err)}
	}
	rootInfo, ok := first[root]
	if !ok {
		return 0, []string{fmt.Sprintf("pid %d no longer present", root)}
	}
	if expectName != "" && !strings.EqualFold(rootInfo.Name, expectName) {
		return 0, []string{fmt.Sprintf("pid %d is now %q (expected %q) -> PID reused, abort",
			root, rootInfo.Name, expectName)}
	}

	killed := 0
	for round := 1; round <= killRounds; round++ {
		all, err := snapshotProcs()
		if err != nil {
			errs = append(errs, fmt.Sprintf("round %d snapshot failed: %v", round, err))
			break
		}

		// 每轮开头再校验一次 root（v2 修法）
		rootInfo, ok := all[root]
		if !ok {
			errs = append(errs, fmt.Sprintf("round %d: root pid %d no longer present, abort", round, root))
			break
		}
		if expectName != "" && !strings.EqualFold(rootInfo.Name, expectName) {
			errs = append(errs, fmt.Sprintf("round %d: root pid %d is now %q (expected %q) -> PID reused, abort",
				round, root, rootInfo.Name, expectName))
			break
		}

		order := treeOf(all, root)
		roundKilled := 0
		// 反序遍历：BFS 层序末尾是最深的叶子，先杀叶子再杀根
		for i := len(order) - 1; i >= 0; i-- {
			pid := order[i]
			info := all[pid]

			// root 名字再校验（之前提过但 round 内仍可被换名 —— 极罕见但防御）
			if pid == root && expectName != "" && !strings.EqualFold(info.Name, expectName) {
				errs = append(errs, fmt.Sprintf("round %d: root %d changed name to %q mid-round, abort",
					round, pid, info.Name))
				return killed, errs
			}

			h, e := OpenProcess(pid)
			if e != nil {
				errs = append(errs, fmt.Sprintf("open %d(%s) failed: %v", pid, info.Name, e))
				continue
			}

			// v1-M2 子节点 InheritedFromUniqueProcessId 校验
			if pid != root {
				actualPPID, qerr := QueryInheritedFromPid(h)
				if qerr != nil {
					errs = append(errs, fmt.Sprintf(
						"query PPID for %d(%s) failed; refusing to terminate (safety): %v",
						pid, info.Name, qerr))
					CloseHandle(h)
					continue
				}
				if actualPPID != info.PPID {
					errs = append(errs, fmt.Sprintf(
						"pid %d(%s) is now child of %d (snapshot said %d) -> PID reused, skipping",
						pid, info.Name, actualPPID, info.PPID))
					CloseHandle(h)
					continue
				}
			}

			if err := TerminateProcess(h, 1); err != nil {
				errs = append(errs, fmt.Sprintf("terminate %d(%s) failed: %v", pid, info.Name, err))
				CloseHandle(h)
				continue
			}
			CloseHandle(h)
			killed++
			roundKilled++
		}

		if roundKilled == 0 {
			break
		}
	}
	return killed, errs
}
