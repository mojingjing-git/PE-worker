// tools/run_script.go: 写临时 .bat + cmd /c 执行。
//
// docs/02 §7: run_script 不经白名单（直接走 .bat）。
// PE 上最小化：写 utf-8 .bat 到 temp 目录 → cmd /c 跑 → 删。
//
// 编码：cmd 默认 OEM 代码页，utf-8 写 .bat 会中文乱码。
// 这里**只**声明支持 ASCII 脚本（不传中文）。
package tools

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const runScriptTimeoutSec = 120

type runScriptTool struct{}

func (runScriptTool) Name() string        { return "run_script" }
func (runScriptTool) Description() string { return "把脚本写到临时 .bat 文件并执行。脚本必须 ASCII（不传中文）。不经白名单。危险操作需要 confirm。" }
func (runScriptTool) Risk() RiskLevel     { return RiskDangerous }

func (runScriptTool) Run(ctx *Context, args string) (Result, error) {
	if strings.TrimSpace(args) == "" {
		return Result{}, errors.New("run_script: empty script")
	}

	// confirm 交互（run_script 跳过白名单但 confirm 仍要）
	if ctx.Confirm != nil && !ctx.Confirm(fmt.Sprintf("run_script: %d 字节脚本", len(args))) {
		return Result{Text: "user declined"}, nil
	}

	// 写临时 .bat（仅 ASCII 检查）
	for _, r := range args {
		if r > 127 {
			return Result{}, errors.New("run_script: script 含非 ASCII 字符, cmd.exe 按 OEM 解码会乱码; 请用 ASCII 或加 chcp 65001 头")
		}
	}

	dir := os.TempDir()
	path := filepath.Join(dir, fmt.Sprintf("peagent_run_%d.bat", time.Now().UnixNano()))
	if err := os.WriteFile(path, []byte(args), 0644); err != nil {
		return Result{}, fmt.Errorf("run_script: write %s: %w", path, err)
	}
	defer os.Remove(path) // 跑完删, PE 上 X: 内存盘, 别堆

	cctx, cancel := timeoutContext(runScriptTimeoutSec)
	defer cancel()

	cmd := exec.CommandContext(cctx, "cmd", "/c", path)
	if ctx.Cwd != "" {
		cmd.Dir = ctx.Cwd
	}
	out, err := cmd.CombinedOutput()
	if cctx.Err() == context_DeadlineExceeded {
		return Result{Text: fmt.Sprintf("[run_script timeout %ds] partial: %s", runScriptTimeoutSec, string(out))},
			fmt.Errorf("run_script: timeout after %ds", runScriptTimeoutSec)
	}
	if err != nil {
		return Result{Text: string(out)},
			fmt.Errorf("run_script: %v", err)
	}
	return Result{Text: string(out)}, nil
}

func init() {
	Register(runScriptTool{})
}
