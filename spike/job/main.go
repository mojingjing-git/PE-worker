//go:build windows

// spike/job -- P0-5
//
// 目标：验证「中止机制」这条路在目标环境里真的走得通。
//
// 为什么这条最危险（一轮审核 A3 + 二轮审核 B2/B3）：
//
//	Windows 7 **不支持嵌套 job**（嵌套是 Win8 才有的）。如果本进程已经被
//	某个父 job 包含（winpeshl.exe 或 cmd 拉起时都可能），
//	AssignProcessToJobObject 会直接失败 —— 而"超时/停止按钮杀整棵进程树"
//	正是靠它。失败是**静默**的，不处理的话中止机制看起来正常、实际失效。
//
//	二轮审核还指出：最后兜底用的 taskkill.exe 在**最小 WinPE 3.x 里可能不存在**。
//	所以本 spike 同时实现一个**完全自包含的进程树终止**（不依赖任何外部 exe）：
//	CreateToolhelp32Snapshot 按 th32ParentProcessID 递归收树，再逐个 TerminateProcess。
//
// 注意：二轮审核建议过 PROC_THREAD_ATTRIBUTE_JOB_LIST，但已查证微软文档 ——
// 那是 "Windows 10 and newer"，**Win7 用不了**。所以走
// CreateProcess(CREATE_SUSPENDED) -> AssignProcessToJobObject -> ResumeThread，
// 先挂起再入 job 就没有竞态。
//
// 三轮代码审计（2026-09-11）修掉的问题，都在下面各自的注释里标了 [audit]：
//   - 杀树用单次静态快照 -> 漏杀快照之后派生的子孙；改多轮快照到稳定
//   - 没有 PID 复用防护 -> root 被回收后可能误杀无关进程；加映像名比对
//   - TerminateJobObject 失败无降级 -> 中止静默失效；补降级
//   - pi2 句柄泄漏、ResumeThread 返回值未检查、IsProcessInJob 失败未识别
//   - Process32First/Next 返回 0 时不区分"枚举结束"和"真出错" -> 快照被静默截断
//
// 输出全 ASCII。
package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"syscall"
	"unsafe"
)

var (
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	ntdll    = syscall.NewLazyDLL("ntdll.dll")

	pCreateJobObjectW         = kernel32.NewProc("CreateJobObjectW")
	pSetInformationJobObject  = kernel32.NewProc("SetInformationJobObject")
	pAssignProcessToJobObject = kernel32.NewProc("AssignProcessToJobObject")
	pTerminateJobObject       = kernel32.NewProc("TerminateJobObject")
	pIsProcessInJob           = kernel32.NewProc("IsProcessInJob")
	pGetCurrentProcess        = kernel32.NewProc("GetCurrentProcess")
	pCreateProcessW           = kernel32.NewProc("CreateProcessW")
	pResumeThread             = kernel32.NewProc("ResumeThread")
	pTerminateProcess         = kernel32.NewProc("TerminateProcess")
	pWaitForSingleObject      = kernel32.NewProc("WaitForSingleObject")
	pOpenProcess              = kernel32.NewProc("OpenProcess")
	pCloseHandle              = kernel32.NewProc("CloseHandle")
	pCreateToolhelp32Snapshot = kernel32.NewProc("CreateToolhelp32Snapshot")
	pProcess32FirstW          = kernel32.NewProc("Process32FirstW")
	pProcess32NextW           = kernel32.NewProc("Process32NextW")
	pSleep                    = kernel32.NewProc("Sleep")
	// 第四轮独立审计 M2：用 NtQueryInformationProcess 拿 hProcess 的 InheritedFromUniqueProcessId，
	// 给 killTreeSelfContained 第二道 PID 复用防护。PE 上 ntdll 必有。
	pNtQueryInformationProcess = ntdll.NewProc("NtQueryInformationProcess")
)

