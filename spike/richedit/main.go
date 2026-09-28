//go:build windows

// spike/richedit -- 验证「PE 里能否用 LoadLibrary 加载 riched20.dll +
// CreateWindowExW("RichEdit20W") + EM_SETCHARFORMAT 改字号」这条便携 RichEdit 路线。
//
// 背景（eval-win32 评估用）：
//   gui.go 现在的日志区是普通 EDIT 控件，EM_SETCHARFORMAT（RichEdit 专有）
//   被 EDIT 直接忽略，所以 think 块缩小字号不生效。把它换成 RichEdit 即可让
//   现有 newCharFormatSize(92) + styleThinkBlocks 直接生效。
//
// 本探针做的事（纯验证，不 import src/）：
//   1. 优先 LoadLibraryW(exe 同目录/riched20.dll)，失败再 LoadLibraryW("riched20.dll")
//   2. 取加载到的 DLL 真实路径，并用 PE 导入表解析列出它的依赖 DLL
//   3. CreateWindowExW("RichEdit20W") 建一个只读多行 RichEdit
//   4. EM_EXLIMITTEXT 把缓冲上限抬到 1<<24（避免 32K/64K 默认上限）
//   5. 写一段含 <think> 的文本，再 EM_SETSEL + EM_SETCHARFORMAT(SCF_SELECTION)
//      把 think 段 yHeight 设成 100 twips，验证返回值非 0
//   6. 打印 386/ amd64 各自的结果
//
// 编译（本机 Win 开发机，不要在本沙箱跑，沙箱无 Windows DLL / go 工具链）：
//   CGO_ENABLED=0 GOOS=windows GOARCH=386 <go1.20.14> build -o spike386/richedit.exe ./spike/richedit
//   CGO_ENABLED=0 GOOS=windows GOARCH=amd64 <go1.20.14> build -o spike64/richedit.exe ./spike/richedit
//
// 预期：Win7/10/11 桌面（有 riched20.dll + 依赖）上两架构都 PASS。
//       PE 精简镜像若缺 riched20 的依赖（如 msls31.dll），LoadLibrary 会失败 →
//       应用层须降级回 EDIT（见 gui.go 评估）。本探针只负责把"能不能加载/生效"
//       这件事在真机上量化出来。
package main

import (
	"encoding/binary"
	"fmt"
	"os"
	"runtime"
	"syscall"
	"unsafe"
)

var (
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	user32   = syscall.NewLazyDLL("user32.dll")

	pLoadLibraryW       = kernel32.NewProc("LoadLibraryW")
	pGetModuleFileNameW = kernel32.NewProc("GetModuleFileNameW")
	pFreeLibrary         = kernel32.NewProc("FreeLibrary")
	pCreateWindowExW    = user32.NewProc("CreateWindowExW")
	pDestroyWindow       = user32.NewProc("DestroyWindow")
	pGetMessageW         = user32.NewProc("GetMessageW")
	pDefWindowProcW      = user32.NewProc("DefWindowProcW")
	pSendMessageW        = user32.NewProc("SendMessageW")
)

const (
	cpOEMCP = 1
	// EM_*
	emSetSel        = 0x00B1
	emReplaceSel    = 0x00C2
	emExLimitText   = 0x0437
	emSetLimitText  = 0x00D5 // EM_SETLIMITTEXT：EDIT/RichEdit 都认，wParam=limit
	emSetCharFormat = 0x0444
	scfSelection    = 0x0001
	cfmSize         = 0x80000000
	// 风格
	wsOverlappedWindow = 0x00CF0000
	wsChild            = 0x40000000
	wsVisible          = 0x10000000
	wsVScroll          = 0x00200000
	wsExClientEdge     = 0x00000200
	esMultiline        = 0x0004
	esReadonly         = 0x0800
	esAutoVScroll      = 0x0040
	wmClose            = 0x0010
)

func utf16Ptr(s string) *uint16 { p, _ := syscall.UTF16PtrFromString(s); return p }

