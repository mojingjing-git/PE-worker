//go:build windows

// Package win: jobexec.go —— 把子进程绑进 Job Object，使其可被整棵终止。
//
// 【为什么不能用 os/exec】—— 已核实（Go 1.20.14 syscall/exec_windows.go）：
// `syscall.SysProcAttr` 共 9 个字段（HideWindow / CmdLine / CreationFlags /
// Token / ProcessAttributes / ThreadAttributes / NoInheritHandles /
// AdditionalInheritedHandles / ParentProcess），**无任何 job 能力**。
// 必须自己调 CreateProcess。
//
// 【为什么不用 PROC_THREAD_ATTRIBUTE_JOB_LIST】—— SDK `winbase.h` 用
// `#if (_WIN32_WINNT >= _WIN32_WINNT_WINTHRESHOLD)` 门禁，而
// `_WIN32_WINNT_WINTHRESHOLD = 0x0A00` = **Windows 10**。Win7 用不了。
//
// 【为什么用 syscall 包的结构体而不是自己手写】—— Go 的 syscall 已内置
// StartupInfo / ProcessInformation / SecurityAttributes，布局在所有 Go
// Windows 程序上验证过（探针实测 386/amd64：
// StartupInfo 68/104、ProcessInformation 16/24、SecurityAttributes 12/24）。
// 自己手写就要面对 §S1 那条铁律（含指针成员的手写 Win32 结构体有 386 对齐
// 风险），而 T2 恰好是最容易违反它的地方。**用 stdlib 让这条铁律不触发。**
//
// 【进程生命周期的正确顺序】—— 句柄在手才不存在"进程已退出"的竞态：
//
//	CreateProcess(CREATE_SUSPENDED)   ← 进程创建但不执行
//	CreateJobObject + SetKillOnJobClose(KILL_ON_JOB_CLOSE)
//	AssignProcessToJobObject          ← 绑进 job
//	ResumeThread                      ← 现在才让它跑
//
// 若先 Resume 再 Assign，中间窗口里进程已经可以 spawn 孙进程了。
package win

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"syscall"
	"unsafe"
)

// 本批新增的 sentinel error（L1：所有可能失败的点都要有可 errors.Is 的错误）。
var (
	ErrCreatePipe         = errors.New("win: CreatePipe failed")
	ErrResumeThread       = errors.New("win: ResumeThread failed")
	ErrGetExitCodeProcess = errors.New("win: GetExitCodeProcess failed")
	ErrTerminateProcess   = errors.New("win: TerminateProcess failed")
)

// KillLogFunc 是降级链日志的注入点。
//
// ⚠️ **win 包不能 import logx**（logx → win，会成环），所以这里用一个
// 包级函数变量，由 main 在 init 时注入 logx.Warn。nil 时降级为静默。
//
// 为什么要留这个 hook：PE 现场无法复现，"Esc 杀不掉 diskpart"这种报障
// 只能靠这几行日志判断是 job 没建起来、还是 kill 本身失败。
var KillLogFunc func(format string, args ...any)

func logKillFallback(format string, args ...any) {
	if KillLogFunc != nil {
		KillLogFunc(format, args...)
	}
}

// STARTUPINFO / CREATE_PROCESS 相关的 Win32 常量。
// 放在 win 包（而不是 tools）是因为它们是 Win32 值，且必须与 consts_test.go
// 的 C1 门禁同步 —— 见 consts_test.go 的 TestWin32StructSizes。
const (
	startfUseStdHandles = 0x00000100 // STARTF_USESTDHANDLES
	startfUseShowWindow = 0x00000001 // STARTF_USESHOWWINDOW
	// createSuspended / createBreakawayFromJob / createNoWindow 已由 proc.go 定义，
	// 不在这里重复（Go 不允许同名常量）。
	waitInfinite      = 0xFFFFFFFF
	handleFlagInherit = 0x00000001 // HANDLE_FLAG_INHERIT

	// 窗口风格：隐藏子进程自己的窗口（console 程序用）
	swHide = 0
)

