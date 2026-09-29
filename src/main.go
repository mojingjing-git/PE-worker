// PE-agent 主程序入口。
//
// 启动序列（PLAN §0.6 A6 + verifier 6.6 boot 契约）：
//
//	[1] 命令行参数（--console / --key / --no-gui）
//	[2] 早期文件日志（GUI 起来之前就能写）
//	[3] 加载 smith.ini（缺失走默认；缺 key 不致命，下一步报清晰错）
//	[4] 构造 LLM 客户端（cfg 缺字段 → 返 nil 客户端，loop 调用时再报错）
//	[5] 绑 GUI 钩子：OnSend → worker，OnStop → cancel
//	[6] 启动 worker goroutine（消费 userInputCh）
//	[7] 调 win.Run()（LockOSThread + 消息循环，**不返回**直到窗口关闭）
//	[8] 窗口关闭后：cancel worker，等它退出，return 0
//
// 退出码：
//
//	0  正常退出（用户关闭窗口）
//	1  启动错误（log 文件都开不了 / 致命配置错误）
//	2  GUI 错误（RegisterClassEx 失败 / CreateWindowEx 失败）
//	3  worker 异常退出
//
// 设计原则（v1 L1+L4+L5 + PLAN §0.9）：
//
//   - 所有 API 返 (T, error)。
//   - **不吞错**：每层 err 透传到上一层。
//   - 日志按 you>/ai>/->/<-/!!/** 前缀（PLAN §8）→ logx 统一处理。
//   - worker 用 context 取消；UI 线程**不**直接调 agent.loop.Run（loop 会
//     阻塞秒级 → 窗口冻结）。所有"调 LLM / 跑工具"都丢到 worker goroutine。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"peagent/src/agent"
	"peagent/src/cfg"
	"peagent/src/logx"
	"peagent/src/tools"
	"peagent/src/win"
)

func main() {
	os.Exit(boot())
}

// fatalExit 是**所有非零退出码路径的统一收口**（T1-2）。
//
// 存在的理由：产物是 -H windowsgui（**没有控制台**），PE 里双击运行时
// 用户看到的就是"闪一下就没了"。每一个提前 return 1 都是一次静默消失。
//
// 顺序很重要：**日志永远先写**（万一弹窗失败，日志还在），
// 然后弹 MessageBoxW（PE 里唯一可靠的可读通道）。
//
// ⚠️ **无头模式不弹窗**：`--no-gui` 跑的是 smoke_bin_test，它执行的正是
// `smith.exe --no-gui`。MessageBox 是**模态阻塞**调用，没人点 OK 就会
// 永挂 → smoke 测试 15s 超时失败。所以 noGUIMode 时只写日志 + stderr。
//
// ⚠️ **只收非零退出码**。用户主动取消 key 对话框是**正常退出**（return 0），
// 那种路径弹「启动失败」是错误 UX —— 只打 log 静默退出即可。
func fatalExit(code int, format string, args ...any) int {
	_ = logx.Error("!! "+format, args...)

	if noGUIMode {
		fmt.Fprintf(os.Stderr, "FATAL(code=%d): %s\n", code, fmt.Sprintf(format, args...))
		return code
	}
	win.FatalBox(0, buildFatalMessage(code, fmt.Sprintf(format, args...)))
	return code
}

// buildFatalMessage 拼弹窗文本。抽成纯函数是为了**能在无头 CI 里测** ——
// MessageBox 本身模态阻塞测不了，但它拼出来的字符串可以测。
func buildFatalMessage(code int, detail string) string {
	var b strings.Builder
	b.WriteString("smith 启动失败（退出码 ")
	b.WriteString(strconv.Itoa(code))
	b.WriteString("）\n\n")
	b.WriteString(detail)
	if fatalLogPath != "" {
		b.WriteString("\n\n详细日志：\n")
		b.WriteString(fatalLogPath)
	}
	b.WriteString("\n\n（PE 现场无法复现，请把上面这个日志文件带回来）")
	return b.String()
}

