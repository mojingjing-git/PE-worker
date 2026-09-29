// tools/read.go: 4 个只读工具 - ls / cat / grep / find。
//
// **为什么不走 cmd.exe（docs/11 S5-1/S5-2/S5-5）**：
//   - 工具参数直接来自 LLM 生成的字符串，而 LLM 输入里可能含 http_get 读回的网页
//     （提示注入路径真实存在）。参数一旦拼进 cmd 字符串就能注入执行任意命令。
//   - cmd.exe **不认** Go 的 \" 转义：argv 直传 `cmd /c dir <arg> /B /A` 时，
//     EscapeArg 产出的 `a\" & echo X & \"b` 会被 cmd 二次解析 —— 闭合引号之后的
//     `&` 重新变成分隔符，注入照样成立（实测输出含 PWNED_ARGV）。
//   - 所以这里改用 Go 标准库做 argv 直传本该做的事：零注入面、带空格路径可用、
//     find 拿到 * / ? 原生通配（findstr 默认当字面量，find "*.txt" 静默返 0 行）。
//   - 附带收益：输出是纯 UTF-8，不再过 cmd.exe 的 OEM(GBK) 往返。
//
// 只读工具 Risk=Read，**不弹 confirm**（RiskLevel 参与决策，见 testReadTools_NoConfirm）。
package tools

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"peagent/src/win"
)

// ls: 列目录. args = 路径, 默认 "."
type lsTool struct{}

func (lsTool) Name() string { return "ls" }
func (lsTool) Description() string {
	return "列目录内容。args = 路径, 默认 .(CWD)。路径可含空格。"
}
func (lsTool) Risk() RiskLevel { return RiskRead }

func (lsTool) Run(ctx *Context, args string) (Result, error) {
	target := strings.TrimSpace(args)
	if target == "" {
		target = "."
	}
	// 等价 `dir <target> /B /A`：只列名字（含隐藏/系统项），不含目录头。
	ents, err := os.ReadDir(resolvePath(ctx, target))
	if err != nil {
		return Result{}, fmt.Errorf("ls: %s: %w", target, err)
	}
	var sb strings.Builder
	for _, e := range ents {
		sb.WriteString(e.Name())
		sb.WriteString("\n")
	}
	return Result{Text: sb.String()}, nil
}

// cat: 读文件. args = 路径. 路径可含空格。
type catTool struct{}

func (catTool) Name() string        { return "cat" }
func (catTool) Description() string { return "读文件内容。args = 路径。路径可含空格。" }
func (catTool) Risk() RiskLevel     { return RiskRead }

func (catTool) Run(ctx *Context, args string) (Result, error) {
	target := strings.TrimSpace(args)
	if target == "" {
		return Result{}, errors.New("cat: empty path")
	}
	raw, err := os.ReadFile(resolvePath(ctx, target))
	if err != nil {
		return Result{}, fmt.Errorf("cat: %s: %w", target, err)
	}
	return Result{Text: decodeText(raw)}, nil
}

// grep: 找包含 pattern 的行. args = "<pattern> <path>" 或 "<pattern>" (递归当前目录)
type grepTool struct{}

func (grepTool) Name() string { return "grep" }
func (grepTool) Description() string {
	return `搜索文件内容（findstr 风格）。args = "<pattern> <path>" 或 "<pattern>" (递归 CWD)。pattern 是正则, 忽略大小写; 非法正则按字面量处理。path 可含空格。`
}
func (grepTool) Risk() RiskLevel { return RiskRead }

func (grepTool) Run(ctx *Context, args string) (Result, error) {
	pattern, path, err := splitPatternPath(args, "grep")
	if err != nil {
		return Result{}, err
	}
	match := newMatcher(pattern)

	var sb strings.Builder
	walkErr := filepath.WalkDir(resolvePath(ctx, path), func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			// 读不了的目录跳过, 不中断整棵树（findstr 同样会跳过）。
			return nil
		}
		if d.IsDir() {
			return nil
		}
		// 逐行流式读: 避免大文件整体进内存（PLAN §0.8 OOM 是 runtime.throw, recover 不住）。
		f, oerr := os.Open(p)
		if oerr != nil {
			return nil
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		line := 0
		for sc.Scan() {
			line++
			txt := sc.Text()
			if match(txt) {
				// findstr /N 的输出形状：路径:行号:行内容。
				// 命中的行不转码: Scanner 可能把多字节字符切在行中间,
				// 按整字节判 UTF-8 有效性会误判并把正常行转花。
				fmt.Fprintf(&sb, "%s:%d:%s\n", p, line, txt)
			}
			if sb.Len() > readOutputCap {
				return filepath.SkipAll
			}
		}
		return nil
	})
	if walkErr != nil {
		return Result{Text: limitOutput(sb.String())}, fmt.Errorf("grep: walk %s: %w", path, walkErr)
	}
	return Result{Text: limitOutput(sb.String())}, nil
}

// find: 按文件名模式找文件. args = "<pattern> <path>" 或 "<pattern>" (递归)
type findTool struct{}