// JobCmd 是一个已绑定到 Job Object 的子进程。
//
// 句柄共 **7 个**（3 + 4），关闭时机各不相同 —— 见 Close 与各字段注释。
type JobCmd struct {
	hJob  uintptr
	hProc uintptr
	// hThread 在 ResumeThread 成功后**立即**关闭，不留到 Close()
	// （留着没有任何用，白占一个句柄）。

	stdoutRd uintptr // 管道读端（父进程读）
	stderrRd uintptr
	stdoutWr uintptr // 管道写端（给子进程）；CreateProcess 成功后立即在父进程关闭
	stderrWr uintptr

	rootPID  uint32 // 用于 M2 降级杀树
	rootName string // 映像名，用于 M2(a) 整轮中止
	closed   bool
	mu       sync.Mutex
}

// ---------------------------------------------------------------------------
// 可失败点：抽成可替换的包级 var，供 T2 的注入式单测使用
// （docs/12 §七 Q3 硬前置条件：降级链必须在本机可测）
//
// ⚠️ 抽成 var 有一个绕过口子：生产路径若改回直接 pXxx.Call，单测照样绿。
// 所以对应的单测必须**从 exec 工具入口端到端跑**，而不是直接调这些函数。
// ---------------------------------------------------------------------------

var (
	procCreateJobObject          = win32CreateJobObject
	procSetKillOnJobClose        = win32SetKillOnJobClose
	procAssignProcessToJobObject = win32AssignProcessToJobObject
	procTerminateJobObject       = win32TerminateJobObject
	procCreatePipe               = win32CreatePipe
	procResumeThread             = win32ResumeThread
)

// Win32 原始调用的薄封装（var 指向它们，便于单测替换）。
// 单独一层是为了让「可注入点」和「真实实现」在代码上可区分。

func win32CreateJobObject() (uintptr, error) {
	return CreateJobObject()
}

func win32SetKillOnJobClose(hJob uintptr) error { return SetKillOnJobClose(hJob) }

func win32AssignProcessToJobObject(hJob, hProcess uintptr) error {
	return AssignProcessToJobObject(hJob, hProcess)
}

func win32TerminateJobObject(hJob uintptr, exitCode uint32) error {
	return TerminateJobObject(hJob, exitCode)
}

func win32CreatePipe() (rd, wr uintptr, err error) {
	var r, w syscall.Handle
	if err := createPipe(&r, &w); err != nil {
		return 0, 0, err
	}
	return uintptr(r), uintptr(w), nil
}

func win32ResumeThread(hThread uintptr) error {
	r, _, e := pResumeThread.Call(hThread)
	if r == 0xFFFFFFFF {
		return fmt.Errorf("%w: %v", ErrResumeThread, e)
	}
	return nil
}

// createPipe 造一对匿名管道。**两端默认可继承**（sa.InheritHandle=1）——
// 写端是子进程要用的 stdout/stderr，必须可继承。
func createPipe(rd, wr *syscall.Handle) error {
	sa := syscall.SecurityAttributes{
		Length:        uint32(unsafe.Sizeof(syscall.SecurityAttributes{})),
		InheritHandle: 1,
	}
	if r, _, e := pCreatePipe.Call(
		uintptr(unsafe.Pointer(rd)),
		uintptr(unsafe.Pointer(wr)),
		uintptr(unsafe.Pointer(&sa)),
		0,
	); r == 0 {
		return fmt.Errorf("%w: %v", ErrCreatePipe, e)
	}
	return nil
}

// StartJobSpec 描述要启动的子进程。
type StartJobSpec struct {
	// ExePath 是可执行文件的**完整路径或含 exe 后缀的名字**。
	// 留空则用 Args[0]。
	//
	// ⚠️ **不能只写 "cmd"** —— 那是 cmd.exe 的内部名，不是文件名，
	// CreateProcessW 会报 "The system cannot find the file specified"。
	// 调用方应传 "cmd.exe"（带后缀，由系统按 PATH 解析）。
	ExePath string
	// Args 是参数**数组**（不含 exe 本身）。用数组而不是拼字符串，
	// 是为了让 exec.CommandContext 的转义规则替你处理空格与引号。
	Args []string
	// Cwd 是工作目录，空则继承父进程。
	Cwd string
	// Hidden 传 CREATE_NO_WINDOW（控制台工具用，避免子进程再弹黑框）
	Hidden bool
	// Breakaway 先尝试 CREATE_BREAKAWAY_FROM_JOB，失败则重试不带该 flag。
	// Win7 没有嵌套 job，本进程若已在父 job 里且父 job 不允许 breakaway，
	// 带该 flag 的 CreateProcess 会直接失败。
	Breakaway bool
}