const (
	createSuspended        = 0x00000004
	createBreakawayFromJob = 0x01000000
	createNoWindow         = 0x08000000
	jobObjectLimitKillOnJobClose    = 0x00002000
	jobObjectExtendedLimitInfoClass = 9
	th32csSnapProcess               = 0x00000002
	processTerminate                = 0x0001
	processQueryLimited             = 0x1000
	infinite                        = 0xFFFFFFFF

	// ERROR_NO_MORE_FILES —— Process32FirstW/NextW 枚举到末尾时的正常返回值。
	// [audit] 不区分它和真错误的话，中途出错会被当成"枚举结束"，
	// 快照被静默截断，杀树就漏掉一批进程。
	errorNoMoreFiles = 18

	// 杀树的轮数上限。单次快照是静态的，快照之后才派生的子孙不在列表里，
	// 所以要「快照 -> 从叶子杀到根 -> 再快照」循环到没有进展为止。
	killRounds = 3

	// PROCESSINFOCLASS 枚举值（ntdll!NtQueryInformationProcess 第二参数）。
	// 第四轮独立审计 M2 用 ProcessBasicInformation (0) 拿 InheritedFromUniqueProcessId。
	processBasicInformationClass = 0
)

type jobBasicLimitInformation struct {
	PerProcessUserTimeLimit int64
	PerJobUserTimeLimit     int64
	LimitFlags              uint32
	MinimumWorkingSetSize   uintptr
	MaximumWorkingSetSize   uintptr
	ActiveProcessLimit      uint32
	Affinity                uintptr
	PriorityClass           uint32
	SchedulingClass         uint32
}

type ioCounters struct {
	ReadOperationCount  uint64
	WriteOperationCount uint64
	OtherOperationCount uint64
	ReadTransferCount   uint64
	WriteTransferCount  uint64
	OtherTransferCount  uint64
}

type jobObjectExtendedLimitInformation struct {
	BasicLimitInformation jobBasicLimitInformation
	IoInfo                ioCounters
	ProcessMemoryLimit    uintptr
	JobMemoryLimit        uintptr
	PeakProcessMemoryUsed uintptr
	PeakJobMemoryUsed     uintptr
}

type processInformation struct {
	Process   uintptr
	Thread    uintptr
	ProcessId uint32
	ThreadId  uint32
}

// PROCESS_BASIC_INFORMATION（ntdll!NtQueryInformationProcess 第 0 类返回）。
//
// 字段布局：
//
//	ExitStatus                    NTSTATUS (LONG)
//	PebBaseAddress                PPEB
//	AffinityMask                  ULONG_PTR
//	BasePriority                  KPRIORITY (LONG)
//	UniqueProcessId               ULONG_PTR
//	InheritedFromUniqueProcessId  ULONG_PTR  ← 我们只关心这个
//
// 按硬规则 2：所有成员大小 = 平台指针大小（386=4 / amd64=8），
// 不含混合 32/64 位，可直接用 Go struct，不需要手工字节缓冲。
// 386 上 sizeof = 24，amd64 上 sizeof = 48（runtime 已实测，job_386 -diag 会打印）。
type processBasicInformation struct {
	ExitStatus                   int32
	PebBaseAddress               uintptr
	AffinityMask                 uintptr
	BasePriority                 int32
	UniqueProcessId              uintptr
	InheritedFromUniqueProcessId uintptr
}

type startupInfoW struct {
	Cb            uint32
	Reserved      uintptr
	Desktop       uintptr
	Title         uintptr
	X             uint32
	Y             uint32
	XSize         uint32
	YSize         uint32
	XCountChars   uint32
	YCountChars   uint32
	FillAttribute uint32
	Flags         uint32
	ShowWindow    uint16
	CbReserved2   uint16
	Reserved2     uintptr
	StdInput      uintptr
	StdOutput     uintptr
	StdErr        uintptr
}

type processEntry32 struct {
	Size            uint32
	CntUsage        uint32
	ProcessID       uint32
	DefaultHeapID   uintptr
	ModuleID        uint32
	CntThreads      uint32
	ParentProcessID uint32
	PriClassBase    int32
	Flags           uint32
	ExeFile         [260]uint16
}

type procInfo struct {
	Name string
	PPID uint32
}

// ---------------------------------------------------------------------------
// JOBOBJECT_EXTENDED_LIMIT_INFORMATION 的构造
//
// ⚠️ 这里刻意**不用 Go 结构体**，见下面 diagLayout 的说明：
// 386 上 Go 把 int64/uint64 对齐到 4，而 MSVC 默认 8，
// 直接翻译 C 定义会得到 108 字节（MSVC 要 112），
// SetInformationJobObject 返回 ERROR_BAD_LENGTH，**而且不抛错**，
// 结果 KILL_ON_JOB_CLOSE 静默失效 —— 只在 32 位出现，极难发现。
//
// 用字节缓冲 + 显式偏移就完全绕开这个问题。
// ---------------------------------------------------------------------------