func loadRiched20() (uintptr, string) {
	// 1) exe 同目录优先（让 PE 里可随 exe 附带 riched20.dll）
	n, _, _ := pGetModuleFileNameW.Call(0, 0, 0) // 先取所需长度
	buf := make([]uint16, n+1)
	pGetModuleFileNameW.Call(0, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	dir := ""
	for i := len(buf) - 1; i >= 0; i-- {
		if buf[i] == '\\' {
			dir = string(utf16ToRunes(buf[:i]))
			break
		}
	}
	if dir != "" {
		local := dir + "\\riched20.dll"
		lp := utf16Ptr(local)
		if h, _, _ := pLoadLibraryW.Call(uintptr(unsafe.Pointer(lp))); h != 0 {
			runtime.KeepAlive(lp)
			return h, local
		}
	}
	// 2) 系统目录
	np := utf16Ptr("riched20.dll")
	if h, _, _ := pLoadLibraryW.Call(uintptr(unsafe.Pointer(np))); h != 0 {
		runtime.KeepAlive(np)
		return h, "system:riched20.dll"
	}
	return 0, ""
}

func utf16ToRunes(b []uint16) []rune {
	out := make([]rune, 0, len(b))
	for _, c := range b {
		out = append(out, rune(c))
	}
	return out
}

func main() {
	runtime.LockOSThread() // 项目铁律：窗口创建前必须锁线程，否则 goroutine 迁移后 SendMessage 跨线程 → 非确定性 AV（386 也中过）
	fmt.Printf("== spike/richedit == arch=%d-bit\n", 8*unsafe.Sizeof(uintptr(0)))

	h, path := loadRiched20()
	if h == 0 {
		fmt.Println("RESULT: LOAD_FAIL  riched20.dll 无法加载（PE 缺 DLL 或依赖）")
		os.Exit(2)
	}
	defer pFreeLibrary.Call(h)
	fmt.Printf("LOADED: %s\n", path)

	// 解析依赖 DLL 名（用加载到的模块真实路径，别用标签字符串）
	var realPath string
	{
		pbuf := make([]uint16, 512)
		n, _, _ := pGetModuleFileNameW.Call(h, uintptr(unsafe.Pointer(&pbuf[0])), uintptr(len(pbuf)))
		if n > 0 {
			realPath = string(utf16ToRunes(pbuf[:n]))
		}
	}
	fmt.Printf("REAL: %s\n", realPath)
	deps := peImportDlls(realPath)
	fmt.Printf("DEPS(%d): %v\n", len(deps), deps)

	// 建 RichEdit20W（顶层窗口：WS_CHILD 必须有父窗，否则 CreateWindowExW 必败）
	// 注意 M1：*uint16 转 uintptr 后 GC 看不见，必须 KeepAlive 续命 —— amd64 实测
	// 不续命会在后续 EM_REPLACESEL 处 0xc0000005（v1 探针就栽在这）。
	clsPtr := utf16Ptr("RichEdit20W")
	hwnd, _, _ := pCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(clsPtr)),
		0,
		wsOverlappedWindow|wsVisible|wsVScroll|esMultiline|esReadonly|esAutoVScroll,
		10, 10, 420, 300, 0, 0, 0, 0,
	)
	runtime.KeepAlive(clsPtr)
	if hwnd == 0 {
		fmt.Println("RESULT: CREATE_FAIL  RichEdit20W 类未注册 / 建窗失败")
		os.Exit(3)
	}
	defer pDestroyWindow.Call(hwnd)
	fmt.Println("CREATED: RichEdit20W ok")

	// 差分实验：先试 REPLACESEL（不设 limit），再单独试 EXLIMITTEXT，定位崩溃点
	pSend := func(msg, w, l uintptr) uintptr {
		r, _, _ := pSendMessageW.Call(hwnd, msg, w, l)
		return r
	}
	fmt.Println("STEP: replaceSel")
	text := "ai > 先看磁盘。\r\n<think>这里在推理为什么 C 盘满了</think>\r\nai > C 盘剩余 12GB。"
	txtPtr := utf16Ptr(text)
	pSend(emReplaceSel, 0, uintptr(unsafe.Pointer(txtPtr)))
	runtime.KeepAlive(txtPtr)
	fmt.Println("STEP: replaceSel ok")

	fmt.Println("STEP: setLimitText (EM_SETLIMITTEXT 0xD5)")
	pSend(emSetLimitText, 1<<20, 0)
	fmt.Println("STEP: setLimitText ok")

	// 找 <think> 段并改字号（仿 styleThinkBlocks）
	const tagS, tagE = "<think>", "</think>"
	full, _ := syscall.UTF16FromString(text)
	lineStr := string(utf16ToRunes(full))
	s0 := indexOf(lineStr, tagS)
	e0 := indexOf(lineStr, tagE)
	if s0 >= 0 && e0 >= 0 {
		start := uintptr(s0)
		end := uintptr(e0 + len(tagE))
		pSend(emSetSel, start, end)
		// CHARFORMATW 92 字节，仅设 cbSize/dwMask=CFM_SIZE/yHeight=100 twips
		cf := make([]byte, 92)
		binary.LittleEndian.PutUint32(cf[0:4], 92)
		binary.LittleEndian.PutUint32(cf[4:8], cfmSize)
		binary.LittleEndian.PutUint32(cf[12:16], 100)
		ret, _, _ := pSendMessageW.Call(hwnd, emSetCharFormat, scfSelection, uintptr(unsafe.Pointer(&cf[0])))
		if ret != 0 {
			fmt.Println("RESULT: PASS  EM_SETCHARFORMAT 在 RichEdit20W 上生效 (ret!=0)")
		} else {
			fmt.Println("RESULT: CHARFMT_IGNORED  ret==0（罕见，可能是 cbSize 不匹配）")
		}
	} else {
		fmt.Println("RESULT: PASS  (无 think 段，控件创建本身已验证)")
	}
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// peImportDlls 读一个 PE 文件，列出它的导入依赖 DLL 名（即 Import Descriptor 的 Name 字段）。
// 支持 PE32(386) 与 PE32+(amd64)。best-effort：解析失败返回空切片。
func peImportDlls(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	if len(data) < 0x40 || string(data[0:2]) != "MZ" {
		return nil
	}
	eLfanew := binary.LittleEndian.Uint32(data[0x3C:])
	if int(eLfanew)+24 > len(data) {
		return nil
	}
	if string(data[eLfanew:eLfanew+4]) != "PE\x00\x00" {
		return nil
	}
	// COFF: Machine(2) + NumberOfSections(2) ... OptionalHeaderSize at e_lfanew+20(2)
	optSize := binary.LittleEndian.Uint16(data[eLfanew+20:])
	optOff := int(eLfanew) + 24
	if optOff+int(optSize) > len(data) {
		return nil
	}
	magic := binary.LittleEndian.Uint16(data[optOff:])
	var dataDirOff int
	switch magic {
	case 0x10B: // PE32
		dataDirOff = optOff + 96
	case 0x20B: // PE32+
		dataDirOff = optOff + 112
	default:
		return nil
	}
	// IMAGE_DIRECTORY_ENTRY_IMPORT = 1，8 字节(VirtualAddress, Size)
	impVA := binary.LittleEndian.Uint32(data[dataDirOff+8 : dataDirOff+12])
	if impVA == 0 {
		return nil
	}
	// 解析节表做 RVA→文件偏移
	secOff := optOff + int(optSize)
	numSec := int(binary.LittleEndian.Uint16(data[eLfanew+2:]))
	type sec struct{ va, raw uint32 }
	var secs []sec
	for i := 0; i < numSec; i++ {
		o := secOff + i*40
		if o+20 > len(data) {
			break
		}
		v := binary.LittleEndian.Uint32(data[o+12 : o+16]) // VirtualAddress
		r := binary.LittleEndian.Uint32(data[o+20 : o+24]) // PointerToRawData
		secs = append(secs, sec{v, r})
	}
	rva2off := func(va uint32) uint32 {
		for _, s := range secs {
			if va >= s.va && va < s.va+0x10000000 { // 宽松区间
				return s.raw + (va - s.va)
			}
		}
		return va
	}
	// 遍历 Import Descriptor（每个 20 字节，末尾全 0）
	var out []string
	off := int(rva2off(impVA))
	for {
		if off+20 > len(data) {
			break
		}
		nameRVA := binary.LittleEndian.Uint32(data[off+12 : off+16])
		if nameRVA == 0 {
			break
		}
		no := int(rva2off(nameRVA))
		if no >= len(data) {
			break
		}
		end := no
		for end < len(data) && data[end] != 0 {
			end++
		}
		out = append(out, string(data[no:end]))
		off += 20
	}
	return out
}
