// P2-2: think 块检测单测
//
// 验证 findThinkBlocks 在各种输入下返正确 spans。EM_SETCHARFORMAT 实际染色
// 走 EDIT 控件，需要真机 GUI 验证（headless 没法试 EM_SETCHARFORMAT）。
package win

import (
	"reflect"
	"syscall"
	"testing"
	"unsafe"
)

// TestWndClassExWSize 钉住 wndClassExW 的 cbSize 尺寸：386=48 / amd64=80。
//
// Run() 用 unsafe.Sizeof(wndClassExW{}) 当 CbSize 传给 RegisterClassExW，而这个
// 结构体含 8 个 uintptr 成员（AGENTS.md §3 的 S1 场景），尺寸随架构变。改字段后
// cbSize 跟着变，而写错时 RegisterClassExW 只静默返 0，Run() 直接 return 1
// —— 主窗口根本不出现，无头环境看不见，要到 PE 现场才暴露。参照
// TestMemoryStatusExSize（P3-24）给两个架构都上真断言，让 gui.go 注释里那行
// 数字不再单独背书。
func TestWndClassExWSize(t *testing.T) {
	sz := unsafe.Sizeof(wndClassExW{})
	ptr := unsafe.Sizeof(uintptr(0))

	// 期望值来自 Win32 ABI（winuser.h 的 WNDCLASSEXW）：4 个 4 字节字段
	// + 8 个指针宽字段，386 上指针 4 字节，amd64 上指针 8 字节。
	var want uintptr
	switch ptr {
	case 4:
		want = 48
	case 8:
		want = 80
	default:
		t.Fatalf("未知指针宽度 %d 字节，本项目只构建 386 / amd64", ptr)
	}

	if sz != want {
		t.Fatalf("unsafe.Sizeof(wndClassExW{}) = %d，%d-bit 上期望 %d —— "+
			"它是 RegisterClassExW 的 cbSize，尺寸错位会让主窗口注册失败", sz, ptr*8, want)
	}
	t.Logf("unsafe.Sizeof(wndClassExW{}) = %d 字节 (指针 %d 字节, %d-bit)", sz, ptr, ptr*8)
}

func TestFindThinkBlocks(t *testing.T) {
	// lineBuf 是 []uint16，索引 = UTF-16 单元数（ASCII 1 char = 1 unit）。
	// span 包含 <think> 和 </think> tag 本身（整段视觉缩字号）。
	// End 是开区间，指向 </think> 之后位置。没 close 时 span End = n。
	// 注意：syscall.UTF16FromString 含 NUL 终止符，所以 len(buf) = chars+1。
	cases := []struct {
		name  string
		input string
		want  []thinkSpan
	}{
		{"no think", "hello world", nil},
		// <think>(7) + reasoning(9) + </think>(8) = 24 chars
		{"one block mid", "<think>reasoning</think> reply", []thinkSpan{{0, 24}}},
		// <think>(7) + a(1) + </think>(8) = 16 chars
		{"one block prefix", "<think>a</think>", []thinkSpan{{0, 16}}},
		// <think>a</think>(16) + " text "(6) + <think>b</think>(16) = 38 chars
		{"two blocks", "<think>a</think> text <think>b</think>", []thinkSpan{{0, 16}, {22, 38}}},
		// <think>(7) + "no close"(8) = 15 chars + NUL; 没 close → e=n=16
		{"unclosed to end", "<think>no close", []thinkSpan{{0, 16}}},
		// <think>(7) + </think>(8) = 15 chars
		{"empty think", "<think></think>", []thinkSpan{{0, 15}}},
		// "hello"(5) + <think>(7) + "reasoning"(9) + " only"(5) = 26 chars + NUL;
		// 没 close → e=n=27
		{"think at end no reply", "hello<think>reasoning only", []thinkSpan{{5, 27}}},
		// <think>(7) + <think>inner</think>(20) + </think>(8) = 35 chars;
		// 第一个 <think> 找首个 </think>: s=0, e=27
		{"nested-ish (first wins)", "<think><think>inner</think></think>", []thinkSpan{{0, 27}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			buf, _ := syscall.UTF16FromString(c.input)
			got := findThinkBlocks(buf)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("input %q (len=%d)\n  got  %+v\n  want %+v", c.input, len(buf), got, c.want)
			}
		})
	}
}

func TestNewCharFormatSize(t *testing.T) {
	cf := newCharFormatSize(140)
	const wantSize = 92 // CHARFORMATW actual layout size
	if len(cf) != wantSize {
		t.Fatalf("size = %d, want %d", len(cf), wantSize)
	}
	// cbSize
	cbSize := uint32(cf[0]) | uint32(cf[1])<<8 | uint32(cf[2])<<16 | uint32(cf[3])<<24
	if cbSize != wantSize {
		t.Errorf("cbSize = %d, want %d", cbSize, wantSize)
	}
	// dwMask = CFM_SIZE = 0x80000000
	dwMask := uint32(cf[4]) | uint32(cf[5])<<8 | uint32(cf[6])<<16 | uint32(cf[7])<<24
	if dwMask != 0x80000000 {
		t.Errorf("dwMask = %#x, want 0x80000000", dwMask)
	}
	// yHeight at offset 12 = 140
	yH := uint32(cf[12]) | uint32(cf[13])<<8 | uint32(cf[14])<<16 | uint32(cf[15])<<24
	if yH != 140 {
		t.Errorf("yHeight = %d, want 140", yH)
	}
	// 其他字段默认 0 (dwEffects=8, yOffset=16, crTextColor=20, bCharSet=24, bFamily=25, ...)
	// 抽样检查 yOffset 应该是 0（不染 dyOffset）
	yOff := uint32(cf[16]) | uint32(cf[17])<<8 | uint32(cf[18])<<16 | uint32(cf[19])<<24
	if yOff != 0 {
		t.Errorf("yOffset = %d, want 0", yOff)
	}
}