const (
	jobExtLimitInfoSizeX86 = 112 // MSVC x86
	jobExtLimitInfoSizeX64 = 144 // MSVC x64
	jobLimitFlagsOffset    = 16  // 两种架构下 LimitFlags 都在偏移 16（两个 LARGE_INTEGER 之后）
)

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

// ---------------------------------------------------------------- 进程快照

// [audit] 返回 error：Process32FirstW/NextW 返回 0 既可能是"枚举结束"，
// 也可能是真出错。用 Call 带回来的 errno（Go 在 syscall 返回后立刻取 GetLastError）
// 区分，避免快照被静默截断。
func snapshotProcs() (map[uint32]procInfo, error) {
	out := map[uint32]procInfo{}
	h, _, _ := pCreateToolhelp32Snapshot.Call(th32csSnapProcess, 0)
	if h == 0 || h == uintptr(^uintptr(0)) {
		return out, fmt.Errorf("CreateToolhelp32Snapshot failed")
	}
	defer pCloseHandle.Call(h)

	var pe processEntry32
	pe.Size = uint32(unsafe.Sizeof(pe))
	r, _, errno := pProcess32FirstW.Call(h, uintptr(unsafe.Pointer(&pe)))
	if r == 0 {
		if errno != nil && errno != syscall.Errno(0) && errno != syscall.Errno(errorNoMoreFiles) {
			return out, fmt.Errorf("Process32FirstW failed: %v", errno)
		}
		return out, nil
	}
	for {
		out[pe.ProcessID] = procInfo{
			Name: syscall.UTF16ToString(pe.ExeFile[:]),
			PPID: pe.ParentProcessID,
		}
		r, _, errno = pProcess32NextW.Call(h, uintptr(unsafe.Pointer(&pe)))
		if r == 0 {
			if errno != nil && errno != syscall.Errno(0) && errno != syscall.Errno(errorNoMoreFiles) {
				return out, fmt.Errorf("Process32NextW failed after %d process(es): %v", len(out), errno)
			}
			break
		}
	}
	return out, nil
}

// mustSnapshot 是给 main 用的宽松包装：快照不完整时打印警告但仍然返回已有数据。
func mustSnapshot() map[uint32]procInfo {
	m, err := snapshotProcs()
	if err != nil {
		fmt.Printf("    WARN: process snapshot incomplete: %v\n", err)
	}
	return m
}

// 从根 pid 出发按 parent 关系递归收集整棵进程树（含根自己），按 BFS 层序返回
func treeOf(all map[uint32]procInfo, root uint32) []uint32 {
	children := map[uint32][]uint32{}
	for pid, info := range all {
		children[info.PPID] = append(children[info.PPID], pid)
	}
	var order []uint32
	seen := map[uint32]bool{root: true}
	queue := []uint32{root}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		order = append(order, cur)
		kids := children[cur]
		sort.Slice(kids, func(i, j int) bool { return kids[i] < kids[j] })
		for _, k := range kids {
			if !seen[k] {
				seen[k] = true
				queue = append(queue, k)
			}
		}
	}
	return order
}

// ---------------------------------------------------- 自包含的进程树终止

