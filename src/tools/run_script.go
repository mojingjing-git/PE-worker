// tools/run_script.go: 写临时 .bat + cmd /c 执行。
//
// docs/02 §7: run_script 不经白名单（直接走 .bat）。
// PE 上最小化：写 utf-8 .bat 到 temp 目录 → cmd /c 跑 → 删。
//
// 【P3-30】改走 win.StartJobCmd（原 os/exec.CommandContext）：
// os/exec 只能杀**直接子进程** cmd.exe，脚本里 spawn 的孙进程（脚本最常见
// 的就是 start / call 一个安装器）会变孤儿继续跑 —— 在 PE 里就是一堆僵死的
// 进程还锁着磁盘。exec 早已迁到 Job Object（exec.go 的 T2 批），两条平行
// 实现正是 A1 那种句柄 bug 只在一处发生的结构性原因，现在收成一条。
//
// 编码：cmd 默认 OEM 代码页，utf-8 写 .bat 会中文乱码。
// 这里**只**声明支持 ASCII 脚本（不传中文）。
package tools

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"peagent/src/logx"
	"peagent/src/win"
)

const runScriptTimeoutSec = 120

type runScriptTool struct{}

func (runScriptTool) Name() string { return "run_script" }
func (runScriptTool) Description() string {
	return "把脚本写到临时 .bat 文件并执行。脚本必须 ASCII（不传中文）。不经白名单。危险操作需要 confirm。"
}
func (runScriptTool) Risk() RiskLevel { return RiskDangerous }

