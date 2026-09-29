// Package win 提供 Win32 绑定层（Phase 1 核心）。
//
// 本文件实现 wstr 子模块：Go 字符串与 Win32 UTF-16 之间的安全桥接。
//
// 两条硬规则（PLAN §0.9 §5 + §9 / MEMORY §11 + §0.9）：
//
//  1. syscall.UTF16PtrFromString 返回的 *uint16 一旦被转成 uintptr 交给
//     syscall.Proc.Call，GC 就再也看不到它；从 wcs() 返回到 .Call() 真正陷入
//     内核之间发生 GC，这块内存可能被回收复用，Win32 拿到野指针。
//     32 位 PE 地址空间小、堆复用快，命中概率远高于 64 位。
//     修法：用 wstrKeep 长期持有，**绝不要 [:0] 重置**。
//
//  2. UTF16PtrFromString / UTF16FromString 显式判 err，调用方不许用 _ 吞掉。
//     来自外部输入的字符串（含 NUL）会让 &buf[0] 在异常路径上下标越界 panic。
package win

import (
	"errors"
	"runtime"
	"sync"
	"syscall"
)

// wstrKeep 持有所有交给 Win32 的 UTF-16 缓冲（单 *uint16 形式）。
//
// 为什么长期持有（PLAN §0.9 §5 / MEMORY §11）：
// syscall.UTF16PtrFromString 返回的 *uint16 一旦被转成 uintptr 交给
// syscall.Proc.Call，GC 就再也看不到它。详见包注释。
//
// 为什么不要 [:0] 重置（v1-M1）：
// 即使设个"满了回收"看似控制内存，实为定时炸弹。一旦某个在飞 Win32 Call
// 持有被踢出去的 *uint16，重置 → append 之间的 GC 窗口里 Win32 就会读到
// 野指针，GUI 表现会像"PE 不支持 GUI"这种最难排查的故障。
// 保活集合。
//
// 【T0 修复：M1 契约在集成面被并发 append 打穿】
//
// 这两个是**无锁的包级全局 slice**，而调用方横跨两个线程：
//
//	T1 = UI 线程   logx 在 OnStop（main.go 必打 `!! user abort`）/
//	              OnSend（userInputCh 满时打 dropping warn）时调 Ptr
//	T2 = worker   agent/loop.go 内 5 处 logx.Info / logx.Error
//
// logx.logf 对**每一条**日志行都调 win.Ptr()，所以并发 append 竞态是常态。
//
// 危害不是"少一行日志"：丢失一次 append 意味着那个 *uint16 从此**只被 uintptr
// 引用**，GC 立刻可回收 → UI 线程 appendLog 的
// (*uint16)(unsafe.Pointer(lparam)) 解引用野指针 → 随机花屏/崩溃。
// logx 里的 win.KeepAlive(lp) **救不了**：KeepAlive 只保证本语句之前存活，
// 而这里的解引用跨越线程边界（PostMessage → 消息队列 → UI 线程消费）。
//
// 审计实测丢失率（go1.20.14 / 386 多核 / 5 轮取区间）：
//
//	2 goroutine × 200,000 → 丢 29.7% – 34.4%
//	4 goroutine × 200,000 → 丢 59.5% – 64.3%
//	8 goroutine × 200,000 → 丢 69.7% – 79.1%
//
// 死锁评估：无。Ptr / Hold / FromCmdline 内部只有 UTF16PtrFromString + append，
// 不调用任何其它加锁函数；logx 自己的 mu 是"取完就放"（Lock → Unlock 之后才调
// win.Ptr），两把锁不存在嵌套，无锁序环路。
var wstrKeepMu sync.Mutex

// wstrKeep 持有 syscall.UTF16PtrFromString 返回的 *uint16。
//
// 永不 [:0] 重置（v1-M1）：即使设个"满了回收"看似控制内存，实为定时炸弹。
// 一旦某个在飞 Win32 Call 持有被踢出去的 *uint16，重置 → append 之间的 GC
// 窗口里 Win32 就会读到野指针，GUI 表现会像"PE 不支持 GUI"这种最难排查的故障。
//
// ⚠️ 容量估计（2026-09-28 复审订正）：**不是**"峰值 < 100 个元素"。
// logx 对**每一条日志行**都调 Ptr()，而一次长会话的 agent loop 轻松打几百到
// 几千行 → wstrKeep 可达数千条，单条持有 UTF-16 缓冲约 100 B ~ 2 KB。
// 在 32 位 PE 上按 **< 2 MB** 估。增长速率不受 T0 加锁影响（加锁前后一样）。
//
// 这是**已知限制而非 bug**：M1 明令不许 `[:0]` 重置，而真正的治本方案
// （所有权交接队列，见 docs/12 §一 T0-2）经评估收益低于风险，决定不做。
// 长会话 PE 上若观察到内存单调增长，是预期的。
var wstrKeep []*uint16

// wstrKeepSlices 持有 syscall.UTF16FromString 返回的 []uint16 切片。
//
// UTF16FromString 返回 []uint16（带 NUL 终止的 UTF-16 切片），调用方拿到
// 切片后用 &slice[0] 取首指针交给 Win32。**切片本身**要被 GC 看到，
// 否则 &slice[0] 就成野指针。**和 *uint16 是不同对象**，需要单独的
// 保活集合。
var wstrKeepSlices [][]uint16

// ErrEmpty 显式错误：空字符串不允许转 UTF-16 指针。
var ErrEmpty = errors.New("win: empty string")

