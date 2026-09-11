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
// 实际峰值 < 100 个元素（GUI 启动期），长期持有 4KB 引用 + 几百 KB 字符串
// 对 GUI 程序完全可接受。
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
	wstrKeep = append(wstrKeep, p)
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
	wstrKeepSlices = append(wstrKeepSlices, slice)
	return &slice[0], nil
}

// Hold 把外部 *uint16 加入保活集合（用于 syscall.SyscallN 等手写路径，
// 调用方自己 UTF16PtrFromString 但没有用 Ptr/FromCmdline 时）。
func Hold(p *uint16) {
	if p != nil {
		wstrKeep = append(wstrKeep, p)
	}
}

// KeepAlive 是 runtime.KeepAlive 的便利包装，让代码 grep "win.KeepAlive"
// 就能看到所有需要保活的点。
//
// 用法：
//
//	defer win.KeepAlive(p)              // p 至少活到本函数返回
//	win.KeepAlive(p); pSend.Call()      // p 至少活到 Call 返回
func KeepAlive(p *uint16) {
	runtime.KeepAlive(p)
}
