// tools/read.go: 4 个只读工具 - ls / cat / grep / find。
//
// 全部用 cmd.exe + 内建命令, 不引入新依赖。
// 跨平台不是目标, 目标是 PE 上一致行为。
package tools

import (
	"errors"
	"fmt"
	"strings"
)

// ls: 列目录. args = 路径, 默认 "."
type lsTool struct{}

func (lsTool) Name() string        { return "ls" }
func (lsTool) Description() string { return "列目录内容。args = 路径, 默认 .(CWD)。" }
func (lsTool) Risk() RiskLevel     { return RiskRead }

func (lsTool) Run(ctx *Context, args string) (Result, error) {
	target := strings.TrimSpace(args)
	if target == "" {
		target = "."
	}
	return runCmd(ctx, fmt.Sprintf("dir %s /B /A", quoteArg(target)))
}

func quoteArg(s string) string {
	if strings.ContainsAny(s, " \t\"&|<>^()") {
		return "\"" + strings.ReplaceAll(s, "\"", "\\\"") + "\""
	}
	return s
}

// cat: 读文件. args = 路径. (用 type 命令, PE 上文件小 OK, 大文件用 read.)
type catTool struct{}

func (catTool) Name() string        { return "cat" }
func (catTool) Description() string { return "读文件内容。args = 路径。" }
func (catTool) Risk() RiskLevel     { return RiskRead }

func (catTool) Run(ctx *Context, args string) (Result, error) {
	if strings.TrimSpace(args) == "" {
		return Result{}, errors.New("cat: empty path")
	}
	return runCmd(ctx, "type "+quoteArg(args))
}

// grep: 找包含 pattern 的行. args = "<pattern> <path>" 或 "<pattern>" (递归当前目录)
type grepTool struct{}

func (grepTool) Name() string { return "grep" }
func (grepTool) Description() string {
	return `findstr 包装。args = "<pattern> <path>" 或 "<pattern>" (递归当前目录)。`
}
func (grepTool) Risk() RiskLevel { return RiskRead }

func (grepTool) Run(ctx *Context, args string) (Result, error) {
	parts := strings.Fields(args)
	if len(parts) == 0 {
		return Result{}, errors.New("grep: empty args")
	}
	pattern := parts[0]
	path := "."
	if len(parts) > 1 {
		path = parts[1]
	}
	return runCmd(ctx, fmt.Sprintf("findstr /S /I /N %s %s", quoteArg(pattern), quoteArg(path)))
}

// find: 按文件名找文件. args = "<pattern> <path>" 或 "<pattern>" (递归)
type findTool struct{}

func (findTool) Name() string        { return "find" }
func (findTool) Description() string { return "按文件名模式找文件。args = \"<pattern> <path>\" 或 \"<pattern>\" (递归 CWD)。" }
func (findTool) Risk() RiskLevel     { return RiskRead }

func (findTool) Run(ctx *Context, args string) (Result, error) {
	parts := strings.Fields(args)
	if len(parts) == 0 {
		return Result{}, errors.New("find: empty args")
	}
	pattern := parts[0]
	path := "."
	if len(parts) > 1 {
		path = parts[1]
	}
	// dir /S /B 递归 + 仅全路径
	return runCmd(ctx, fmt.Sprintf("dir %s /S /B | findstr %s", quoteArg(path), quoteArg(pattern)))
}

// runCmd 是 read 类工具的共 helper —— 复用 execTool 的逻辑（confirm / 白名单）。
// 用 nil 接收者 + 类型转换, 避免循环 import.
func runCmd(ctx *Context, cmdline string) (Result, error) {
	t, _ := Get("exec")
	if t == nil {
		return Result{}, errors.New("exec 工具未注册 (init() 顺序问题)")
	}
	return t.Run(ctx, cmdline)
}

func init() {
	Register(lsTool{})
	Register(catTool{})
	Register(grepTool{})
	Register(findTool{})
}