// ErrNUL 显式错误：字符串含 NUL 字节，UTF16PtrFromString / UTF16FromString 失败。
var ErrNUL = errors.New("win: string contains NUL byte")

// Ptr 返回字符串 s 的 UTF-16 指针（*uint16）。
// 错误返回 (nil, err)，调用方必须判 err，绝不 panic。
// 内部把 *uint16 append 到 wstrKeep 长期持有，调用方不需要额外 KeepAlive。
//
// 用 syscall.Proc.Call 时调用方自己转 uintptr：
//
//	lp, err := win.Ptr("STATIC")
//	if err != nil { return err }
//	pCreateWindowExW.Call(0, uintptr(unsafe.Pointer(lp)), ...)
//
// 这样设计的原因：返回 *uint16 而非 uintptr，让调用方仍能 KeepAlive(*uint16)
// （uintptr 不被 Go GC 跟踪，runtime.KeepAlive(uintptr) 无效）。
func Ptr(s string) (*uint16, error) {
	if len(s) == 0 {
		return nil, ErrEmpty
	}
	p, err := syscall.UTF16PtrFromString(s)
	if err != nil {
		return nil, ErrNUL
	}
	wstrKeepMu.Lock()
	wstrKeep = append(wstrKeep, p)
	wstrKeepMu.Unlock()
	return p, nil
}

// FromCmdline 把外部输入的命令行（含 NUL 风险）转为 *uint16 给 CreateProcess。
// 显式判 err —— 来自用户/网络的命令可能含 NUL，UTF16FromString 返回错误。
//
// syscall.UTF16FromString 返回 []uint16（UTF-16 切片），不是 *uint16。
// 本函数返回 &slice[0] 的 *uint16，并把切片 append 到 wstrKeepSlices
// 保活。**调用方不需要额外 KeepAlive**。
//
// 用法：
//
//	lp, err := win.FromCmdline("cmd.exe /c ver")
//	if err != nil { return err }
//	pCreateProcessW.Call(0, uintptr(unsafe.Pointer(lp)), ...)
func FromCmdline(cmdline string) (*uint16, error) {
	if len(cmdline) == 0 {
		return nil, ErrEmpty
	}
	slice, err := syscall.UTF16FromString(cmdline)
	if err != nil {
		return nil, ErrNUL
	}
	wstrKeepMu.Lock()
	wstrKeepSlices = append(wstrKeepSlices, slice)
	wstrKeepMu.Unlock()
	return &slice[0], nil
}

// Hold 把外部 *uint16 加入保活集合（用于 syscall.SyscallN 等手写路径，
// 调用方自己 UTF16PtrFromString 但没有用 Ptr/FromCmdline 时）。
func Hold(p *uint16) {
	if p != nil {
		wstrKeepMu.Lock()
		wstrKeep = append(wstrKeep, p)
		wstrKeepMu.Unlock()
	}
}

// UTF16ZToString 把"定长 UTF-16 缓冲 + 尾部 NUL 填充"转成 Go 字符串。
//
// 收拢的是同一个模式在 4 处的重复（P3-32）：先从第一个 NUL 起扫描出有效长度，
// 再交给 syscall.UTF16ToString。调用点分别是 VolumeLabel / ComputerName /
// UserName 三个 Win32 探针的定长栈数组，加上 tools 侧读 CSDVersion。
//
// 为什么不进 wstrKeep：方向是反的。Ptr / FromCmdline 是"Go 字符串 → UTF-16
// 指针交给 Win32"，指针被转成 uintptr 后 GC 就看不见了，才需要长期保活。
// 本函数是"Win32 写好的 UTF-16 缓冲 → Go 字符串"，做的是**值拷贝**：
// syscall.UTF16ToString 内部先建一个全新的 []rune 再 string()，返回值不
// 引用入参 a 的任何内存。调用方转完即丢，a 本身也是栈上或局部的定长数组，
// 所以不存在悬垂 UTF-16 引用，**不违反 M1**。
//
// 调用方约定：a 通常是 `var buf [N]uint16` 这样的定长数组加尾部 NUL。
// 传 a[:]（整段）而不是 a[:n]（调用方自己算的长度）—— 后者在 API 返回的
// 长度大于 len(buf) 时会切片越界 panic，前者由 NUL 扫描兜住真实边界。
func UTF16ZToString(a []uint16) string {
	n := 0
	for n < len(a) && a[n] != 0 {
		n++
	}
	return syscall.UTF16ToString(a[:n])
}

// KeepAlive 是 runtime.KeepAlive 的便利包装，让代码 grep "win.KeepAlive"
// 就能看到所有需要保活的点。
//
// 接 interface{} 接受任意类型：
//   - *uint16：UTF16PtrFromString 的 *uint16 已 append 到 wstrKeep 保活，
//     这里 KeepAlive(*uint16) 让调用方显式声明"在我这条语句之前 GC 别回收它"。
//   - []byte：手工字节缓冲（如 buildJobExtLimitInfo 的 buf），
//     KeepAlive(buf) 让 buf 跨 .Call 调用活着。
//   - *T：任意类型，runtime.KeepAlive 接 interface{}，等效。
//
// 用法：
//
//	defer win.KeepAlive(p)              // p 至少活到本函数返回
//	win.KeepAlive(p); pSend.Call()      // p 至少活到 Call 返回
func KeepAlive(x interface{}) {
	runtime.KeepAlive(x)
}
