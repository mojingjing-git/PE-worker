package win

import (
	"errors"
	"runtime"
	"testing"
	"unsafe"
)

// TestPtr_Valid 验证普通字符串能正确转 UTF-16 指针。
func TestPtr_Valid(t *testing.T) {
	p, err := Ptr("hello")
	if err != nil {
		t.Fatalf("Ptr(hello): err=%v", err)
	}
	if p == nil {
		t.Fatal("Ptr returned nil with no error")
	}
	// deref 验证：前 5 个 uint16 = 'h','e','l','l','o'
	utf16 := (*[5]uint16)(unsafe.Pointer(p))
	if utf16[0] != 'h' || utf16[1] != 'e' || utf16[2] != 'l' || utf16[3] != 'l' || utf16[4] != 'o' {
		t.Fatalf("Ptr content wrong: %v", utf16)
	}
}

// TestPtr_Empty 验证空字符串返回 ErrEmpty（不 panic，不返回 nil + nil）。
func TestPtr_Empty(t *testing.T) {
	_, err := Ptr("")
	if !errors.Is(err, ErrEmpty) {
		t.Fatalf("Ptr(\"\"): want ErrEmpty, got %v", err)
	}
}

// TestPtr_NUL 验证含 NUL 字节的字符串返回 ErrNUL（不 panic）。
func TestPtr_NUL(t *testing.T) {
	_, err := Ptr("hello\x00world")
	if !errors.Is(err, ErrNUL) {
		t.Fatalf("Ptr with NUL: want ErrNUL, got %v", err)
	}
}

// TestFromCmdline_Valid 验证普通命令字符串能正确转 *uint16。
func TestFromCmdline_Valid(t *testing.T) {
	p, err := FromCmdline("cmd.exe /c ver")
	if err != nil {
		t.Fatalf("FromCmdline: err=%v", err)
	}
	if p == nil {
		t.Fatal("FromCmdline returned nil with no error")
	}
}

// TestFromCmdline_Empty 验证空命令返回 ErrEmpty。
func TestFromCmdline_Empty(t *testing.T) {
	_, err := FromCmdline("")
	if !errors.Is(err, ErrEmpty) {
		t.Fatalf("FromCmdline(\"\"): want ErrEmpty, got %v", err)
	}
}

// TestFromCmdline_NUL 验证含 NUL 的命令返回 ErrNUL（不 panic）。
func TestFromCmdline_NUL(t *testing.T) {
	_, err := FromCmdline("cmd\x00.exe")
	if !errors.Is(err, ErrNUL) {
		t.Fatalf("FromCmdline with NUL: want ErrNUL, got %v", err)
	}
}

// TestHold_Nil 验证 Hold(nil) 不 panic（重要：路径上 nil 出现不该崩）。
func TestHold_Nil(t *testing.T) {
	Hold(nil) // 期望：不 panic
}

// TestKeepAlive_NotPanic 验证 KeepAlive 正常调用不 panic。
func TestKeepAlive_NotPanic(t *testing.T) {
	s := "test"
	p, err := Ptr(s)
	if err != nil {
		t.Fatal(err)
	}
	KeepAlive(p) // 期望：不 panic
}

// TestPtrNotGCed 是 PLAN §0.9 §5 的核心契约验证：
// Ptr() 返回的指针在强制多次 GC 之后仍能 deref 出正确内容。
//
// 如果未来有人把 wstrKeep 改成 [:0] 重置（或者移除了 wstrKeep 持有），
// 这个测试会失败 —— 它就是 v1-M1 的回归门禁。
func TestPtrNotGCed(t *testing.T) {
	// 拿一个 Ptr，强制多次 GC，再 deref 看内容。
	// 必须有 NUL 终止符（UTF-16 末尾），让 deref 不会越界读。
	p, err := Ptr("test")
	if err != nil {
		t.Fatal(err)
	}
	// 强制 GC 多次（分配大量临时让 GC 跑 + runtime.GC 显式触发）。
	for i := 0; i < 100; i++ {
		_ = make([]byte, 1024*1024) // 1MB 临时
	}
	runtime.GC()
	runtime.GC()
	runtime.GC()
	// deref 前 4 字符（'t','e','s','t'）
	utf16 := (*[4]uint16)(unsafe.Pointer(p))
	want := []uint16{'t', 'e', 's', 't'}
	for i, c := range want {
		if utf16[i] != c {
			t.Fatalf("Ptr GC'd? got utf16[%d]=%d, want %d", i, utf16[i], c)
		}
	}
}

// TestWstrKeepGrows 验证 wstrKeep 长期增长（不重置）—— v1-M1 契约。
func TestWstrKeepGrows(t *testing.T) {
	before := len(wstrKeep)
	for i := 0; i < 500; i++ {
		_, err := Ptr("x")
		if err != nil {
			t.Fatal(err)
		}
	}
	after := len(wstrKeep)
	if after-before < 500 {
		t.Fatalf("wstrKeep not growing: before=%d, after=%d (delta=%d, want >= 500)",
			before, after, after-before)
	}
}
