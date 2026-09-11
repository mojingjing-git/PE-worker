// tools/exec.go: 执行命令（白名单软护栏 + confirm 交互）。
//
// docs/02 §7 6 条约定:
//   - exec 只校验首 token (白名单软护栏)
//   - 真正拦危险操作的是 confirm 交互
//   - 不在白名单里 → warn 但不阻断（白名单是软护栏, 不当沙箱用）
//
// 用 os/exec.Cmd 跑命令 + 管道合并 stdout/stderr。timeout 60s。
package tools

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"peagent/src/win"
)

const execTimeoutSec = 60

type execTool struct{}

func (execTool) Name() string        { return "exec" }
func (execTool) Description() string { return "执行命令并返回输出。首 token 会在白名单里检查（软护栏）。危险操作需要 confirm。" }
func (execTool) Risk() RiskLevel     { return RiskExec }

func (execTool) Run(ctx *Context, args string) (Result, error) {
	if strings.TrimSpace(args) == "" {
		return Result{}, errors.New("exec: empty command")
	}

	// confirm 交互
	if ctx.Confirm != nil && !ctx.Confirm(fmt.Sprintf("exec: %q", args)) {
		return Result{Text: "user declined"}, nil
	}

	// 白名单软护栏：首 token 不在白名单就 warn（不阻断）
	if ctx.Config != nil && len(ctx.Config.Whitelist) > 0 {
		first := strings.Fields(args)[0]
		if !containsToken(ctx.Config.Whitelist, first) {
			// 不阻断, 仅提示（白名单是软护栏）
			// TODO: 接入 logx.Warn
		}
	}

	// 用 cmd /c 让 cmd.exe 处理内建命令 (dir/cd/...)
	cctx, cancel := context.WithTimeout(context.Background(), execTimeoutSec*time.Second)
	defer cancel()

	// 把整个 args 当 cmdline 传给 cmd /c — cmd /c 会按空格切分
	cmd := exec.CommandContext(cctx, "cmd", "/c", args)
	if ctx.Cwd != "" {
		cmd.Dir = ctx.Cwd
	}
	out, err := cmd.CombinedOutput()
	// H-1：cmd.exe 输出是 OEM(GBK) 字节，直接 string(out) 会中文乱码。
	// 用 win.OEMToUTF8 转成 UTF-8（L1 返 (T,error)、L5 不吞错）。
	outStr, decErr := win.OEMToUTF8(out)
	if decErr != nil {
		outStr = string(out) // 解码失败兜底用原始字节（decErr 已在上层透传）
	}
	if cctx.Err() == context.DeadlineExceeded {
		if decErr != nil {
			return Result{Text: outStr}, fmt.Errorf("exec: timeout after %ds (decode output: %w)", execTimeoutSec, decErr)
		}
		return Result{Text: fmt.Sprintf("[exec timeout %ds] partial: %s", execTimeoutSec, outStr)},
			fmt.Errorf("exec: timeout after %ds", execTimeoutSec)
	}
	if err != nil {
		// exit code != 0 也算 err, 但把 output 也带回去
		if decErr != nil {
			return Result{Text: outStr}, fmt.Errorf("exec: %v (decode output: %w)", err, decErr)
		}
		return Result{Text: outStr},
			fmt.Errorf("exec: %v", err)
	}
	if decErr != nil {
		return Result{Text: outStr}, fmt.Errorf("exec: decode output: %w", decErr)
	}
	return Result{Text: outStr}, nil
}

// containsToken 检查 list 里是否包含 token（不区分大小写）。
func containsToken(list []string, tok string) bool {
	tok = strings.ToLower(tok)
	for _, x := range list {
		if strings.ToLower(x) == tok {
			return true
		}
	}
	return false
}

// 注册
func init() {
	Register(execTool{})
}