// StartJobCmd 启动一个绑进 Job Object 的子进程。
//
// ⚠️ 调用方必须在调用后**及时 CloseWriteEnds**（见方法），否则父进程自己
// 持有的写端不关，子进程 stdout 写满管道后阻塞、永不退出 → Wait 死锁。
func StartJobCmd(spec StartJobSpec) (*JobCmd, error) {
	if len(spec.Args) == 0 {
		return nil, errors.New("StartJobCmd: Args 不能为空")
	}

	// ---- 1. 两根管道（stdout / stderr）----
	var soRd, soWr, seRd, seWr syscall.Handle
	if err := createPipe(&soRd, &soWr); err != nil {
		return nil, fmt.Errorf("StartJobCmd: stdout pipe: %w", err)
	}
	if err := createPipe(&seRd, &seWr); err != nil {
		pCloseHandle.Call(uintptr(soRd))
		pCloseHandle.Call(uintptr(soWr))
		return nil, fmt.Errorf("StartJobCmd: stderr pipe: %w", err)
	}
	// 父进程自己那份**读端**设为不可继承，避免子进程也持有读端
	//（不关的话子进程退出后管道 EOF 永远不来）。
	// 写端**必须保持可继承** —— 那是子进程要用的。
	pSetHandleInformation.Call(uintptr(soRd), handleFlagInherit, 0)
	pSetHandleInformation.Call(uintptr(seRd), handleFlagInherit, 0)

	si := syscall.StartupInfo{
		Cb:         uint32(unsafe.Sizeof(syscall.StartupInfo{})),
		Flags:      startfUseStdHandles | startfUseShowWindow,
		ShowWindow: uint16(swHide),
		StdOutput:  soWr,
		StdErr:     seWr,
		// StdInput 留 0：子进程不读 stdin。
		// 注意不能给 NUL 句柄 —— 传无效句柄时子进程启动即失败。
	}
	var pi syscall.ProcessInformation

	// ---- 2. 组装命令行 ----
	// CreateProcess 的 lpCommandLine 是**单个可写缓冲区**（不是 const 字符串），
	// 且第一个 token 必须是 exe 路径。syscall 的 CreateProcess 会自己格式化，
	// 但它要求传入命令行本身，这里用 UTF16FromString 自己拼。
	cmdline, err := buildCommandLine(spec)
	if err != nil {
		pCloseHandle.Call(uintptr(soRd))
		pCloseHandle.Call(uintptr(soWr))
		pCloseHandle.Call(uintptr(seRd))
		pCloseHandle.Call(uintptr(seWr))
		return nil, err
	}

	flags := uint32(createSuspended)
	if spec.Breakaway {
		flags |= createBreakawayFromJob
	}
	if spec.Hidden {
		flags |= createNoWindow
	}

	// ---- 3. CreateProcess（SUSPENDED）----
	created := false
	defer func() {
		if !created {
			pCloseHandle.Call(uintptr(soRd))
			pCloseHandle.Call(uintptr(soWr))
			pCloseHandle.Call(uintptr(seRd))
			pCloseHandle.Call(uintptr(seWr))
		}
	}()

	if err := doCreateProcess(spec, cmdline, flags, &si, &pi); err != nil {
		if !spec.Breakaway {
			return nil, err
		}
		// L1 降级点：带 BREAKAWAY 失败 → 去掉该 flag 重试。
		// （Win7 无嵌套 job 时这是唯一能建起来的路径）
		if err2 := doCreateProcess(spec, cmdline, flags&^createBreakawayFromJob, &si, &pi); err2 != nil {
			return nil, fmt.Errorf("%w（去掉 BREAKAWAY 重试仍失败: %v）", err, err2)
		}
	}
	created = true

	// ---- 4. 父进程立刻关掉两个写端 ----
	// 不关的话子进程 stdout 写满管道会阻塞、永不退出 → Wait 死锁。
	pCloseHandle.Call(uintptr(soWr))
	pCloseHandle.Call(uintptr(seRd))
	seRd = 0
	soWr = 0

	jc := &JobCmd{
		hProc:    uintptr(pi.Process),
		stdoutRd: uintptr(soRd),
		stderrRd: uintptr(seRd),
		rootPID:  pi.ProcessId,
		rootName: exeBaseName(spec),
	}

	// ---- 5. 建 job + 绑进去 ----
	if err := jc.attachToJob(); err != nil {
		// 绑不上 job：先杀掉刚创建的（还 SUSPENDED，没跑过）再返回错误，
		// 不能留一个挂起的孤儿进程。
		pTerminateProcess.Call(jc.hProc, 1)
		pCloseHandle.Call(jc.hProc)
		pCloseHandle.Call(jc.stdoutRd)
		pCloseHandle.Call(jc.stderrRd)
		return nil, err
	}

	// ---- 6. Resume ----
	if err := procResumeThread(uintptr(pi.Thread)); err != nil {
		// Resume 失败：job 已建好，关 job 会因 KILL_ON_JOB_CLOSE 连带杀掉
		// 尚未执行的进程，正好是我们想要的。
		jc.Close()
		return nil, err
	}
	pCloseHandle.Call(uintptr(pi.Thread)) // 线程句柄用完即弃
	return jc, nil
}

