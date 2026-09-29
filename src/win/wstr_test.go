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

// wstrKeepLen 在锁下读 wstrKeep 长度。
// wstrKeep 是无锁语义的包级全局（T0 之后靠 wstrKeepMu 保护 append），
// 测试断言它的长度时必须走同一把锁，否则并发用例（wstr_concurrency_test.go）
// 与本文件互相构成 data race。
func wstrKeepLen() int {
	wstrKeepMu.Lock()
	defer wstrKeepMu.Unlock()
	return len(wstrKeep)
}

// TestHold_Nil 验证 Hold(nil) 不 panic（重要：路径上 nil 出现不该崩），
// 且**不会把 nil 塞进保活集合**。
//
// 修 B4：原实现是裸 `Hold(nil) // 期望：不 panic`，零断言 —— "不崩"这件事
// Go 本来就不需要测（崩了就是 panic，整个包全红），所以这条门禁什么都拦不住。
// 这里给出真断言：nil 必须被忽略（长度不变），并配一条正向对照证明
// Hold 对非 nil 指针确实会 append（否则上面那条会因为 Hold 整个不做事而恒真）。
func TestHold_Nil(t *testing.T) {
	before := wstrKeepLen()
	Hold(nil) // 期望：不 panic，且不改变 wstrKeep
	after := wstrKeepLen()
	if after != before {
		t.Fatalf("Hold(nil) 改变了 wstrKeep 长度: before=%d after=%d —— nil 被 append 进保活集合了？", before, after)
	}

	// 正向对照：非 nil 必须 +1（Ptr 自己 +1，Hold 再 +1）。
	p, err := Ptr("hold-nil-probe")
	if err != nil {
		t.Fatalf("Ptr: %v", err)
	}
	Hold(p)
	if got := wstrKeepLen(); got != after+2 {
		t.Fatalf("Hold(非 nil) 后期望 wstrKeep 长度 = %d（Ptr +1、Hold +1），实际 %d", after+2, got)
	}
}

// TestKeepAlive_NotPanic 验证 KeepAlive 正常调用不 panic，**并**给出正向断言。
//
// 修 B4：原实现只有 `KeepAlive(p) // 期望：不 panic`，零断言。
// 诚实说明覆盖边界：runtime.KeepAlive 的效果依赖 GC 时序，**任何测试都无法
// 判定它被删掉了仍会红**。这里能真拦住的回归是"KeepAlive 改坏了它的入参"
// （清零 / 换指针 / panic）以及"Ptr 不再 NUL 终止"——这三条都是真断言。
func TestKeepAlive_NotPanic(t *testing.T) {
	s := "test"
	p, err := Ptr(s)
	if err != nil {
		t.Fatal(err)
	}
	KeepAlive(p) // 期望：不 panic
	// 正向断言 1：KeepAlive 之后指针仍指向我们放进去的内容（没被清零/换掉）。
	utf16 := (*[5]uint16)(unsafe.Pointer(p))
	want := []uint16{'t', 'e', 's', 't'}
	for i, c := range want {
		if utf16[i] != c {
			t.Fatalf("KeepAlive 后 utf16[%d]=%d, want %d", i, utf16[i], c)
		}
	}
	// 正向断言 2：第 5 个 uint16 必须是 NUL —— Win32 侧按 C 字符串读，
	// 缺了就是野指针读越界。Ptr 少写终止符时这条会红。
	if utf16[4] != 0 {
		t.Errorf("Ptr(%q) 缺 NUL 终止符: utf16[4]=%d", s, utf16[4])
	}
	// 正向断言 3：KeepAlive 还接 []byte（buildJobExtLimitInfo 的手工字节缓冲
	// 走这条路，之前完全没有测试覆盖）。同样要有断言而不是"没崩就算过"。
	buf := make([]byte, 16)
	buf[0] = 0xAB
	KeepAlive(buf)
	if buf[0] != 0xAB {
		t.Errorf("KeepAlive([]byte) 改写了入参: buf[0]=%#x, want 0xab", buf[0])
	}
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