func (runScriptTool) Run(ctx *Context, args string) (Result, error) {
	if strings.TrimSpace(args) == "" {
		return Result{}, errors.New("run_script: empty script")
	}
	// 【S5-审计发现】ctx 可能为 nil（RunByName 允许传 nil），解引用会 panic。
	if ctx == nil {
		ctx = &Context{}
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
	// ⚠️ **这里绝对不能 `defer os.Remove(path)`** —— 理由见 Wait 之后那次删除。
	// 删除做成一个显式函数，由两处终端路径各调一次（PE 跑在内存盘上，别堆）。
	removeBat := func() {
		if err := os.Remove(path); err != nil {
			_ = logx.Warn("run_script: 删临时 .bat 失败 %s: %v", path, err)
		}
	}

	// 【S1-1】从本轮 runCtx 派生，Esc/Stop 能真正中止脚本
	cctx, cancel := ctx.timeoutContext(runScriptTimeoutSec)
	defer cancel()

	// 【P3-30】与 exec.go 同构：CreateProcess(CREATE_SUSPENDED) → 建 job →
	// Assign → Resume（顺序见 win/jobexec.go，硬规则 J1 不可换）。
	jc, err := win.StartJobCmd(win.StartJobSpec{
		ExePath:   "cmd.exe",
		Args:      []string{"cmd.exe", "/c", path},
		Cwd:       ctx.Cwd,
		Hidden:    true,
		Breakaway: true, // Win7 无嵌套 job，带该 flag 失败时 jobexec 内部会去掉重试
	})
	if err != nil {
		// 【陷阱 2】StartJobCmd 失败时 jc == nil，所以 defer jc.Close() **必须**
		// 写在下面 —— 先判 err 提前 return，再注册 defer。顺序反了就是 nil
		// 解引用 j.mu，panic 而不是返错。
		// 与硬规则 J1 同源：关 hJob 会因 KILL_ON_JOB_CLOSE 连带杀树。
		// 这一分支没有任何进程在读 .bat，此刻删是安全的。
		removeBat()
		return Result{}, fmt.Errorf("run_script: 启动失败: %w", err)
	}
	defer jc.Close()

	// 【S3 + P3-30】限流输出 + **并发**排空两个管道。
	//
	// ⚠️ 必须并发读：匿名管道缓冲约 64KB，写满后子进程 WriteFile 阻塞 →
	// 永不退出 → jc.Wait() 永远等不到，整个 run_script 挂死。
	// 本项目已知有 14.7MB 输出的故障场景，远超任何管道缓冲。
	//
	// ⚠️ **但超时救不了这一段**（不是回归，只是别把话说满）：os/exec 时代
	// cmd.Run() 同样在等 copy goroutine，所以这个洞一直存在。超时只保证
	// jc.Wait() 会返回；下面 drainWG.Wait() 仍可能无限阻塞 —— 若 cmd.exe 的
	// 孙进程也以继承方式拿到管道写端（doCreateProcess 用
	// bInheritHandles=TRUE），读端就永远等不到 EOF。而 Win7 无嵌套 job 时
	// jc.Kill() 可能降级到只杀直接子进程，孙进程活下来 → 卡死。
	// 详见 win/jobexec.go 的 Kill 注释。
	cw := newCapWriter(maxToolOutputBytes)
	drainErrs := make(chan error, 2)
	var drainWG sync.WaitGroup
	drainWG.Add(2)
	// drain 把一根管道排空到 EOF。
	//
	// ⚠️ 三个不能省的点：
	// ① Take*Pipe 返 nil 必须当**错误**（win/jobexec.go：nil 意味着句柄管理
	//    有 bug，不能当成"这次没输出"）—— P3-20 之前就是恒 nil，而 `_, _ =`
	//    把它静默吞了，exec 的 stderr 全丢而测试还绿。
	// ② Take*Pipe 已把所有权**移交**调用方（Close 归属唯一，见
	//    JobCmd.Close 会跳过已被 Take 置 0 的字段），所以**我们负责 Close**。
	//    os.NewFile 只挂了 finalizer，不显式关就等 GC 回收 —— 每次执行泄一个
	//    句柄，PE 上反复跑几十次就够呛。
	// ③ 两个 goroutine 共写同一个 cw —— capWriter 内部有锁（P3-30 C1），
	//    且 io.Copy 的返回错误必须收进 drainErrs，不能 `_ , _ =` 吞掉（L5）。
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

	// Esc/Stop 或超时 → Kill 整棵树（不是只杀 cmd.exe）
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
	// ⚠️ 这一步**没有超时**：见上面"超时救不了这一段"的说明（孙进程持有继承
	// 写端时读端永不 EOF）。本轮只订正注释，不做行为变更。
	drainWG.Wait()
	// 容量 2 且 Wait 已过，两个结果必已就绪，不需要 select/drain。
	drainErr := firstErr(<-drainErrs, <-drainErrs)

	// 【陷阱 1】临时 .bat 的删除**必须在这里**，即 jc.Wait() 返回之后。
	//
	// 迁移前用 os/exec 时 defer os.Remove(path) 是安全的：cmd.Run() 等进程
	// 死透才返回，defer 在其后跑，.bat 必然是 cmd.exe 读完才删的。
	// 迁到 StartJobCmd 后这个保证没了 —— 超时/取消分支里 jc.Kill() 只是
	// **发出**终止就返回，jc.Wait() 才是等死透的那一步；而 cmd.exe 是**逐行**
	// 读 .bat 的。若把删除挂成函数顶部的 defer，或放在 Wait 之前的任何位置，
	// 就会在 cmd.exe 还没读完时把它删掉 —— 脚本尾部被静默截断，退出码还可能
	// 是 0，看起来完全像"脚本本来就这么短"。
	//
	// 正常路径与超时/取消路径都汇到这一行，所以一次调用覆盖全部。
	removeBat()

	// H-1：cmd.exe 输出是 OEM(GBK) 字节，直接 string(out) 会中文乱码。
	// 用 win.OEMToUTF8 转成 UTF-8（L1 返 (T,error)、L5 不吞错）。
	outStr := cw.String()
	decoded, decErr := win.OEMToUTF8([]byte(outStr))
	if decErr == nil {
		outStr = decoded
	}
	// 解码失败时保留原始字节串（decErr 会由 classifyRunOutcome 透传，L5）

	// 分类阶梯与 exec 共用（runneresult.go）。
	// ⚠️ 迁移前这里是 `if canceled, _ := classifyAbort(cctx); canceled`
	// —— classifyAbort 返回 (timedOut, canceled)，第一个返回值被当 canceled 用，
	// 于是**超时时对用户报 "canceled by user (Esc/Stop)"**，而用户根本没按；
	// 真 canceled 被 `_` 丢掉，下一行 cctx.Err()==DeadlineExceeded 成了死代码。
	timedOut, canceled := classifyAbort(cctx)
	return classifyRunOutcome("run_script", outStr, decErr,
		mergeDrainErr(synthRunErr(exitCode, waitErr, killErr), drainErr),
		timedOut, canceled, runScriptTimeoutSec)
}

func init() {
	Register(runScriptTool{})
}