// doCreateProcess 调 CreateProcessW（bInheritHandles=TRUE）。
//
// ⚠️ bInheritHandles 必须为 TRUE —— 管道写端设为可继承就是为了让子进程拿到
// stdout/stderr；不传 TRUE 则前面所有管道设置都白做。
// 代价是会继承父进程**所有**可继承句柄。Go 的解法是
// PROC_THREAD_ATTRIBUTE_HANDLE_LIST 白名单，但该特性 Win7 不可用，
// 所以本项目的约定是：给 win 包对外暴露的句柄都设 HANDLE_FLAG_INHERIT=0，
// 只留管道写端可继承。
func doCreateProcess(spec StartJobSpec, cmdline []uint16, flags uint32,
	si *syscall.StartupInfo, pi *syscall.ProcessInformation) error {
	// ⚠️ **CreateProcessW 传了 lpApplicationName 时不做 PATH 搜索** ——
	// 只按完整路径找，传 "cmd.exe" 会报 "cannot find the file"（实测）。
	// 所以先把名字解析成 System32 下的完整路径。
	lpAppName := spec.ExePath
	if lpAppName != "" {
		if abs, err := resolveSystemExe(lpAppName); err == nil {
			lpAppName = abs
		}
	}
	var lpAppNamePtr uintptr
	if lpAppName != "" {
		b, err := syscall.UTF16PtrFromString(lpAppName)
		if err != nil {
			return fmt.Errorf("StartJobCmd: exe path 含 NUL: %w", err)
		}
		lpAppNamePtr = uintptr(unsafe.Pointer(b))
	}
	var cwdPtr uintptr
	if spec.Cwd != "" {
		b, err := syscall.UTF16PtrFromString(spec.Cwd)
		if err != nil {
			return fmt.Errorf("StartJobCmd: cwd 含 NUL: %w", err)
		}
		cwdPtr = uintptr(unsafe.Pointer(b))
	}

	r, _, e := pCreateProcessW.Call(
		lpAppNamePtr,
		uintptr(unsafe.Pointer(&cmdline[0])), // 可写缓冲区，CreateProcess 会改它
		0,                                    // lpProcessAttributes
		0,                                    // lpThreadAttributes
		1,                                    // bInheritHandles = TRUE（管道写端要传给子进程）
		uintptr(flags),                       // dwCreationFlags
		0,                                    // lpEnvironment
		cwdPtr,                               // lpCurrentDirectory
		uintptr(unsafe.Pointer(si)),
		uintptr(unsafe.Pointer(pi)),
	)
	KeepAlive(&cmdline[0])
	if r == 0 {
		return fmt.Errorf("CreateProcessW: %v", e)
	}
	return nil
}