// 完全不依赖 taskkill.exe / 外部命令。先杀叶子再杀根，避免有进程不断重生子进程。
//
// expectName 是调用方在起进程时观察到的 root 映像名，用来防 PID 复用：
// 如果 root 在我们拿到 pid 之后就被系统回收给了别的进程，名字会对不上，
// 这时候**绝不能杀** —— 否则就是杀掉一个完全无关的用户进程。
func killTreeSelfContained(root uint32, expectName string) (killed int, errs []string) {
	// 入口先做一次归属校验，名字对不上直接拒绝。
	first, err := snapshotProcs()
	if err != nil {
		return 0, []string{fmt.Sprintf("snapshot failed: %v", err)}
	}
	rootInfo, ok := first[root]
	if !ok {
		return 0, []string{fmt.Sprintf("pid %d no longer present", root)}
	}
	if expectName != "" && !strings.EqualFold(rootInfo.Name, expectName) {
		return 0, []string{fmt.Sprintf(
			"pid %d is now %q but expected %q -> refusing to kill (PID was reused)",
			root, rootInfo.Name, expectName)}
	}

	for round := 1; round <= killRounds; round++ {
		all, err := snapshotProcs()
		if err != nil {
			errs = append(errs, fmt.Sprintf("round %d snapshot: %v", round, err))
			break
		}
		order := treeOf(all, root)

		// treeOf 总把 root 自己放进 order，但 root 可能已经被上一轮杀掉了，
		// 所以要数的是"快照里真实存在的成员"。
		alive := 0
		for _, pid := range order {
			if _, ok := all[pid]; ok {
				alive++
			}
		}
		if alive == 0 {
			break
		}

		roundKilled := 0
		rootSkippedThisRound := false
		for i := len(order) - 1; i >= 0; i-- { // 反序 = 从叶子往根杀
			pid := order[i]
			info, present := all[pid]
			if !present {
				continue
			}
			// root 自己的 PID 复用校验：发现名字对不上时**整轮中止**，不再杀子树。
			//
			// 原版只 continue skip root，然后继续杀子树 —— 但这些"子树"所在的 PID
			// 也是从 root.父链 派生出来的，root 一旦被复用，这些 PID 同样可能
			// 已经被回收给了别的进程（甚至可能是**完全不同**的进程树），
			// 继续杀 = 直接干掉无辜的 GUI 或别的用户进程。是个静默的崩别人进程的事故。
			if pid == root {
				if expectName != "" && !strings.EqualFold(info.Name, expectName) {
					errs = append(errs, fmt.Sprintf(
						"round %d: pid %d is now %q (expected %q) -> ABORT entire tree kill (PID reuse)",
						round, pid, info.Name, expectName))
					rootSkippedThisRound = true
					break
				}
				// root 名字仍然匹配 —— 继续正常杀
			}
			h, _, e := pOpenProcess.Call(processTerminate|processQueryLimited, 0, uintptr(pid))
			if h == 0 {
				// 错误**已经**记到 errs（下面会逐行打印），不会静默丢失。
				// 这里 continue 让本轮"无进展" → 外层 roundKilled==0 → break，
				// 是合理的"尽早退出 + 不再徒劳"策略。
				errs = append(errs, fmt.Sprintf("open %d(%s) failed: %v", pid, info.Name, e))
				continue
			}
			// 第四轮独立审计 M2：非 root 节点也要防 PID 复用。root 的名字校验只
			// 覆盖 root 自己；子节点可能在 snapshot 之后被回收，PID 被分给一个
			// 恰好挂在我们父链下的新进程，treeOf 仍把它列入树。验证"实际父进程"
			// 与 snapshot 一致再杀，否则 skip + log，绝不杀无辜。
			if pid != root {
				actualPPID, ok := queryInheritedFromPid(h)
				if !ok {
					errs = append(errs, fmt.Sprintf(
						"query PPID for %d(%s) failed; refusing to terminate (safety)", pid, info.Name))
					pCloseHandle.Call(h)
					continue
				}
				if actualPPID != info.PPID {
					errs = append(errs, fmt.Sprintf(
						"pid %d(%s) is now child of %d (snapshot said %d) -> PID reused, skipping",
						pid, info.Name, actualPPID, info.PPID))
					pCloseHandle.Call(h)
					continue
				}
			}
			r, _, e2 := pTerminateProcess.Call(h, 1)
			pCloseHandle.Call(h)
			if r == 0 {
				errs = append(errs, fmt.Sprintf("terminate %d(%s) failed: %v", pid, info.Name, e2))
			} else {
				killed++
				roundKilled++
			}
		}
		// root 被 PID 复用了 —— 绝不能再循环，宁可留着残留子进程也不要再误杀。
		if rootSkippedThisRound {
			errs = append(errs, "ABORT: root PID appears reused; not retrying further rounds")
			break
		}
		if roundKilled == 0 {
			break // 这一轮没有任何进展，再循环也没意义
		}
		// 给内核一点时间回收 pid，让下一轮的快照能看到真实的残留。
		sleep(300)
	}
	return killed, errs
}

