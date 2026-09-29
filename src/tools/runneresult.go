// tools/runneresult.go —— exec / run_script 共用的"结果分类阶梯"。
//
// 【为什么要抽出来】
//
// 迁移前两条执行路径各写一份阶梯，且**判超时的机制还不一样**：
//
//	exec.go       classifyAbort(cctx) → (timedOut, canceled) 两个值都用上
//	run_script.go `if canceled, _ := classifyAbort(cctx); canceled`
//	              ↑ 第一个返回值是 timedOut，被当成 canceled 用；
//	                真 canceled 被 `_` 丢掉，cctx.Err()==DeadlineExceeded
//	                那一支因而被上面的分支抢走，成了死代码
//
// 结果是 run_script 超时时对用户报 "canceled by user (Esc/Stop)" ——
// 用户看到的是"你按了停止"，而他根本没按。两条平行实现也正是 A1 那种
// 句柄 bug 只在一处发生的结构性原因。
//
// 现在阶梯只有这一份，工具名与超时秒数是参数。
package tools

import (
	"errors"
	"fmt"
)

// errPipeNotTaken 表示 JobCmd.Take*Pipe 返了 nil。
//
// win/jobexec.go 的说明写得很直白：nil 意味着**句柄管理有 bug**，不能当成
// "这次命令没输出"。P3-20 之前 stderr 读端被误关，TakeStderrPipe() 恒返 nil，
// 而调用方 `_, _ = io.Copy(cw, nil)` 把 error 丢弃 —— exec 的 stderr 输出
// 全丢而测试还绿。这个 sentinel 就是为了让"句柄管理出 bug"重新可见。
var errPipeNotTaken = errors.New("管道读端取不到（句柄管理有 bug，不是本次没输出）")

// mergeDrainErr 把"排空输出管道失败"并进 runErr。
//
// 为什么必须并：管道读不到 / 关闭失败 = 句柄管理或读取出了问题。若让它静默
// 走掉，用户看到的是"命令没输出"，模型看到的是空结果 —— 一个 bug 伪装成
// 正常的空输出，排查时完全看不出来（P3-20 正是这样潜伏下来的）。
//
// 命令本身已经失败时保留原 err 为前缀，把管道错用 %w 挂在后面：
// 不掩盖主因，但 L5 要求它仍然进错误链 —— **本函数的返回值上**
// errors.Is(err, errPipeNotTaken) 为真（output_limit_test.go 有断言）。
//
// ⚠️ 这条链要一路活到工具最终返回的 err 上，哨兵才算真的"可见"。
// 中间有过一次断裂（docs/13 复审 F3）：classifyRunOutcome 的 runErr 分支
// 曾用 %v 把本函数的返回值拍平，`%v` 不建立 unwrap 链，于是
// errors.Is 对工具交给模型的 err 是 false，errPipeNotTaken 白设。
// 现已改回 %w（该函数 ③ runErr 分支的两个 fmt.Errorf）。**仍然断链的是
// canceled / timedOut 两个分支** —— 它们整个丢弃 runErr，本函数的结果连同
// 哨兵一起消失，已知缺口记录在 classifyRunOutcome 的注释里。
func mergeDrainErr(runErr, drainErr error) error {
	if drainErr == nil {
		return runErr
	}
	if runErr == nil {
		return drainErr
	}
	return fmt.Errorf("%v（排空输出管道另有失败: %w）", runErr, drainErr)
}

// firstErr 返回首个非 nil 错误，全 nil 时返 nil。
// 用于合并 stdout / stderr 两根管道各自的排空结果。
func firstErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