// attachToJob 建 job、设 KILL_ON_JOB_CLOSE、绑进程。
func (j *JobCmd) attachToJob() error {
	hJob, err := procCreateJobObject()
	if err != nil {
		return fmt.Errorf("StartJobCmd: CreateJobObject: %w", err)
	}
	if err := procSetKillOnJobClose(hJob); err != nil {
		pCloseHandle.Call(hJob)
		return fmt.Errorf("StartJobCmd: SetKillOnJobClose: %w", err)
	}
	if err := procAssignProcessToJobObject(hJob, j.hProc); err != nil {
		// L2 降级点：绑不上（Win7 无嵌套 job 且父 job 不允许 breakaway）。
		// 关键：**关 job 会因 KILL_ON_JOB_CLOSE 杀掉刚创建的进程**，所以
		// 先关 job（顺手杀掉），再由调用方决定是否降级。
		pCloseHandle.Call(hJob)
		return fmt.Errorf("StartJobCmd: AssignProcessToJobObject: %w", err)
	}
	j.hJob = hJob
	return nil
}

// resolveSystemExe 把 "cmd.exe" 解析成 "%SystemRoot%\\System32\\cmd.exe"。
//
// 为什么需要：CreateProcessW 在 lpApplicationName 非空时**不做 PATH 搜索**
// （只有 lpApplicationName 为空、靠 lpCommandLine 首个 token 解析时才搜）。
// 直接传 "cmd.exe" 实测报 ERROR_FILE_NOT_FOUND。
func resolveSystemExe(name string) (string, error) {
	if strings.ContainsAny(name, `\\/`) {
		return name, nil // 已经是路径
	}
	n, _, _ := pGetSystemDirectoryW.Call(0, 0)
	if n == 0 {
		return "", errors.New("GetSystemDirectoryW failed")
	}
	buf := make([]uint16, n+1)
	r, _, e := pGetSystemDirectoryW.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if r == 0 {
		return "", fmt.Errorf("GetSystemDirectoryW: %v", e)
	}
	dir := string(utf16ToRunes(buf[:r]))
	return dir + `\\` + name, nil
}

// buildCommandLine 拼 CreateProcess 要的 lpCommandLine（可写缓冲）。
func buildCommandLine(spec StartJobSpec) ([]uint16, error) {
	exe := spec.ExePath
	if exe == "" {
		exe = spec.Args[0]
	}
	parts := make([]string, 0, len(spec.Args))
	for _, a := range spec.Args {
		parts = append(parts, quoteForCmd(a))
	}
	line := exe
	for _, p := range parts[1:] {
		line += " " + p
	}
	// 末尾必须留 NUL 终止符
	b, err := syscall.UTF16FromString(line)
	if err != nil {
		return nil, fmt.Errorf("StartJobCmd: 命令行含 NUL 字符: %w", err)
	}
	return b, nil
}

// quoteForCmd 按 Windows 命令行规则给单个参数加引号。
// 与 exec_windows.go 的 makeCmdLine 同源语义：含空格/制表符/引号的要包起来。
func quoteForCmd(s string) string {
	if s == "" {
		return `""`
	}
	needQuote := false
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case ' ', '\t', '"':
			needQuote = true
		}
	}
	if !needQuote {
		return s
	}
	// 内部引号用 \" 转义（这是 Windows 命令行的规则，不是 cmd.exe 的）
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			// 反斜杠：其后若紧跟引号，需要翻倍
			n := 0
			for i+n+1 < len(s) && s[i+n+1] == '\\' {
				n++
			}
			if i+n+1 < len(s) && s[i+n+1] == '"' {
				b.WriteString(strings.Repeat(`\\`, n+1))
				i += n
			} else {
				b.WriteString(strings.Repeat(`\`, n+1))
				i += n
			}
		case '"':
			b.WriteString(`\"`)
		default:
			b.WriteByte(s[i])
		}
	}
	b.WriteByte('"')
	return b.String()
}

func exeBaseName(spec StartJobSpec) string {
	exe := spec.ExePath
	if exe == "" {
		exe = spec.Args[0]
	}
	if i := strings.LastIndexAny(exe, `\/`); i >= 0 {
		return exe[i+1:]
	}
	return exe
}

// StdoutPipe / StderrPipe 返回管道读端（调用方负责读到 EOF）。
func (j *JobCmd) StdoutPipe() uintptr { return j.stdoutRd }
func (j *JobCmd) StderrPipe() uintptr { return j.stderrRd }
func (j *JobCmd) RootPID() uint32     { return j.rootPID }
func (j *JobCmd) RootName() string    { return j.rootName }