// 诊断用：打印某个 pid 下面挂着谁（用任务管理器式的缩进树）
func dumpTree(all map[uint32]procInfo, root uint32, label string) []string {
	order := treeOf(all, root)
	fmt.Printf("    [%s] process tree of pid %d: %d process(es)\n", label, root, len(order))
	var names []string
	for _, pid := range order {
		info := all[pid]
		if pid != root && all[pid].Name != "" {
			names = append(names, info.Name)
		}
		if _, ok := all[pid]; ok {
			fmt.Printf("        pid=%-6d ppid=%-6d %s\n", pid, info.PPID, info.Name)
		}
	}
	return names
}

func sleep(ms int) { pSleep.Call(uintptr(ms)) }

// queryInheritedFromPid 通过 ntdll!NtQueryInformationProcess 拿 hProcess 的父 PID。
// 返回 ok=false 表示 API 调用失败（极罕见，如 handle 无效 / 跨 session）。
//
// 第四轮独立审计 M2 修法：snapshot 记录的 PPID 来自父链映射，但 PID 是可复用的 ——
// 子节点被回收后，PID 可能被分给一个恰好挂在我们父链上的新进程，treeOf 仍把它当
// 树成员。所以对每个非 root 节点，OpenProcess 后再查一次 InheritedFromUniqueProcessId，
// 与 snapshot 记录的 PPID 不一致就 skip + log，绝不杀。
func queryInheritedFromPid(hProcess uintptr) (ppid uint32, ok bool) {
	var pbi processBasicInformation
	// NtQueryInformationProcess 返回 NTSTATUS：0 = STATUS_SUCCESS。缓冲按完整 struct 传。
	ret, _, _ := pNtQueryInformationProcess.Call(
		hProcess,
		processBasicInformationClass,
		uintptr(unsafe.Pointer(&pbi)),
		uintptr(unsafe.Sizeof(pbi)),
		0, // ReturnLength 不需要
	)
	if ret != 0 {
		return 0, false
	}
	return uint32(pbi.InheritedFromUniqueProcessId), true
}

// diagLayout 打印结构体布局，用来抓 386 / amd64 的对齐差异。
//
// 背景（本 spike 实测抓到的真 bug）：
//
//	MSVC 在 x86 上默认按 8 字节对齐 LARGE_INTEGER/ULONGLONG（/Zp8 默认值），
//	而 Go 在 386 上对这些 64 位类型用的是更小的对齐 —— 于是同一个结构体
//	Go 算出 108 字节、MSVC 是 112 字节。SetInformationJobObject 收到错的
//	cbSize 会返回 ERROR_BAD_LENGTH，**而且不报错给用户**：job 建起来了，
//	但 KILL_ON_JOB_CLOSE 没设上，进程一退子进程就泄漏。
//	只在 32 位出现、只在这类混合结构体上出现 —— 极难发现。
func diagLayout() {
	fmt.Println("---- struct layout diagnostics ----")
	fmt.Printf("pointer size            = %d\n", unsafe.Sizeof(uintptr(0)))
	fmt.Printf("Alignof(int64)          = %d\n", unsafe.Alignof(int64(0)))
	fmt.Printf("Alignof(uint64)         = %d\n", unsafe.Alignof(uint64(0)))
	fmt.Printf("Alignof(uintptr)        = %d\n", unsafe.Alignof(uintptr(0)))
	fmt.Println()
	fmt.Printf("BasicLimitInformation   Go=%d\n", unsafe.Sizeof(jobBasicLimitInformation{}))
	fmt.Printf("  offset LimitFlags     Go=%d\n", unsafe.Offsetof(jobBasicLimitInformation{}.LimitFlags))
	fmt.Printf("  offset Affinity       Go=%d\n", unsafe.Offsetof(jobBasicLimitInformation{}.Affinity))
	fmt.Printf("IoCounters              Go=%d\n", unsafe.Sizeof(ioCounters{}))
	fmt.Println()
	fmt.Printf("ExtLimitInformation     Go=%d\n", unsafe.Sizeof(jobObjectExtendedLimitInformation{}))
	fmt.Printf("  expected MSVC         = 112 (x86) / 144 (x64)\n")
	fmt.Println()
	fmt.Printf("STARTUPINFOW            Go=%d   (MSVC 68 x86 / 104 x64)\n", unsafe.Sizeof(startupInfoW{}))
	fmt.Printf("PROCESS_INFORMATION     Go=%d   (MSVC 16 x86 / 24 x64)\n", unsafe.Sizeof(processInformation{}))
	fmt.Printf("PROCESSENTRY32W         Go=%d   (MSVC 556 x86 / 568 x64)\n", unsafe.Sizeof(processEntry32{}))
	fmt.Printf("PROCESS_BASIC_INFO      Go=%d   (expected 24 x86 / 48 x64)\n", unsafe.Sizeof(processBasicInformation{}))
	fmt.Println()
	fmt.Println("  ^ 只有 ExtLimitInformation 会不一致：它以两个 LARGE_INTEGER 开头，")
	fmt.Println("    386 上 Go 按 4 对齐、MSVC 按 8 对齐，于是 108 vs 112。")
	fmt.Println("    其余几个结构体不含 64 位成员（或自然 8 对齐），两边一致。")
	fmt.Println("    PROCESS_BASIC_INFO 的所有成员大小都等于平台指针大小，")
	fmt.Println("    所以 Go struct 直接用、不需要字节缓冲（硬规则 2 适用）。")
}