func (findTool) Name() string { return "find" }
func (findTool) Description() string {
	return `按文件名模式找文件（递归）。args = "<pattern> <path>" 或 "<pattern>" (递归 CWD)。pattern 支持 * / ? 通配（*.txt 找所有 .txt）, 无通配符时按子串匹配文件名。path 可含空格。`
}
func (findTool) Risk() RiskLevel { return RiskRead }

func (findTool) Run(ctx *Context, args string) (Result, error) {
	pattern, path, err := splitPatternPath(args, "find")
	if err != nil {
		return Result{}, err
	}
	root := resolvePath(ctx, path)

	var sb strings.Builder
	walkErr := filepath.WalkDir(root, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if matchName(pattern, d.Name(), p, root) {
			fmt.Fprintln(&sb, p)
		}
		if sb.Len() > readOutputCap {
			return filepath.SkipAll
		}
		return nil
	})
	if walkErr != nil {
		return Result{Text: limitOutput(sb.String())}, fmt.Errorf("find: walk %s: %w", path, walkErr)
	}
	return Result{Text: limitOutput(sb.String())}, nil
}

// matchName 复刻 find 的语义：按文件名模式找文件。
// filepath.Match 提供 * / ? 通配 —— findstr 默认把 pattern 当字面量，
// 模型发 "*.txt" 会静默得到 0 行（静默错误答案比报错有害得多）。
func matchName(pattern, base, full, root string) bool {
	if ok, err := filepath.Match(pattern, base); err == nil && ok {
		return true
	}
	if strings.ContainsAny(pattern, `*?`) {
		// 带通配时再按相对路径试一次（find "sub\*.txt"）。
		if rel, rerr := filepath.Rel(root, full); rerr == nil {
			if ok, err := filepath.Match(pattern, rel); err == nil && ok {
				return true
			}
		}
		return false
	}
	// 无通配符 → 不区分大小写子串匹配（find "note" 应能找到 note.txt）。
	return strings.Contains(strings.ToLower(base), strings.ToLower(pattern))
}

// newMatcher 造一个不区分大小写的行匹配器（对应 findstr /I）。
// 非法正则退回不区分大小写子串匹配 —— 静默按字面量搜, 不吞错也不整轮失败。
func newMatcher(pattern string) func(string) bool {
	if re, err := regexp.Compile(`(?i)` + pattern); err == nil {
		return re.MatchString
	}
	lit := strings.ToLower(pattern)
	return func(line string) bool { return strings.Contains(strings.ToLower(line), lit) }
}

// splitPatternPath 解析 "<pattern> <path>"。第 1 段是 pattern，
// 余下整段（含空格）拼回 path —— PE 上 path 带空格是常态，模型还会整体加引号。
func splitPatternPath(args, tool string) (pattern, path string, err error) {
	parts := strings.Fields(args)
	if len(parts) == 0 {
		return "", "", errors.New(tool + ": empty args")
	}
	pattern = parts[0]
	path = "."
	if len(parts) > 1 {
		path = strings.Join(parts[1:], " ")
	}
	if path = strings.Trim(path, `"`); path == "" {
		path = "."
	}
	return pattern, path, nil
}

// resolvePath 把相对路径落到 ctx.Cwd（走 cmd.Dir 的时代由 cmd 负责，搬到纯 Go 后自己拼）。
func resolvePath(ctx *Context, p string) string {
	if ctx == nil || ctx.Cwd == "" || filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(ctx.Cwd, p)
}

// decodeText 把文件字节转成 LLM-friendly 的 UTF-8。
// UTF-8 文件直接用；中文 PE 上常见的 GBK 文件退回 OEM(CP936) 转码。
// 两条都失败就交原始字节 —— 静默变空才是错答案（L5 不吞内容）。
func decodeText(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	if utf8.Valid(raw) {
		return limitOutput(string(raw))
	}
	if s, err := win.OEMToUTF8(raw); err == nil && s != "" {
		return limitOutput(s)
	}
	return limitOutput(string(raw))
}

// limitOutput 截断超长输出，尾部标明省略字节数 —— 让模型知道还有内容没看，
// 而不是假装看全了（docs/11 S3 同款策略）。
//
// ⚠️ 这里原本写着"exec 侧那份常量在 exec.go，不在此处定义以免重名"——
// **该说法已失效**（P3-28 核实全项目无此说法的依据）：那份常量
// `maxToolOutputBytes` 在 **limited_writer.go**，不在 exec.go；而下面这个
// `maxBytes`（512*1024，与 maxToolOutputBytes 同值）是**重复定义**。
// 合并二者属 docs/13 Task D3 的活，刻意不在此动，以免与 D3 打架。
func limitOutput(s string) string {
	const maxBytes = 512 * 1024
	if len(s) <= maxBytes {
		return s
	}
	cut := maxBytes
	// 回退到 rune 边界，避免把一个 UTF-8 多字节字符劈成乱码。
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + fmt.Sprintf("\n...[truncated %d bytes]", len(s)-cut)
}

// readOutputCap 是单次 ls/grep/find 累积输出的停写阈值（同 512KB，见 limitOutput）。
const readOutputCap = 512 * 1024

func init() {
	Register(lsTool{})
	Register(catTool{})
	Register(grepTool{})
	Register(findTool{})
}
