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
//	- 所有 API 返 (T, error)。
//	- **不吞错**：每层 err 透传到上一层。
//	- 日志按 you>/ai>/->/<-/!!/** 前缀（PLAN §8）→ logx 统一处理。
//	- worker 用 context 取消；UI 线程**不**直接调 agent.loop.Run（loop 会
//	  阻塞秒级 → 窗口冻结）。所有"调 LLM / 跑工具"都丢到 worker goroutine。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sync"
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

// boot 是 main 的实际实现；这样可以用 os.Exit 不影响 defer 链。
func boot() int {
	// [1] 命令行
	var (
		consoleFlag = flag.Bool("console", false, "create console window for stderr output")
		keyFlag     = flag.String("key", "", "API key (overrides smith.ini; not recommended, visible in tasklist)")
		noGUI       = flag.Bool("no-gui", false, "headless smoke test: run one user input then exit")
	)
	flag.Parse()

	// [2] 早期文件日志（GUI 起来前就有 trace）。
	logPath, err := setupEarlyLog()
	if err != nil {
		// 连日志都开不了：只能往 stderr 喊一嗓子
		fmt.Fprintf(os.Stderr, "FATAL: cannot open early log: %v\n", err)
		return 1
	}
	_ = logx.Info("** ver boot start log=%s", logPath)

	// [3] 加载 cfg
	exe, _ := os.Executable()
	exeDir := filepath.Dir(exe)
	iniPath := filepath.Join(exeDir, "smith.ini")
	cfgInstance, err := loadCfg(iniPath)
	if err != nil {
		// 加载失败 = 致命（ini 写了错格式）
		_ = logx.Error("!! cfg: %v", err)
		return 1
	}
	_ = logx.Info("** ver cfg loaded ini=%s", iniPath)

	// CLI --key 覆盖 ini
	if *keyFlag != "" {
		cfgInstance.LLM.Key = *keyFlag
		_ = logx.Warn("!! --key 覆盖了 ini；命令行明文，tasklist 可见")
	}

	// [4] 构造 LLM 客户端（缺字段 → nil，loop 时再报错）
	llmClient, err := buildLLM(cfgInstance)
	if err != nil {
		// 致命：LLM 客户端是核心
		_ = logx.Error("!! llm: %v", err)
		return 1
	}
	if llmClient == nil {
		_ = logx.Warn("!! llm: 缺配置（base/model/key），对话不可用；工具/单次 exec 仍可用")
	} else {
		_ = logx.Info("** ver llm ready base=%s model=%s", cfgInstance.LLM.Base, cfgInstance.LLM.Model)
	}

	// [5] worker 编排
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	userInputCh := make(chan string, 8)
	workerDone := make(chan struct{})
	go runWorker(ctx, llmClient, cfgInstance, userInputCh, workerDone)

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
		cancel()
		// 重新建立 ctx 给下一轮用
		ctx, cancel = context.WithCancel(context.Background())
		_ = ctx
		_ = cancel
	})

	// [7] --no-gui smoke 模式：发一条 → 取消 → 等 worker 退出
	if *noGUI {
		_ = logx.Info("** ver no-gui mode; sending one test input then exit")
		userInputCh <- "ver"
		// 给 worker 5s 跑完；超时也走 cancel 路径
		select {
		case <-workerDone:
		default:
			time.Sleep(5 * time.Second)
			cancel()
			<-workerDone
		}
		return 0
	}

	// [8] 进 GUI 消息循环
	_ = logx.Info("** ver starting gui loop")
	_ = *consoleFlag // --console 仅影响 link flag，不在 main 处理
	winCode := win.Run()
	_ = logx.Info("** ver gui returned code=%d", winCode)

	// [9] 通知 worker 退出
	cancel()
	<-workerDone
	return winCode
}

// runWorker 是单 goroutine 消费的 worker 循环。
// 一个时刻只跑一个 loop.Run（顺序处理用户输入）。
func runWorker(ctx context.Context, llm agent.Client, c *cfg.Config, in <-chan string, done chan<- struct{}) {
	defer close(done)
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
	for {
		var userInput string
		select {
		case <-ctx.Done():
			return
		case userInput = <-in:
		}
		// 没有 LLM 客户端：直接报"无法对话"
		if llm == nil {
			_ = logx.Error("!! LLM 未配置；请编辑 smith.ini 的 [llm] 段")
			continue
		}
		loop := agent.NewLoop(llm, toolCtx, maxTurns, systemPrompt)
		runCtx, runCancel := context.WithCancel(ctx)
		_, err := loop.Run(runCtx, userInput)
		runCancel()
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