// boot 是 main 的实际实现；这样可以用 os.Exit 不影响 defer 链。
// noGUIMode 记录是否无头模式（--no-gui）。fatalExit 与 runWorker 的 recover 据此决定
// **要不要弹窗**（MessageBox 是模态阻塞调用，无人点 OK 会永挂 → smoke 测试必失败）。
var noGUIMode bool

// workerPanicked 被 runWorker 的 recover 置 1，表示 agent worker 崩了。
// **必须用原子变量而不是 channel**：GUI 模式下 boot 阻塞在 win.Run() 的消息循环里，
// 根本不在 select 中，任何 channel 都不会有人读。
var workerPanicked int32

// fatalLogPath 供 fatalExit 拼消息时附上日志位置（PE 里用户照着它去 U 盘找）。
var fatalLogPath string

func boot() int {
	// [1] 命令行
	//
	// ⚠️ T1-4：`--console` 已删除。理由（docs/12 §七 Q1）：
	// 产物用 -H windowsgui 链接，**没有控制台**，os.Stderr 全部丢弃；而
	// AttachConsole(ATTACH_PARENT_PROCESS) 只在父进程有控制台时才成功 ——
	// U 盘双击场景它必然失败，cmd 启动场景 MessageBoxW 也一样够用。
	// 留一个"承诺弹控制台但什么也不做"的假开关比没有更坏：用户以为有保护，
	// 实际没有。PE 里唯一可靠的可读输出通道现在是 MessageBoxW（见 fatalExit）。
	var (
		keyFlag = flag.String("key", "", "API key (overrides smith.ini; not recommended, visible in tasklist)")
		noGUI   = flag.Bool("no-gui", false, "headless smoke test: run one user input then exit")
	)
	flag.Parse()
	noGUIMode = *noGUI

	// [2] 早期文件日志（GUI 起来前就有 trace）。
	logPath, err := setupEarlyLog()
	if err != nil {
		// 连日志都开不了：这是最早的一次失败，stderr 大概率没人看，
		// 弹窗是用户唯一能看到的东西
		fmt.Fprintf(os.Stderr, "FATAL: cannot open early log: %v\n", err)
		win.FatalBox(0, fmt.Sprintf("无法打开日志文件：%v\n\n"+
			"请确认 U 盘可写，或把 smith.exe 放到可写目录再运行。", err))
		return 1
	}
	fatalLogPath = logPath
	_ = logx.Info("** ver boot start log=%s", logPath)

	// [3] 加载 cfg
	exe, _ := os.Executable()
	exeDir := filepath.Dir(exe)
	iniPath := filepath.Join(exeDir, "smith.ini")
	cfgInstance, err := loadCfg(iniPath)
	if err != nil {
		// 加载失败 = 致命（ini 写了错格式）→ 弹窗（T1-2）
		return fatalExit(1, "smith.ini 格式错误：%v\n\n请检查 %s", err, iniPath)
	}
	_ = logx.Info("** ver cfg loaded ini=%s", iniPath)

	// CLI --key 覆盖 ini
	if *keyFlag != "" {
		cfgInstance.LLM.Key = *keyFlag
		_ = logx.Warn("!! --key 覆盖了 ini；命令行明文，tasklist 可见")
	}

	// [3.5] 首次运行 / 缺 key → 弹输入对话框（仅 GUI 模式）
	// --no-gui / --key / ini 已有 key 都不弹
	// 用户勾选"保存" → 写 smith.ini（base/model/provider）+ smith.key
	// 用户不勾选 → 全部仅在内存里，进程退出就丢（U 盘发给别人用就这模式）
	//
	// 走 keyfile 模式（KeyFile 配 + 文件可读）也不弹窗——让"安全模式"用户
	// 配好后每次启动直接用。C-3 修复：避免 keyfile 用户每次都被弹窗骚扰。
	if !*noGUI && !hasUsableKey(cfgInstance) && *keyFlag == "" {
		_ = logx.Info("** ver calling PromptAPIKey")
		key, prov, baseURL, model, save, ok, err := win.PromptAPIKey(
			cfgInstance.LLM.Key,
			cfgInstance.LLM.Provider,
			cfgInstance.LLM.Base,
			cfgInstance.LLM.Model,
		)
		if err != nil {
			return fatalExit(1, "API Key 输入框出错：%v", err)
		}
		_ = logx.Info("** ver PromptAPIKey returned ok=%v save=%v keyLen=%d", ok, save, len(key))
		if !ok {
			_ = logx.Warn("!! key dialog cancelled; 退出")
			return 0
		}
		if key == "" {
			_ = logx.Warn("!! key dialog OK but empty; 退出")
			return 0
		}
		// 写回内存 cfg
		cfgInstance.LLM.Key = key
		if prov != "" {
			cfgInstance.LLM.Provider = prov
		}
		if baseURL != "" {
			cfgInstance.LLM.Base = baseURL
		}
		if model != "" {
			cfgInstance.LLM.Model = model
		}
		if save {
			_ = logx.Info("** ver key dialog OK'd, save=true")
			keyPath := filepath.Join(exeDir, "smith.key")
			if err := os.WriteFile(keyPath, []byte(key+"\n"), 0600); err != nil {
				return fatalExit(1, "写 %s 失败：%v\n\nU 盘可能是只读的；不勾选「保存到磁盘」则只用当次。", iniPath, err)
			}
			_ = logx.Info("** ver ini updated %s (provider=%s, model=%s, base=%s)",
				iniPath, cfgInstance.LLM.Provider, cfgInstance.LLM.Model, cfgInstance.LLM.Base)
		} else {
			_ = logx.Info("** ver LLM config 仅当次有效（不落盘）")
		}
	}
	_ = logx.Info("** ver post-dialog checkpoint; about to build LLM")

	// [4] 构造 LLM 客户端（缺字段 → nil，loop 时再报错）
	llmClient, err := buildLLM(cfgInstance)
	if err != nil {
		// 致命：LLM 客户端是核心 → 弹窗（T1-2）
		return fatalExit(1, "LLM 客户端初始化失败：%v\n\n请检查 smith.ini 的 [llm] 段（base / model / key）。", err)
	}
	if llmClient == nil {
		_ = logx.Warn("!! llm: 缺配置（base/model/key），对话不可用；工具/单次 exec 仍可用")
	} else {
		_ = logx.Info("** ver llm ready base=%s model=%s", cfgInstance.LLM.Base, cfgInstance.LLM.Model)
	}

	// [5] worker 编排
	//
	// 设计：worker 是常驻 goroutine，**不**响应外层 ctx 退出。
	// Esc/Stop 只取消"当前一轮"的 runCtx（loop.Run），worker 继续等下条 user input。
	// 进程退出靠主函数 return（或 GUI 关闭后 os.Exit），worker 自然被 GC 回收。
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	userInputCh := make(chan string, 8)
	stopRunCh := make(chan struct{}, 1) // 缓冲 1：UI 线程连按 Stop 不丢信号
	go runWorker(ctx, llmClient, cfgInstance, userInputCh, stopRunCh)

	// [6] GUI 钩子
	win.SetOnSend(func(text string) {
		// 过滤空（Enter 也能触发）
		if text == "" {
			return
		}
		// 异步投递到 worker（不要在 UI 线程阻塞）
		select {
		case userInputCh <- text:
		default:
			_ = logx.Warn("!! user input buffer full, dropping: %s", text)
		}
	})
	win.SetOnStop(func() {
		_ = logx.Warn("!! user abort (Esc/Stop)")
		// 通知 worker 取消**当前 runCtx**（loop.Run 用的）。不取消外层 ctx，
		// worker 仍然存活，下条 user input 进来还能继续。
		select {
		case stopRunCh <- struct{}{}:
		default:
			// 已有一个 stop 信号在 queue 里，OK
		}
	})
	// 主窗口就绪后把 hwnd 绑给 logx，否则日志走文件兜底，GUI 看不到任何输出
	win.SetOnMainWindowCreated(func(hwnd uintptr) {
		logx.SetHWND(hwnd)
		_ = logx.Info("** ver logx bound hwnd=%d; 日志开始投递到 GUI", hwnd)
	})

	// [7] --no-gui smoke 模式：发一条 → 等 worker 跑完 → 退出
	if *noGUI {
		_ = logx.Info("** ver no-gui mode; sending one test input then exit")
		userInputCh <- "ver"
		// 给 worker 最多 5s 跑完（agent loop 内部会自己完成；超时也无所谓，进程直接退）
		time.Sleep(5 * time.Second)
		_ = logx.Info("** ver no-gui smoke done; exit 0")
		return 0
	}

	// [8] 进 GUI 消息循环
	_ = logx.Info("** ver starting gui loop")
	winCode := win.Run()
	_ = logx.Info("** ver gui returned code=%d", winCode)

	// 【T1-3】worker 崩了 → 退出码 3。
	//
	// ⚠️ 这里**必须用原子变量而不是 channel**（复审指出的设计错误）：
	// worker panic 发生在 GUI 模式下时，boot 此刻**阻塞在 win.Run() 的消息
	// 循环里，根本不在 select 中** —— 任何 channel 都不会有人读。
	// 让 win.Run() 提前返回的唯一途径是 PostQuitMessage（见 runWorker 的
	// recover），退出码只能靠共享内存传回来。
	if atomic.LoadInt32(&workerPanicked) != 0 {
		cancel()
		return 3
	}

	// [9] 通知 worker 退出（外层 ctx cancel → runWorker 主 select 命中 ctx.Done 退出）
	cancel()
	return winCode
}