func main() {
	hold := flag.Int("hold", 30, "seconds the child process tree should stay alive")
	diag := flag.Bool("diag", false, "print struct layout diagnostics and exit")
	flag.Parse()

	if *diag {
		diagLayout()
		os.Exit(0)
	}

	fmt.Println("========================================================")
	fmt.Println(" pe-spike-job  (P0-5 job object + self-contained tree kill)")
	fmt.Println("========================================================")

	me, _, _ := pGetCurrentProcess.Call()

	// ---------- [1] 本进程是否已在某个 job 里 ----------
	fmt.Println("\n[1] IsProcessInJob(GetCurrentProcess())")
	var inJob int32
	r, _, e := pIsProcessInJob.Call(me, 0, uintptr(unsafe.Pointer(&inJob)))
	fmt.Printf("    ret=%d inJob=%d err=%v\n", r, inJob, e)
	switch {
	case r == 0:
		// [audit] 查询失败时 inJob 保持 0。直接当"不在 job 里"是错的 ——
		// 那会掩盖 Win7 上 AssignProcessToJobObject 注定失败的真正原因。
		fmt.Println("    WARN: IsProcessInJob failed -> inJob is meaningless, treat as UNKNOWN")
	case inJob != 0:
		fmt.Println("    -> this process IS inside a job. AssignProcessToJobObject may fail")
		fmt.Println("       (Windows 7 has no nested jobs). Breakaway flag becomes essential.")
	default:
		fmt.Println("    -> not in a job. AssignProcessToJobObject should succeed.")
	}

	// ---------- [2] 建 job ----------
	fmt.Println("\n[2] CreateJobObject + SetInformationJobObject(KILL_ON_JOB_CLOSE)")
	hJob, _, e := pCreateJobObjectW.Call(0, 0)
	fmt.Printf("    CreateJobObject -> hJob=%d err=%v\n", hJob, e)
	if hJob == 0 {
		fmt.Println("    FATAL: cannot create a job object here")
		// [audit] os.Exit 不执行 defer，必须显式关句柄 —— 否则产品化后
		// hJob 句柄泄漏，下一次跑这条路径时 GetLastError 还能看到老句柄。
		os.Exit(3)
	}
	// 在产品里这里会改成 defer + 显式 cleanup。当前 spike 里只用一次，记在心里就行：
	// 每个走到这里的分支都必须先把 hJob 关掉再退出（或赋值给全局、由专门的 cleanup 关）。
	closeJobAtExit := func() {
		fmt.Printf("    cleanup: CloseHandle(hJob=%d)\n", hJob)
		pCloseHandle.Call(hJob)
	}

	li := buildJobExtLimitInfo(true)
	r, _, e = pSetInformationJobObject.Call(hJob, jobObjectExtendedLimitInfoClass,
		uintptr(unsafe.Pointer(&li[0])), uintptr(len(li)))
	fmt.Printf("    SetInformationJobObject ret=%d err=%v (cbSize=%d)\n", r, e, len(li))
	if r == 0 {
		fmt.Println("    WARN: could not set KILL_ON_JOB_CLOSE -> children will leak on exit")
	} else {
		fmt.Println("    OK    KILL_ON_JOB_CLOSE armed")
	}

	// ---------- [3] 起一个会派生子进程的命令 ----------
	// cmd.exe -> ping.exe，两层，正好用来验证"杀整棵树"
	cmdline := fmt.Sprintf("cmd.exe /c ping -n %d 127.0.0.1", *hold)
	fmt.Printf("\n[3] CreateProcess(CREATE_SUSPENDED|CREATE_BREAKAWAY_FROM_JOB): %s\n", cmdline)

	cl, err := syscall.UTF16FromString(cmdline) // 必须是可写内存：CreateProcessW 会改它
	if err != nil {
		fmt.Printf("    FATAL: %v\n", err)
		// [audit] 必须先关 hJob 再退 —— os.Exit 跳过 defer。
		closeJobAtExit()
		os.Exit(2)
	}

	var si startupInfoW
	si.Cb = uint32(unsafe.Sizeof(si))
	var pi processInformation

	createdWith := ""
	r, _, e = pCreateProcessW.Call(0, uintptr(unsafe.Pointer(&cl[0])), 0, 0, 0,
		createSuspended|createBreakawayFromJob|createNoWindow,
		0, 0, uintptr(unsafe.Pointer(&si)), uintptr(unsafe.Pointer(&pi)))
	if r != 0 {
		createdWith = "SUSPENDED|BREAKAWAY_FROM_JOB"
	} else {
		fmt.Printf("    with BREAKAWAY failed (%v), retrying without it\n", e)
		r, _, e = pCreateProcessW.Call(0, uintptr(unsafe.Pointer(&cl[0])), 0, 0, 0,
			createSuspended|createNoWindow,
			0, 0, uintptr(unsafe.Pointer(&si)), uintptr(unsafe.Pointer(&pi)))
		if r != 0 {
			createdWith = "SUSPENDED"
		}
	}
	if r == 0 {
		fmt.Printf("    FATAL: CreateProcessW failed: %v\n", e)
		// [audit] 必须先关 hJob + 任何已拿到的 pi 句柄。
		closeJobAtExit()
		pCloseHandle.Call(pi.Process)
		pCloseHandle.Call(pi.Thread)
		os.Exit(4)
	}
	rootPid := pi.ProcessId
	fmt.Printf("    created  pid=%d tid=%d  flags=%s\n", rootPid, pi.ThreadId, createdWith)

	// ---------- [4] 入 job（关键一步）----------
	fmt.Println("\n[4] AssignProcessToJobObject  <-- THE critical call on Win7")
	r, _, e = pAssignProcessToJobObject.Call(hJob, pi.Process)
	assigned := r != 0
	if assigned {
		fmt.Printf("    OK    assign succeeded\n")
	} else {
		fmt.Printf("    FAIL  %v\n", e)
		fmt.Println("          ^ 这就是 Win7 嵌套 job 限制的表现。必须降级到自实现的杀树。")
	}

	// ---------- [5] 放它跑 ----------
	// [audit] ResumeThread 返回 (DWORD)-1 表示失败。不检查的话，恢复失败会让子进程
	// 永远挂在挂起态，而后面"grandchild present: false"看起来像"程序没派生子进程"，
	// 把一个失败误读成一次正常观察。
	rt, _, eRt := pResumeThread.Call(pi.Thread)
	if rt == 0xFFFFFFFF {
		fmt.Printf("\n[5] WARN: ResumeThread failed (%v) -> child stays suspended\n", eRt)
	} else {
		fmt.Printf("\n[5] ResumeThread -> child is running (previous suspend count=%d)\n", rt)
	}
	sleep(2500)

	all := mustSnapshot()
	names := dumpTree(all, rootPid, "alive")
	foundPing := false
	for _, n := range names {
		if strings.EqualFold(n, "ping.exe") {
			foundPing = true
		}
	}
	fmt.Printf("    grandchild ping.exe present: %v\n", foundPing)

	// root 的映像名 —— 后面杀树用它做 PID 复用防护，必须来自真实观察而不是硬编码。
	rootName := all[rootPid].Name

	// ---------- [6] 用 job 杀（失败则降级）----------
	fmt.Println("\n[6] TerminateJobObject")
	via := "job object"
	if !assigned {
		fmt.Println("    skipped (job was never assigned)")
		via = "self-contained tree kill"
	} else {
		r, _, e = pTerminateJobObject.Call(hJob, 1)
		fmt.Printf("    TerminateJobObject ret=%d err=%v\n", r, e)
		if r == 0 {
			// [audit] 入 job 成功 ≠ 终止一定成功。失败时若不降级，
			// 中止机制就是静默失效的 —— 而且只在 Win7 这类老系统上发生。
			fmt.Println("    -> terminate FAILED, falling back to self-contained tree kill")
			via = "self-contained tree kill (job terminate failed)"
		}
	}
	if via != "job object" {
		killed, errs := killTreeSelfContained(rootPid, rootName)
		fmt.Printf("    killed %d process(es), %d error(s)\n", killed, len(errs))
		for _, s := range errs {
			fmt.Printf("      err: %s\n", s)
		}
	}
	sleep(1200)
	after := mustSnapshot()
	left := 0
	for _, pid := range treeOf(all, rootPid) {
		if _, ok := after[pid]; ok {
			left++
		}
	}
	fmt.Printf("    processes from the tree still alive: %d  -> %s\n",
		left, map[bool]string{true: "OK", false: "INCOMPLETE"}[left == 0])

	// ---------- [7] 单独验证自包含杀树（不管上面成没成，都测一遍）----------
	fmt.Println("\n[7] self-contained tree kill on a fresh tree (independent of job path)")
	cmdline2 := fmt.Sprintf("cmd.exe /c ping -n %d 127.0.0.1", *hold)
	// 第四轮独立审计 L5：原来吞 err。当前 spike 里 cmdline 来自 *hold (int) 不会失败，
	// 但产品里命令可能来自外部输入，不可信。补 err 检查。
	cl2, err := syscall.UTF16FromString(cmdline2)
	if err != nil {
		fmt.Printf("    FATAL: UTF16FromString: %v\n", err)
		// pi2 还是零值，CloseHandle(0) 是 no-op
		os.Exit(2)
	}
	var si2 startupInfoW
	si2.Cb = uint32(unsafe.Sizeof(si2))
	var pi2 processInformation
	noJob := uintptr(createNoWindow) // 故意不带 breakaway，让它跑在默认环境里
	r, _, _ = pCreateProcessW.Call(0, uintptr(unsafe.Pointer(&cl2[0])), 0, 0, 0,
		noJob, 0, 0, uintptr(unsafe.Pointer(&si2)), uintptr(unsafe.Pointer(&pi2)))
	if r == 0 {
		fmt.Println("    could not spawn second tree; skipping")
	} else {
		sleep(2500)
		all2 := mustSnapshot()
		order2 := treeOf(all2, pi2.ProcessId)
		name2 := all2[pi2.ProcessId].Name
		fmt.Printf("    spawned pid=%d (%s), tree size=%d\n", pi2.ProcessId, name2, len(order2))
		killed, errs := killTreeSelfContained(pi2.ProcessId, name2)
		fmt.Printf("    killed %d, errors %d\n", killed, len(errs))
		for _, s := range errs {
			fmt.Printf("      err: %s\n", s)
		}
		sleep(1000)
		after2 := mustSnapshot()
		left := 0
		for _, pid := range order2 {
			if _, ok := after2[pid]; ok {
				left++
			}
		}
		fmt.Printf("    still alive: %d  -> %s\n", left,
			map[bool]string{true: "OK", false: "INCOMPLETE"}[left == 0])

		// [audit] 这两个句柄原来从不关闭，每跑一次就泄漏两个内核句柄。
		pCloseHandle.Call(pi2.Thread)
		pCloseHandle.Call(pi2.Process)
	}

	pCloseHandle.Call(pi.Thread)
	pCloseHandle.Call(pi.Process)

	// [audit] 原来靠 defer 关 hJob，但 os.Exit 不执行 defer，句柄永远不会关 ——
	// 对 spike 无害（进程退出后 OS 回收），但这段代码会被移植进产品，
	// 产品里 os.Exit 是真的会走的路径，必须显式关。
	// 关掉 hJob 会触发 KILL_ON_JOB_CLOSE，正好把任何残留清干净。
	pCloseHandle.Call(hJob)

	fmt.Println("\nRESULT: job spike done. Abort mechanism feasibility =",
		map[bool]string{true: "job object works", false: "must use self-contained tree kill"}[assigned])
	os.Exit(0)
}