// classifyRunOutcome 把"输出 + 三类 err + 两种中止原因"归一成 (Result, error)。
//
// 判定顺序是**有讲究的**，别调换：
//
//	① canceled && !timedOut → "用户按了 Esc/Stop"
//	② timedOut              → "超时"
//	③ runErr                → 进程/退出码层面出错（部分输出照样回灌给模型）
//	④ decErr                → 输出解码失败
//	⑤ 全空                  → 成功
//
// ① 必须排在 ② 前面且带 `&& !timedOut`：canceled 与 timedOut 对用户是
// 完全不同的反馈（"你按了停止" vs "这命令太慢"），混为一谈会误导。
// ① ② 又必须排在 ③ 前面：Esc/Stop 触发 kill 后子进程必然留下非零退出码，
// 若先判 runErr 就会把"取消"降级成一句裸露的 "exit status 1"。
//
// 参数：
//
//	toolName    消息前缀（"exec" / "run_script"）
//	outStr      已解码的输出文本（限流后）
//	decErr      win.OEMToUTF8 的错误，nil 表示解码成功
//	runErr      synthRunErr 合成好的运行错误，nil 表示退出码 0
//	timedOut    ctx 超时
//	canceled    ctx 被用户取消（Esc/Stop）
//	timeoutSec  工具级超时秒数，只用于拼消息
//
// ⚠️ **已知缺口（有意保留，别当成已处理）**：① canceled 与 ② timedOut
// 两个分支**整个丢弃 runErr**，因此并进 runErr 的管道哨兵 errPipeNotTaken
// 在中止路径上完全不可见 —— 用户按了 Esc/Stop 或命令超时时，若句柄管理
// 另有 bug，错误里只剩一句 "canceled by user" / "timeout after Ns"。
// 补它属行为变更（会让"取消"的消息里多出 kill 后的退出码噪音，正是本函数
// 上面那段排序说明要避免的），留待单独一批处理。
// ③ runErr 分支已用 %w 保住错误链，所以非中止路径上哨兵可 errors.Is 追到
// （runneresult_test.go 有断言钉住）。**只有中止路径是例外。**
func classifyRunOutcome(toolName string, outStr string, decErr, runErr error,
	timedOut, canceled bool, timeoutSec int) (Result, error) {

	if canceled && !timedOut {
		return Result{Text: outStr}, fmt.Errorf("%s: canceled by user (Esc/Stop)", toolName)
	}
	if timedOut {
		if decErr != nil {
			return Result{Text: outStr},
				fmt.Errorf("%s: timeout after %ds (decode output: %w)", toolName, timeoutSec, decErr)
		}
		// 输出包一层提示再交给模型，让它知道这是"被截断的部分"而不是全量
		return Result{Text: fmt.Sprintf("[%s timeout %ds] partial: %s", toolName, timeoutSec, outStr)},
			fmt.Errorf("%s: timeout after %ds", toolName, timeoutSec)
	}
	if runErr != nil {
		// exit code != 0 也算 err，但把 output 也带回去。
		// 【S3-audit】这里返回的 Result.Text 是**有效输出** —— loop 侧会把它
		// 作为 "partial output" 一起回灌给模型（原来被整个丢掉，导致
		// `echo hello & exit /b 1` 这种场景模型只看到 "exit status 1"
		// 然后反复重试同一条命令直到 MaxTurns）。
		if decErr != nil {
			return Result{Text: outStr}, fmt.Errorf("%s: %w (decode output: %w)", toolName, runErr, decErr)
		}
		// %w 而不是 %v：%v 只是把 runErr 拍成字符串，错误链在这里断掉，
		// mergeDrainErr 挂上去的 errPipeNotTaken 就再也 errors.Is 不到了
		// （docs/13 复审 F3 修的就是这一处）。两个 %w 都合法（Go 1.20 起
		// fmt.Errorf 支持多个 %w），errors.Is 会同时查这两条链。
		//
		// ⚠️ "改成 %w 不会改变错误消息"这个前提**只在操作数的动态类型是
		// error、且不实现 fmt.Formatter 时成立** —— 真配上一个 Formatter，
		// %w 会以 verb='w'、%v 以 verb='v' 去调 Format，输出可能分叉，
		// 而 runneresult_test.go 的断言是精确字符串比对。
		// 本仓库当前**零个** Format(*fmt.State) 实现（2026-09 复审 grep 过
		// 全部 src/），所以成立 —— 但这是**当前事实、不是语言保证**。
		// 将来若新增实现了 Formatter 的错误类型，先回来核对这里的字符串。
		return Result{Text: outStr}, fmt.Errorf("%s: %w", toolName, runErr)
	}
	if decErr != nil {
		return Result{Text: outStr}, fmt.Errorf("%s: decode output: %w", toolName, decErr)
	}
	return Result{Text: outStr}, nil
}

// synthRunErr 把 Job Object 执行的三元组（退出码 / Wait 错 / Kill 错）
// 合成单一的 runErr，交给 classifyRunOutcome。
//
// 优先级：waitErr > 非零退出码 > killErr。
// killErr 排最后是因为它**经常是噪音**：进程已经自己跑完（退出码 0）时
// ctx 刚好超时，watcher 仍会调 Kill，而对已退出进程杀第二次可能报错。
// 那种情况下命令其实成功了，不该让 kill 的错盖掉成功。
func synthRunErr(exitCode uint32, waitErr, killErr error) error {
	runErr := waitErr
	if runErr == nil && exitCode != 0 {
		runErr = fmt.Errorf("exit status %d", exitCode)
	}
	if killErr != nil && runErr == nil {
		runErr = killErr
	}
	return runErr
}
