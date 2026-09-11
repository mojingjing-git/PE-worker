// tools/write.go: 3 个写文件工具 - write / edit / append。
//
// 全部走 confirm 交互 (RiskWrite 需确认)。写文件用 Go 原生 (不用 cmd)。
package tools

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// write: 写整个文件. args = "<path>\n<content>" (第一行是路径, 剩余是内容)
type writeTool struct{}

func (writeTool) Name() string        { return "write" }
func (writeTool) Description() string { return "写整个文件。args = \"<path>\\n<content>\"。覆盖已存在文件。" }
func (writeTool) Risk() RiskLevel     { return RiskWrite }

func (writeTool) Run(ctx *Context, args string) (Result, error) {
	path, content, err := splitPathContent(args)
	if err != nil {
		return Result{}, err
	}
	if ctx.Confirm != nil && !ctx.Confirm(fmt.Sprintf("write %d 字节到 %q", len(content), path)) {
		return Result{Text: "user declined"}, nil
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return Result{}, fmt.Errorf("write: %w", err)
	}
	return Result{Text: fmt.Sprintf("OK wrote %d bytes to %s", len(content), path)}, nil
}

// edit: 替换文件中 old 为 new. args = "<path>\n<old_sep>\n<new_sep>\n<old>\n<new>" 太复杂,
// 简化: args = "<path>\n<old>\n<new>" 三行
type editTool struct{}

func (editTool) Name() string        { return "edit" }
func (editTool) Description() string { return "替换文件内容。args = 三行: <path> / <old> / <new>。" }
func (editTool) Risk() RiskLevel     { return RiskWrite }

func (editTool) Run(ctx *Context, args string) (Result, error) {
	lines := strings.SplitN(args, "\n", 3)
	if len(lines) < 3 {
		return Result{}, errors.New("edit: 需要 3 行: <path> / <old> / <new>")
	}
	path := strings.TrimSpace(lines[0])
	old := lines[1]
	newStr := lines[2]

	orig, err := os.ReadFile(path)
	if err != nil {
		return Result{}, fmt.Errorf("edit: read %s: %w", path, err)
	}
	content := string(orig)
	count := strings.Count(content, old)
	if count == 0 {
		return Result{}, fmt.Errorf("edit: 找不到要替换的内容 (path=%s)", path)
	}

	preview := fmt.Sprintf("edit %s: 替换 %d 处 (old=%q)", path, count, truncStr(old, 40))
	if ctx.Confirm != nil && !ctx.Confirm(preview) {
		return Result{Text: "user declined"}, nil
	}
	updated := strings.Replace(content, old, newStr, -1)
	if err := os.WriteFile(path, []byte(updated), 0644); err != nil {
		return Result{}, fmt.Errorf("edit: write %s: %w", path, err)
	}
	return Result{Text: fmt.Sprintf("OK 替换 %d 处 in %s", count, path)}, nil
}

// append: 追加到文件末尾. args = "<path>\n<content>"
type appendTool struct{}

func (appendTool) Name() string        { return "append" }
func (appendTool) Description() string { return "追加内容到文件末尾。args = \"<path>\\n<content>\"。" }
func (appendTool) Risk() RiskLevel     { return RiskWrite }

func (appendTool) Run(ctx *Context, args string) (Result, error) {
	path, content, err := splitPathContent(args)
	if err != nil {
		return Result{}, err
	}
	if ctx.Confirm != nil && !ctx.Confirm(fmt.Sprintf("append %d 字节到 %s", len(content), path)) {
		return Result{Text: "user declined"}, nil
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return Result{}, fmt.Errorf("append: open %s: %w", path, err)
	}
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		return Result{}, fmt.Errorf("append: write: %w", err)
	}
	return Result{Text: fmt.Sprintf("OK appended %d bytes to %s", len(content), path)}, nil
}

// splitPathContent 把 "<path>\n<content>" 拆成 (path, content).
func splitPathContent(args string) (string, string, error) {
	parts := strings.SplitN(args, "\n", 2)
	if len(parts) < 2 {
		return "", "", errors.New("需要 2 段: 第一行 path, 剩余 content")
	}
	return strings.TrimSpace(parts[0]), parts[1], nil
}

func truncStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func init() {
	Register(writeTool{})
	Register(editTool{})
	Register(appendTool{})
}
