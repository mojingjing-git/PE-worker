// Package logx: 日志接口 + PostMessage 投递到 UI 线程。
//
// 设计原则：
//   - **线程安全**：多个 goroutine 可同时调 Debug/Info/Warn/Error
//   - **不阻塞 caller**：PostMessage 是 O(1)，立即返
//   - **UI 线程独占显示**：控件只在 UI 线程改，logx 把"显示"推给 UI 线程
//   - **v1-M1 契约**：投递的字符串经 win.Ptr 保活，GC 不回收
//   - **v1-L1 契约**：所有 API 返 (T, error)
//
// 不带 hwnd 时降级为 stderr 写出（产品启动期/PE 测试期/CLI 模式有用）。
//
// P1-7↔P1-8 循环的解：
//   - logx → win (单向), win 不 import logx
//   - 双方都引用 win.WM_LOG_LINE 常量

package logx

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"unsafe"

	"peagent/src/win"
)

// Level 日志级别
type Level int

const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
)

var levelPrefix = map[Level]string{
	LevelDebug: "[D]",
	LevelInfo:  "[I]",
	LevelWarn:  "[W]",
	LevelError: "[E]",
}

var (
	mu   sync.Mutex
	hwnd uintptr    // 0 = 未绑 UI, 降级 stderr
	out  io.Writer = os.Stderr
)

// ErrNoHWND 表示调用时未绑 UI 窗口，logx 降级到 stderr。
// 返回这个 err **不**意味着失败 —— stderr 已成功写出。
var ErrNoHWND = errors.New("logx: no hwnd set; fell back to stderr")

// SetHWND 绑定 UI 线程的窗口句柄。之后所有 log 投递到该窗口的 WM_LOG_LINE。
// 必须在 UI 线程初始化后调一次。
func SetHWND(h uintptr) {
	mu.Lock()
	defer mu.Unlock()
	hwnd = h
}

// HWND 返回当前绑定的窗口句柄（测试用）。
func HWND() uintptr {
	mu.Lock()
	defer mu.Unlock()
	return hwnd
}

// SetOutput 改 fallback sink (默认 os.Stderr)，给测试用。
func SetOutput(w io.Writer) {
	mu.Lock()
	defer mu.Unlock()
	out = w
}

// log 是核心：格式化 + 投递。
func logf(level Level, format string, args ...any) error {
	msg := fmt.Sprintf(format, args...)
	line := levelPrefix[level] + " " + msg + "\r\n"

	mu.Lock()
	h := hwnd
	sink := out
	mu.Unlock()

	if h == 0 {
		// 未绑 UI 窗口：降级 fallback sink
		if _, err := fmt.Fprint(sink, line); err != nil {
			return fmt.Errorf("logx: stderr fallback write: %w", err)
		}
		return ErrNoHWND
	}

	// 投递到 UI 线程：字符串经 win.Ptr 保活
	lp, err := win.Ptr(line)
	if err != nil {
		// line 含 NUL 或空 (理论不会) — 降级 fallback sink, 不静默
		_, _ = fmt.Fprint(sink, line)
		return fmt.Errorf("logx: win.Ptr: %w", err)
	}
	// wparam = level (0/1/2/3 = Debug/Info/Warn/Error)，让 appendLog 拼 [HH:MM:SS] [L] 前缀
	if _, err := win.PostMessageW(h, win.WM_LOG_LINE, uintptr(level), uintptr(unsafe.Pointer(lp))); err != nil {
		_, _ = fmt.Fprint(sink, line) // PostMessage 失败也降级
		return fmt.Errorf("logx: PostMessageW: %w", err)
	}
	win.KeepAlive(lp) // 防止 UI 线程消费前 GC 回收 lp
	return nil
}

// Debug/Info/Warn/Error 四个便捷函数，全部返 err（v1-L1）。
func Debug(format string, args ...any) error { return logf(LevelDebug, format, args...) }
func Info(format string, args ...any) error  { return logf(LevelInfo, format, args...) }
func Warn(format string, args ...any) error  { return logf(LevelWarn, format, args...) }
func Error(format string, args ...any) error { return logf(LevelError, format, args...) }