// runWorker 是单 goroutine 消费的 worker 循环。
// 一个时刻只跑一个 loop.Run（顺序处理用户输入）。
//
// **永不退出**：外层 ctx 仅用于整体进程退出时强制中断（GUI 关闭 → cancel()）。
// 单轮中断走 stopRunCh：OnStop 往里塞信号，loop.Run 拿到 ctx.Done() 自动返回。
// 这样保证：按一次 Esc/Stop 只杀当前一轮，**worker 仍存活**，下条 user input
// 进来还能继续。避免 C-2 描述的"Stop 后 worker 永久死亡"问题。
func runWorker(ctx context.Context, llm agent.Client, c *cfg.Config, in <-chan string, stopRun <-chan struct{}) {
	// 【T1-3】worker 是裸 goroutine，之前**没有 recover** —— tools/agent 任一层
	// panic 会直接崩掉整个进程（Windows 上 exit code 2），且崩之前没有任何日志
	// 说明是哪一步炸的。退出码约定里的 "3 = worker 异常" 因此永远不可达。
	//
	// 三步缺一不可：
	//  1. recover 住 panic
	//  2. **PostQuitMessage 唤醒消息循环** —— 这是让 win.Run() 返回的唯一途径
	//     （boot 阻塞在消息循环里，不在任何 select 中，channel 传不出去）
	//  3. atomic 标记退出码，由 boot 在 win.Run() 返回后读
	defer func() {
		if r := recover(); r != nil {
			_ = logx.Error("!! worker panic: %v\n%s", r, debug.Stack())
			atomic.StoreInt32(&workerPanicked, 1)
			if !noGUIMode {
				win.FatalBox(0, fmt.Sprintf(
					"agent worker 异常终止：\n%v\n\n这是 smith 自身的 bug，请把日志带回。", r))
			}
			// ⚠️ **必须用 PostMessage(hwnd, WM_QUIT) 而不是 PostQuitMessage**：
			// 后者只投给**调用线程**的消息队列，而 worker 跑在另一个 goroutine /
			// 另一个 OS 线程上 —— 投过去 UI 线程根本收不到，boot 会一直挂在
			// win.Run() 里。（docs/12 §二 T1-3 复审指出的正是这类设计错误）
			if h := win.MainHwnd(); h != 0 {
				_ = logx.Warn("!! posting WM_QUIT to hwnd=%d to wake the gui loop", h)
				_, _ = win.PostMessageW(h, win.WM_QUIT, 0, 0)
			}
		}
	}()

	systemPrompt := agent.SystemPrompt(true)
	maxTurns := c.Agent.MaxTurns
	if maxTurns <= 0 {
		maxTurns = 10
	}
	toolCtx := &tools.Context{
		Confirm: makeConfirm(c.Agent.Confirm),
		Cwd:     exeDir(),
		Config: &tools.Config{
			Whitelist: c.Agent.Whitelist,
			Confirm:   c.Agent.Confirm,
		},
	}
	// 【S2-1】Loop 提到 for 循环**外**。
	//
	// 原来 `loop := agent.NewLoop(...)` 在循环体内（而 systemPrompt / toolCtx
	// 在循环外 —— 典型的漏提），于是每条用户输入都拿到全新 history：
	// NewLoop 会把 history 重置成 [system]。后果：
	//   - agent/history.go 整套（40 条滑窗 / 32KB 截断 / ClipImages）
	//     在生产路径上**永远不可达**
	//   - 用户连续问「看看 C 盘还剩多少」→「那 D 盘呢」，第二句模型完全没有
	//     上下文。对一个应急助手，多轮对话是存在的理由，不是加分项。
	//
	// 提到循环外后，history 跨轮累积；换话题时调 loop.Reset()（暂未接 /clear）。
	//
	// ⚠️ 必须与 history.go 的两个 400 洞修复同批：单独提上来会让第一次长会话
	// 就撞上"tool 消息的父 assistant 被切掉 → 本会话之后每次请求都 400"。
	loop := agent.NewLoop(llm, toolCtx, maxTurns, systemPrompt)
	_ = logx.Info("** ver agent loop ready (maxturns=%d, tools=%d)", maxTurns, len(loop.ToolNames()))

	for {
		// 取 user input。优先响应外层 ctx（整体进程退出），但**不**靠它处理单轮中断
		var userInput string
		select {
		case <-ctx.Done():
			return // 整体进程退出（GUI 关闭后 defer cancel 触发）
		case userInput = <-in:
		}

		// 【关键】排空上一轮遗留的 stop 信号。
		//
		// stopRunCh 是**跨轮复用**的全局信号槽（缓冲 1），而 worker 空闲时阻塞在
		// 上面的 select{ctx.Done | in}，**根本不监听 stopRunCh**。所以"空闲期按下的
		// Stop"会变成一颗留在 channel 里的定时炸弹：下一轮 monitor goroutine 一启动
		// 就把它取走并立刻 runCancel()，用户这条真实提问被 0ms 静默丢弃。
		//
		// 复现（审计实测）：
		//   T0 worker 空闲阻塞于 select
		//   T1 用户按 Esc → stopRunCh <- {} （buffer 0→1，无人监听，token 永久滞留）
		//   T1 用户输入 "重启服务" → userInputCh（输入框已被 clearInput 清空）
		//   T2 worker 唤醒 → go monitor → monitor 立即取到残留 token → runCancel()
		//   T2 loop.Run → 0ms 返回 context canceled → GUI 只打一行 "!! loop: context canceled"
		// PE 现场表现 = "我明明发了指令，smith 装死"。
		select {
		case <-stopRun:
			_ = logx.Warn("!! 丢弃上一轮残留的 stop 信号（用户本轮请求继续）")
		default:
		}

		// 没有 LLM 客户端：直接报"无法对话"
		if llm == nil {
			_ = logx.Error("!! LLM 未配置；请编辑 smith.ini 的 [llm] 段")
			continue
		}
		runCtx, runCancel := context.WithCancel(ctx)
		// 单轮中断：把 stopRunCh 转成 ctx.Done 信号接到 runCtx 上
		// （loop.Run 只看 ctx.Done()，不会直接读 stopRunCh）
		stopDone := make(chan struct{})
		go func() {
			select {
			case <-stopRun:
				runCancel()
			case <-stopDone:
				// 正常完成，stop 监控 goroutine 退出
			}
		}()
		_, err := loop.Run(runCtx, userInput)
		runCancel()
		close(stopDone) // 释放 stop 监控 goroutine
		if err != nil {
			_ = logx.Error("!! loop: %v", err)
		}
	}
}

