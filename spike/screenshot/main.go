//go:build windows

// spike/screenshot -- GDI 截屏探针（为评估 HTA/任何 GUI 前端提供视觉证据）。
// 优先抓标题匹配的窗口（FindWindowW + GetWindowRect），失败则抓全屏。
// BITMAPINFOHEADER 全部 32 位字段，无 386 对齐陷阱。
//
// 编译：CGO_ENABLED=0 GOOS=windows GOARCH=386 go build -o dist/spike386/screenshot.exe ./spike/screenshot
// 用法：screenshot.exe <窗口标题> <输出.png>；标题为 "-" 时抓全屏。
package main

import (
	"fmt"
	"image"
	"image/png"
	"os"
	"runtime"
	"syscall"
	"unsafe"
)

var (
	user32 = syscall.NewLazyDLL("user32.dll")
	gdi32  = syscall.NewLazyDLL("gdi32.dll")

	pFindWindowW          = user32.NewProc("FindWindowW")
	pGetWindowRect        = user32.NewProc("GetWindowRect")
	pGetDC                = user32.NewProc("GetDC")
	pReleaseDC            = user32.NewProc("ReleaseDC")
	pCreateCompatibleDC   = gdi32.NewProc("CreateCompatibleDC")
	pDeleteDC             = gdi32.NewProc("DeleteDC")
	pCreateCompatibleBitmap = gdi32.NewProc("CreateCompatibleBitmap")
	pSelectObject         = gdi32.NewProc("SelectObject")
	pDeleteObject         = gdi32.NewProc("DeleteObject")
	pBitBlt               = gdi32.NewProc("BitBlt")
	pGetDIBits            = gdi32.NewProc("GetDIBits")
	pGetSystemMetrics     = user32.NewProc("GetSystemMetrics")
	pSetProcessDPIAware   = user32.NewProc("SetProcessDPIAware")
)

const (
	srccopy    = 0x00CC0020
	captureblt = 0x40000000
)

func utf16Ptr(s string) *uint16 { p, _ := syscall.UTF16PtrFromString(s); return p }

func main() {
	runtime.LockOSThread()
	pSetProcessDPIAware.Call() // 非 aware 进程：GetWindowRect 逻辑坐标 vs BitBlt 物理像素会错位（1.5x DPI 实测）
	title := "-"
	if len(os.Args) > 1 {
		title = os.Args[1]
	}
	out := "shot.png"
	if len(os.Args) > 2 {
		out = os.Args[2]
	}

	var x, y, w, h int
	if title != "-" {
		hwnd, _, _ := pFindWindowW.Call(0, uintptr(unsafe.Pointer(utf16Ptr(title))))
		if hwnd != 0 {
			var rect [4]int32 // left, top, right, bottom
			pGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&rect[0])))
			x, y = int(rect[0]), int(rect[1])
			w, h = int(rect[2]-rect[0]), int(rect[3]-rect[1])
			fmt.Printf("WINDOW: (%d,%d) %dx%d\n", x, y, w, h)
		} else {
			fmt.Println("WINDOW: not found, fallback to full screen")
		}
	}
	if w <= 0 || h <= 0 {
		cx, _, _ := pGetSystemMetrics.Call(0)  // SM_CXSCREEN
		cy, _, _ := pGetSystemMetrics.Call(1)  // SM_CYSCREEN
		x, y, w, h = 0, 0, int(cx), int(cy)
		fmt.Printf("SCREEN: %dx%d\n", w, h)
	}

	hdc, _, _ := pGetDC.Call(0)
	defer pReleaseDC.Call(0, hdc)
	memdc, _, _ := pCreateCompatibleDC.Call(hdc)
	defer pDeleteDC.Call(memdc)
	hbm, _, _ := pCreateCompatibleBitmap.Call(hdc, uintptr(w), uintptr(h))
	old, _, _ := pSelectObject.Call(memdc, hbm)
	ret, _, _ := pBitBlt.Call(memdc, 0, 0, uintptr(w), uintptr(h), hdc, uintptr(x), uintptr(y), srccopy|captureblt)
	if ret == 0 {
		fmt.Println("RESULT: FAIL BitBlt")
		os.Exit(1)
	}
	pSelectObject.Call(memdc, old) // GetDIBits 前解除选中（文档要求）

	// BITMAPINFOHEADER：64 字节缓冲（40 头 + 余量），biHeight 取负 = top-down 行序
	var bi [64]byte
	put32 := func(off int, v int32) {
		bi[off] = byte(v); bi[off+1] = byte(v >> 8); bi[off+2] = byte(v >> 16); bi[off+3] = byte(v >> 24)
	}
	put32(0, 40)             // biSize
	put32(4, int32(w))       // biWidth
	put32(8, -int32(h))      // biHeight (top-down)
	put32(12, 1)             // biPlanes
	put32(16, 32)            // biBitCount
	put32(20, 0)             // biCompression = BI_RGB
	buf := make([]byte, w*h*4)

	// 先用屏幕 DC 试，再用 memdc 试；打印两阶段的错误码
	got, _, callErr := pGetDIBits.Call(hdc, hbm, 0, uintptr(h), uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&bi[0])), 0)
	if got == 0 {
		fmt.Printf("GetDIBits(hdc) failed err=%v\n", callErr)
		got, _, callErr = pGetDIBits.Call(memdc, hbm, 0, uintptr(h), uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&bi[0])), 0)
	}
	if got == 0 {
		fmt.Printf("GetDIBits(memdc) failed err=%v\n", callErr)
		// 兜底：两阶段——先让驱动填 bi，再按返回格式来一次
		got, _, callErr = pGetDIBits.Call(hdc, hbm, 0, 0, 0, uintptr(unsafe.Pointer(&bi[0])), 0)
		fmt.Printf("phase1(fill bi) got=%d err=%v\n", got, callErr)
		put32(8, -int32(h)) // phase1 会把 biHeight 重写为正（bottom-up），改回 top-down
		got, _, callErr = pGetDIBits.Call(hdc, hbm, 0, uintptr(h), uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&bi[0])), 0)
		fmt.Printf("phase2 got=%d err=%v\n", got, callErr)
	}
	if got == 0 {
		fmt.Println("RESULT: FAIL GetDIBits")
		os.Exit(2)
	}
	pDeleteObject.Call(hbm)

	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < w*h; i++ {
		img.Pix[i*4+0] = buf[i*4+2] // B
		img.Pix[i*4+1] = buf[i*4+1] // G
		img.Pix[i*4+2] = buf[i*4+0] // R
		img.Pix[i*4+3] = 255
	}
	f, err := os.Create(out)
	if err != nil {
		fmt.Println("RESULT: FAIL create:", err)
		os.Exit(3)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		fmt.Println("RESULT: FAIL encode:", err)
		os.Exit(4)
	}
	fmt.Println("RESULT: PASS", out)
}