// Wait 等待子进程退出，返回退出码。
//
// ⚠️ **调用方必须先并发读完 stdout/stderr 两个管道**（本项目用 capWriter），
// 否则子进程写满管道缓冲会阻塞、永不退出，这里就永远等不到。
// 正确用法：两个 goroutine 各自 drain，Wait 与它们并行，最后 join。
func (j *JobCmd) Wait() (uint32, error) {
	pWaitForSingleObject.Call(j.hProc, waitInfinite)
	var code uint32
	if r, _, _ := pGetExitCodeProcess.Call(j.hProc, uintptr(unsafe.Pointer(&code))); r == 0 {
		return 0, fmt.Errorf("%w: GetExitCodeProcess", ErrGetExitCodeProcess)
	}
	return code, nil
}

// Kill 终止整棵进程树。
//
// 三级降级（docs/12 §三 T2-4）：
//
//	① TerminateJobObject        → 整棵树已死
//	② KillTreeSelfContained    → M2 双层防护（照抄 spike，不要重写）
//	③ OpenProcess+TerminateProcess → 只杀直接子进程，至少不比现在差
//
// **每级都记日志** —— PE 现场无法复现，日志是唯一线索。
func (j *JobCmd) Kill() error {
	var firstErr error

	if j.hJob != 0 {
		if err := procTerminateJobObject(j.hJob, 1); err != nil {
			firstErr = err
			logKillFallback("TerminateJobObject 失败，降级到 KillTreeSelfContained: %v", err)
		} else {
			return nil
		}
	}

	// ② 自实现杀树（M2 双层防护在 proc.go）
	killed, errs := KillTreeSelfContained(j.rootPID, j.rootName)
	if killed == 0 && len(errs) == 0 {
		logKillFallback("KillTreeSelfContained 未杀到任何进程（可能已自行退出），pid=%d", j.rootPID)
	}
	if len(errs) > 0 {
		logKillFallback("KillTreeSelfContained 部分失败: %v", errs)
		if firstErr == nil {
			firstErr = fmt.Errorf("kill tree: %v", errs)
		}
	} else if killed > 0 {
		return firstErr
	}

	// ③ 最后兜底：只杀直接子进程
	if err := terminateProcessDirect(j.hProc); err != nil {
		logKillFallback("③ 直接 TerminateProcess 也失败: %v", err)
		if firstErr == nil {
			firstErr = err
		}
	} else {
		logKillFallback("③ 直接 TerminateProcess 成功（仅直接子进程）")
	}
	return firstErr
}

func terminateProcessDirect(hProc uintptr) error {
	if hProc == 0 {
		return errors.New("terminateProcessDirect: hProc == 0")
	}
	if r, _, _ := pTerminateProcess.Call(hProc, 1); r == 0 {
		return fmt.Errorf("%w: pid handle=%d", ErrTerminateProcess, hProc)
	}
	return nil
}

// Close 释放全部句柄。
//
// ⚠️ **关 hJob 会因 KILL_ON_JOB_CLOSE 杀掉整棵树**（job.go 已设该标志）。
// 所以：
//   - 正常收尾（进程已 Wait 完）时关它无害 —— 进程已经没了
//   - 启动中途失败/提前返回时，它会连带杀掉仍在运行的子进程树，
//     这正是我们想要的（不留孤儿）
//
// 这条语义**必须留在注释里**，否则后人不敢动 Close。
func (j *JobCmd) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return nil
	}
	j.closed = true

	for _, h := range []uintptr{j.hJob, j.hProc, j.stdoutRd, j.stderrRd} {
		if h != 0 {
			pCloseHandle.Call(h)
		}
	}
	j.hJob, j.hProc, j.stdoutRd, j.stderrRd = 0, 0, 0, 0
	return nil
}

// OsPipe 把 Win32 管道读端转成 *os.File，供调用方 io.Copy 读到 EOF。
//
// ⚠️ 调用方**必须**并发读 stdout 与 stderr 两个管道，直到 EOF。
// 匿名管道缓冲约 64KB，写满后子进程会阻塞、永不退出 → Wait 死锁。
// 本项目已知有 14.7MB 输出的故障场景，远超任何管道缓冲。
//
// 由调用方负责 Close。
func OsPipe(h uintptr) *os.File {
	return os.NewFile(uintptr(h), "jobcmd-pipe")
}
