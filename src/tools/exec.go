// tools/exec.go: 执行命令（白名单软护栏 + confirm 交互）。
//
// docs/02 §7 6 条约定:
//   - exec 只校验首 token (白名单软护栏)
//   - 真正拦危险操作的是 confirm 交互
//   - 不在白名单里 → warn 但不阻断（白名单是软护栏, 不当沙箱用）
//
// 用 win.StartJobCmd 跑命令（Job Object 杀整棵树）+ 并发排空 stdout/stderr 管道。
// timeout 60s。结果分类阶梯在 runneresult.go，与 run_script 共用。
package tools

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"peagent/src/logx"
	"peagent/src/win"
)

const execTimeoutSec = 60

type execTool struct{}

func (execTool) Name() string { return "exec" }
func (execTool) Description() string {
	return "执行命令并返回输出。首 token 会在白名单里检查（软护栏）。危险操作需要 confirm。"
}
func (execTool) Risk() RiskLevel { return RiskExec }

func (execTool) Run(ctx *Context, args string) (Result, error) {
	if strings.TrimSpace(args) == "" {
		return Result{}, errors.New("exec: empty command")
	}
	// 【S5-审计发现】ctx 为 nil 时下面 `ctx.Confirm` / `ctx.Config` 会 panic。
	// TestExec_Empty 传了 nil 但被 empty args 提前 return 挡住，一直没暴露。
	// 工具注册表允许 RunByName(ctx=nil)，所以这里必须兜住。
	if ctx == nil {
		ctx = &Context{}
	}

	// confirm 交互
	if ctx.Confirm != nil && !ctx.Confirm(fmt.Sprintf("exec: %q", args)) {
		return Result{Text: "user declined"}, nil
	}

	// 白名单：不在名单内 → warn 但**不阻断**（PLAN §0.6 B8：白名单是软护栏 +
	// 审计，不是安全边界）。
	//
	// 审计发现这里原本是**空 body + `// TODO: 接入 logx.Warn`**，也就是两层护栏
	// 实际阻力都是 0：白名单不吭声，而 confirm 回调当前恒返 true。
	// 2026-09-28 决策：维持 auto-yes，但把审计痕迹做全 —— 每次越界执行都留下
	// 可追溯记录，事后能从 smith.log 还原到底跑了什么。
	if ctx.Config != nil && len(ctx.Config.Whitelist) > 0 {
		if fields := strings.Fields(args); len(fields) > 0 {
			if first := fields[0]; !containsToken(ctx.Config.Whitelist, first) {
				_ = logx.Warn("!! exec: 首 token %q 不在白名单内，已放行（软护栏不阻断） cmd=%q",
					first, args)
			}
		}
	}

	// 【S1-1】执行 ctx 从调用方（本轮 agent loop 的 runCtx）派生，而不是
	// context.Background()。之前是两条平行线：用户按 Esc/Stop 时 runCtx 被取消，
	// 但这里的 ctx 完全无感知 → 命令继续跑满 60s，GUI 无任何反应。
	cctx, cancel := ctx.timeoutContext(execTimeoutSec)
	defer cancel()

	// 【T2】改用 Job Object 启动子进程。
	//
	// 为什么不能继续用 os/exec.CommandContext：它只能杀掉**直接子进程**
	// （cmd.exe）。`cmd /c start ping -t 1.1.1.1` 或任何会 spawn 孙进程的命令，
	// 孙进程会变孤儿继续跑 —— 在 PE 里就是一堆僵死的 diskpart / dism 还锁着
	// 磁盘（win/job.go 注释里的原话）。
	//
	// Go 的 os/exec 没有 job 能力（SysProcAttr 9 个字段里没有任何 job 相关），
	// 所以必须自己 CreateProcess —— 见 win/jobexec.go。
	jc, err := win.StartJobCmd(win.StartJobSpec{
		ExePath:   "cmd.exe",
		Args:      []string{"cmd.exe", "/c", args},
		Cwd:       ctx.Cwd,
		Hidden:    true,
		Breakaway: true, // Win7 无嵌套 job，带该 flag 失败时 jobexec 内部会去掉重试
	})
	if err != nil {
		return Result{}, fmt.Errorf("exec: 启动失败: %w", err)
	}
	defer jc.Close()

	// 【S3 + T2】排空管道 + 限流。
	//
	// ⚠️ **必须并发读两个管道**。匿名管道缓冲约 64KB，一旦写满，子进程
	// WriteFile 阻塞 → 永不退出 → jc.Wait() 永远等不到 → 整个 exec 挂死。
	// 而本项目已知有个 14.7MB 输出的故障场景
	//（`for /L %i in (1,1,3000000) do @echo ...`），远超任何管道缓冲。
	//
	// ⚠️ **但超时救不了这一段**（不是回归，只是别把话说满）：os/exec 时代
	// cmd.Run() 同样在等 copy goroutine，这个洞一直存在。超时只保证
	// jc.Wait() 会返回；下面 drainWG.Wait() 仍可能无限阻塞 —— 若 cmd.exe 的
	// 孙进程也以继承方式拿到管道写端（doCreateProcess 用
	// bInheritHandles=TRUE），读端就永远等不到 EOF。而 Win7 无嵌套 job 时
	// jc.Kill() 可能降级到只杀直接子进程，孙进程活下来 → 卡死。
	cw := newCapWriter(maxToolOutputBytes)
	drainErrs := make(chan error, 2)
	var drainWG sync.WaitGroup
	drainWG.Add(2)
	// drain 把一根管道排空到 EOF。
	//
	// ⚠️ 三个不能省的点（与 run_script.go 同构）：
	// ① Take*Pipe 返 nil 必须当**错误**（win/jobexec.go：nil 意味着句柄管理
	//    有 bug，不能当成"这次没输出"）—— P3-20 之前就是恒 nil，而本文件原来
	//    的 `_, _ =` 把它静默吞了，exec 的 stderr 全丢而测试还绿。
	// ② Take*Pipe 已把所有权**移交**调用方（JobCmd.Close 会跳过已被 Take
	//    置 0 的字段），所以**我们负责 Close**，别等 GC finalizer 回收。
	// ③ 两个 goroutine 共写同一个 cw —— capWriter 内部有锁（P3-30 C1），
	//    且 io.Copy 的返回错误必须收进 drainErrs（L5）。
	drain := func(f *os.File, which string) {
		defer drainWG.Done()
		if f == nil {
			drainErrs <- fmt.Errorf("取 %s 管道读端: %w", which, errPipeNotTaken)
			return
		}
		_, copyErr := io.Copy(cw, f)
		closeErr := f.Close() // 所有权已移交，显式关，别等 GC finalizer
		switch {
		case copyErr != nil:
			drainErrs <- fmt.Errorf("排空 %s 管道: %w", which, copyErr)
		case closeErr != nil:
			drainErrs <- fmt.Errorf("关闭 %s 管道: %w", which, closeErr)
		default:
			drainErrs <- nil
		}
	}
	go drain(jc.TakeStdoutPipe(), "stdout")
	go drain(jc.TakeStderrPipe(), "stderr")

	// ctx 取消（Esc/Stop 或超时）→ Kill 整棵树
	killed := make(chan error, 1)
	procDone := make(chan struct{})
	go func() {
		select {
		case <-cctx.Done():
			killed <- jc.Kill()
		case <-procDone:
			killed <- nil
		}
	}()

	exitCode, waitErr := jc.Wait()
	close(procDone) // 进程已退出，kill watcher 随之收尾
	killErr := <-killed
	// 等两个 drain goroutine 收尾，否则会丢掉尾部输出。
	// ⚠️ 这一步**没有超时**：见上面"超时救不了这一段"的说明。
	drainWG.Wait()
	// 容量 2 且 Wait 已过，两个结果必已就绪，不需要 select/drain。
	drainErr := firstErr(<-drainErrs, <-drainErrs)

	runErr := mergeDrainErr(synthRunErr(exitCode, waitErr, killErr), drainErr)

	// H-1：cmd.exe 输出是 OEM(GBK) 字节，直接 string(out) 会中文乱码。
	// 用 win.OEMToUTF8 转成 UTF-8（L1 返 (T,error)、L5 不吞错）。
	outStr := cw.String()
	decoded, decErr := win.OEMToUTF8([]byte(outStr))
	if decErr == nil {
		outStr = decoded
	}
	// 解码失败时保留原始字节串（decErr 会在下面的错误分支里透传，L5）

	// 分类阶梯是共用的（runneresult.go）：run_script 也调它，
	// 两边的中止判定与消息形状因此不会再分叉。
	timedOut, canceled := classifyAbort(cctx)
	return classifyRunOutcome("exec", outStr, decErr, runErr, timedOut, canceled, execTimeoutSec)
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