// makeConfirm 构造工具用的 confirm 回调。
// PE 模式 (cfg.Agent.Confirm=true) → 通过 logx 弹"y/n"，目前简化为：默认 true。
// cfg.Agent.Confirm=false → 一律 true（白名单+confirm 都关；纯 CLI 模式）。
func makeConfirm(needConfirm bool) func(string) bool {
	if !needConfirm {
		return func(string) bool { return true }
	}
	// 简化：默认同意。Phase 2 后期再换成真弹窗（要加 WM_CONFIRM 消息 + 状态栏交互）。
	return func(prompt string) bool {
		_ = logx.Warn("!! confirm auto-yes: %s", prompt)
		return true
	}
}

// exeDir 拿当前 exe 所在目录（缓存）。
var (
	exeDirOnce  sync.Once
	exeDirValue string
)

func exeDir() string {
	exeDirOnce.Do(func() {
		exe, _ := os.Executable()
		exeDirValue = filepath.Dir(exe)
	})
	return exeDirValue
}

// hasUsableKey 判 cfg 是否已经有可用的 key：
//   - c.LLM.Key != ""           → 直接有
//   - c.LLM.KeyFile != "" 且文件可读 → 走 keyfile 模式
//
// 不可读时返 false，buildLLM 阶段会报"read keyfile ...: no such file" 错。
func hasUsableKey(c *cfg.Config) bool {
	if c.LLM.Key != "" {
		return true
	}
	if c.LLM.KeyFile == "" {
		return false
	}
	path := c.LLM.KeyFile
	if !filepath.IsAbs(path) {
		path = filepath.Join(exeDir(), path)
	}
	_, err := os.Stat(path)
	return err == nil
}

