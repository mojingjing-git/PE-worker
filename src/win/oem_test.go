//go:build windows

// Package win: oem_test.go 验证 OEMToUTF8（H-1）。
//
// GBK fixture 的码位由 Python gbk 编码器独立生成（勿手算）：
//   "中文字体测试" → D6 D0 CE C4 D7 D6 CC E5 B2 E2 CA D4
// 在 OEMCP=936（中文 Windows/PE）的主机上，MultiByteToWideChar(CP_OEMCP)
// 会把这些字节按 GBK 解回正确的 UTF-8。
package win

import "testing"

// gbk中文字体测试 是"中文字体测试"的 GBK 字节（Python gbk 编码器权威生成）。
var gbk中文字体测试 = []byte{0xD6, 0xD0, 0xCE, 0xC4, 0xD7, 0xD6, 0xCC, 0xE5, 0xB2, 0xE2, 0xCA, 0xD4}

func TestOEMToUTF8GBK(t *testing.T) {
	got, err := OEMToUTF8(gbk中文字体测试)
	if err != nil {
		t.Fatalf("OEMToUTF8(GBK) error: %v", err)
	}
	want := "中文字体测试"
	if got != want {
		t.Fatalf("OEMToUTF8(GBK) = %q, want %q", got, want)
	}
}

func TestOEMToUTF8Empty(t *testing.T) {
	got, err := OEMToUTF8(nil)
	if err != nil {
		t.Fatalf("OEMToUTF8(nil) error: %v", err)
	}
	if got != "" {
		t.Fatalf("OEMToUTF8(nil) = %q, want empty", got)
	}
	got, err = OEMToUTF8([]byte{})
	if err != nil {
		t.Fatalf("OEMToUTF8(empty) error: %v", err)
	}
	if got != "" {
		t.Fatalf("OEMToUTF8(empty) = %q, want empty", got)
	}
}

func TestOEMToUTF8PureASCII(t *testing.T) {
	in := []byte("Hello, World!\r\n")
	got, err := OEMToUTF8(in)
	if err != nil {
		t.Fatalf("OEMToUTF8(ASCII) error: %v", err)
	}
	if got != "Hello, World!\r\n" {
		t.Fatalf("OEMToUTF8(ASCII) = %q, want %q", got, "Hello, World!\r\n")
	}
}

func TestOEMToUTF8Mixed(t *testing.T) {
	// 模拟 cmd.exe 真实输出：ASCII 前缀 + GBK 中文 + ASCII 后缀
	in := append([]byte("result: "), gbk中文字体测试...)
	in = append(in, []byte(" ok")...)
	got, err := OEMToUTF8(in)
	if err != nil {
		t.Fatalf("OEMToUTF8(mixed) error: %v", err)
	}
	want := "result: 中文字体测试 ok"
	if got != want {
		t.Fatalf("OEMToUTF8(mixed) = %q, want %q", got, want)
	}
}