// loadCfg 加载 ini，缺文件走默认（不致命）。
func loadCfg(path string) (*cfg.Config, error) {
	c, err := cfg.Load(path)
	if err != nil {
		// ErrFileNotFound 不致命 → 用默认（cfg.Load 内部用 %w wrap，errors.Is 走得通）
		if errors.Is(err, cfg.ErrFileNotFound) {
			return cfg.Default(), nil
		}
		return nil, err
	}
	return c, nil
}

// buildLLM 根据 cfg 构造 agent.Client。
// cfg 缺关键字段 → 返 (nil, nil) 而不是 err；上层据此区分"配错"和"完全没配"。
func buildLLM(c *cfg.Config) (agent.Client, error) {
	if c.LLM.Base == "" || c.LLM.Model == "" {
		return nil, nil
	}
	key := c.LLM.Key
	if key == "" && c.LLM.KeyFile != "" {
		// keyfile 路径：相对 exeDir
		path := c.LLM.KeyFile
		if !filepath.IsAbs(path) {
			path = filepath.Join(exeDir(), path)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read keyfile %s: %w", path, err)
		}
		// 去掉尾随换行
		key = string(b)
		for len(key) > 0 && (key[len(key)-1] == '\n' || key[len(key)-1] == '\r' || key[len(key)-1] == ' ') {
			key = key[:len(key)-1]
		}
	}
	if key == "" {
		return nil, nil
	}
	provider := agent.ProviderOpenAI
	switch c.LLM.Provider {
	case "anthropic":
		provider = agent.ProviderAnthropic
	case "deepseek":
		provider = agent.ProviderDeepSeek
	}
	return agent.NewClient(agent.Config{
		Provider:  provider,
		BaseURL:   c.LLM.Base,
		Model:     c.LLM.Model,
		APIKey:    key,
		TimeoutS:  c.LLM.Timeout,
		MaxTokens: 2048,
	})
}

// setupEarlyLog 启动早期文件日志，路径优先级：exeDir/smith.log → X:\tmp\smith.log → 失败。
//
// 早期日志在 GUI 起来前就有：用户能看见 GUI 起来前的崩溃（这是 A6 的核心）。
// logx 拿到 hwnd 后会自动切到 PostMessage 投递。
func setupEarlyLog() (string, error) {
	candidates := []string{}
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), "smith.log"))
	}
	candidates = append(candidates, `X:\tmp\smith.log`, `C:\tmp\smith.log`)
	for _, p := range candidates {
		if err := tryOpenLog(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("no writable log path tried %d candidates", len(candidates))
}

func tryOpenLog(p string) error {
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	// 不关：logx 持有 writer 引用，进程退出时由 OS 关。
	// 但这里**没有** defer Close，**全进程只此一处**未关闭的 fd，是
	// 早期文件日志必须的（GUI 没起来时只能走这条 sink）。
	logx.SetOutput(f)
	return nil
}
