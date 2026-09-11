# Changelog

PE-agent (smith / 铁匠) 项目的变更日志。

格式参考 [Keep a Changelog](https://keepachangelog.com/)，按版本倒序排列（最新在上）。

---

## [Unreleased]

### 下一批
- **Batch 1**（LLM 适配层 9 条 ~80 行）：H-1 OEM→UTF8 + M-9 ErrMaxTurns + L-5 空响应 + H-2 OpenAI image wire + H-3 重试空体 + H-4 CheckRedirect + M-6 attach_image + M-7 think 剥离 + M-8 空 tool 占位
- **Batch 2**（Win 互操作 + Job 接入 ~150 行）：H-6 wstrKeep 加锁 + L-1 泄漏 + H-8 IsProcessInJob 签名 + H-7 Job 杀树接入 exec/run_script（大改 + spike 回归）
- **Batch 3**（GUI 交互 5 条 ~40 行 / **同 PR atomic**）：M-2/M-3/M-4/M-5 + H-5
- **Batch 4**（杂项 / 安全 / 健壮 ~120 行）：M-1 + M-10/M-11 + M-12/M-13/M-14 + L-2 kill 工具 + L-3/L-4 + L-7/L-8

详见 `.workbuddy/audit/2026-09-11-P1-audit.md` §五整改版 + `2026-09-11-P1-verify.md` 7 项审核。

---

## [P2-4] - 2026-09-11 (commit 0104246)

### Fixed
- **排版乱（双重 `[I]` 标签）**：`logx/log.go:81` 改 `line := msg + "\r\n"`，不再加 `[D]/[I]/[W]/[E]` 字符串前缀；现在 `appendLog` 唯一负责拼 `[HH:MM:SS] [I] `。
- **行挤一起**：`appendLog` 末尾防御性补 `\r\n`（已 `\r\n` 不动）。agent 内部 raw 字符串透传可能漏换行。
- **think 字号不明显**：`newCharFormatSize(100)` 改 5pt（差 4pt，比 7pt 差 2pt 视觉差异更大）。

### Changed
- `logx/log_test.go` 同步更新：单测期望从 `"[I] hello world"` 改为 `"hello world"`（logx 不再加 level 字符串）。

### Verified
- 截屏实测：每行独立不挤，单层前缀，think 段显示正常。
- 6 包测试全过（agent/cfg/logx/test/tools/win）。

---

## [P2-3] - 2026-09-11 (commit 716984b)

### Security
- **`.gitignore` 补 3 路径 `smith.session.log` 排除**：GUI Save 按钮产物可能含真实 LLM 对话（用户隐私）。`dist/smith.session.log` / `.tmp/smith.session.log` / `smith.session.log` 全部入黑名单。

### Fixed
- `src/win/api_user_gdi.go` 补 `pGetLocalTime` 1 行声明（P2-1 加时间戳功能时漏 commit，build 居然过是因为 `kernel32` 已在 `api_kernel.go` 声明）。**无功能变化**。

---

## [P2-2] - 2026-09-11 (commit 9031479)

### Added
- **think 块单独缩字号**：logx 投递 line 时，appendLog 扫描 `<think>...</think>` 段（不嵌套），对每段 `EM_SETCHARFORMAT(SCF_SELECTION, &CHARFORMATW)` 把 yHeight 改 5pt 让 think 段视觉上"小一号"，便于和正常 reply 区分。
- `msgs.go` 加常量：`EM_SETCHARFORMAT=0x0444` + `SCF_SELECTION=0x0001` + `CFM_SIZE=0x80000000`。
- `gui_test.go`：8 个 think block 子测试（no/single/multi/unclosed/empty/nested-ish）+ CHARFORMAT byte buffer 测试（cbSize/dwMask/yHeight/yOffset 字段）。

### Fixed (verifier 审核发现 3 个 blocker)
- **Blk-2**（pre-existing NUL bug）：`combined = append(prefixBuf...)` 把 NUL 嵌进 combined，REPLACESEL（NUL-terminated）只插 prefix，lineBuf 永远进不去 EDIT → 改 `prefixBuf[:len-1]` 剥 NUL。
- **Blk-1**：与 Blk-2 配套，`styleThinkBlocks(lineBuf, len(prefixBuf)-1)`。
- **Blk-3**：`cfSize=60` 实际应是 **92**（CHARFORMATW），Wine `dlls/user32/edit.c` 严格比较 `cbSize == sizeof(CHARFORMATW) == 92`，60 拒收 → 改 92。

### Verified
- 6 包测试全过 + 9 个 think block 子测试 PASS。
- 独立 verifier 审核 6 维度全过。

---

## [P2-1] - 2026-09-11 (commit e7f40d2)

### Added
- **GUI 日志优化 4 选 4**：
  - **A. 长行 word-wrap**（P1-30 已修过）：log EDIT 不带 `ES_AUTOHSCROLL` → 自动 word-wrap。
  - **B. 60000 字符上限实现（M-3 修复）**：`appendLog` 入口检查 `WM_GETTEXTLENGTH`，超 60000 就 `EM_SETSEL(0, n-30000) + WM_CLEAR` 删前段保留后 30000。
  - **C. Copy / Clear / Save 3 按钮**：状态栏右侧 60px×22px 各 1 个；Copy 全选 + `WM_COPY`；Clear `WM_SETTEXT` 空串；Save dump 到 `exeDir/smith.session.log` + 状态栏提示结果。
  - **D. 时间戳 + level 标签**：`logx/log.go` 投递改 `PostMessage(WM_LOG_LINE, level, lparam)`，wparam=level；`appendLog` prepend `[HH:MM:SS] [L] `（`D/I/W/E` 4 个 level）。`pGetLocalTime` 从 `kernel32` 拿本地时间。
- `msgs.go` 加 `WM_COPY/CUT/PASTE/CLEAR` 常量。

### Changed
- logx 投递走 wparam=level 路径（为 P2-2 think 染色铺路）。

---

## [P2-0] - 2026-09-11 (commit ec1b228)

### Fixed (5 CRITICAL bug, verifier 复核全 CONFIRMED)
- **C-1 日志截断**：`appendLog` 改 `EM_SETSEL(-1, -1) + EM_REPLACESEL` 移到末尾追加（之前 `EM_SETSEL(0, -1)` 选全部再 REPLACESEL = N 次 O(N) 全替换）。
- **C-2 Esc 杀 worker**：`runWorker` 永不退出 + OnStop 改用 `stopRunCh` 取消 runCtx（之前 cancel 完 worker 重赋 ctx 是死代码，一次 Esc worker 永久死亡）。
- **C-3 keyfile 弹窗**：新增 `hasUsableKey()` helper（KeyFile 配且文件可读不弹窗），`main.go:92` 触发条件改 `!hasUsableKey(cfgInstance) && *keyFlag == ""`。
- **C-4 URL 双 /v1**：`llm_openai.go:116` TrimRight + HasSuffix("/v1") 自适配 base URL（避免 `https://api.openai.com/v1` + `/v1/chat/completions` = 404）。
- **C-5 history 滑动窗口方向反向**：`history.go:60-67` 重写，保留 system 段 + 最新 `maxHistoryMessages - system_len` 条（之前 `m[:start]` 取最旧丢所有近期）。

### Verified
- 6 包测试全过 + 3 个 `TestClipHistory_*` PASS（系统 + cap 含 system 算进 cap）。
- 双架构 build OK。

---

## [P1-33] - 2026-09-11 (commit dde1358)

### Changed (UI)
- **keydialog 3 radio → 2 radio**：
  - 删除 "DeepSeek" 预设 radio
  - 保留 "OpenAI 格式" / "Anthropic 格式" 两个协议预设
  - 其它 base URL / model / API key 字段保留手填（默认值是 OpenAI 格式 → `https://api.openai.com/v1` + `gpt-4`，Anthropic 格式 → `https://api.anthropic.com` + `claude-3-5-sonnet-20241022`）
  - hint 文字更新为 "OpenAI 格式: POST `{base}/chat/completions` (Bearer auth)    Anthropic 格式: POST `{base}/v1/messages` (x-api-key auth)"
  - 兼容老 `provider=deepseek` ini（agent 层 `ProviderDeepSeek` enum + main.go case 保留，自动映射到 OpenAI 协议）

---

## [P1-32] - 2026-09-11 (commit 0c5fc41)

### Added
- `main.go` 加 2 行 `** ver` trace log：`calling PromptAPIKey` + `PromptAPIKey returned ok=... save=... keyLen=...` 永久保留为产品 trace。

---

## [P1-29~31] - 2026-09-11 (commit bb57b5b / 814b35a / 4d5f151 / 7bae79c / dde1358 / a685701)

### Fixed (主窗口系列 bug)
- **P1-29 keydialog sub loop 卡死**：DestroyWindow send WM_DESTROY 是"directly to WndProc, bypassing message queue"，sub loop 等不到 → 改用 `IsWindow(dlgHwnd) == 0` 每次循环检查。
- **P1-30 子控件白屏**：5 个子控件 styles 漏 `WS_VISIBLE` → 加上。
- **P1-31 logx.SetHWND 没人调**：GUI 看不到任何 log → 加 `OnMainWindowCreated` 回调，main 绑 hwnd。

---

## [P1-25~28] - 2026-09-11 (commit 4d5f151 / 7bae79c / dde1358 / 9772561 等)

### Added (首次运行体验)
- **首次运行 API key 对话框**（5 字段）：Provider 3 radio (OpenAI/Anthropic/DeepSeek) + Base URL + Model + API Key + Save checkbox。
- **`run.ps1` 修复**（`Start-Process` 空参 bug + mojibake）：`param()` 必须是脚本第一条非注释语句；`Start-Process -ArgumentList @()` 抛错改 `if ($argList.Count -eq 0)` 分支。
- **dialog 加 provider + base URL + Model**：`cfg.Save` 支持 merge 模式（保留 `[agent]/[ui]` 段，只覆盖 `[llm]`）。

---

## [P1-23~24] - 2026-09-11 (commit 814b35a / bb57b5b)

### Fixed (主窗口 bug)
- **`forceOnPrimaryMonitor`**：`SystemParametersInfoW(SPI_GETWORKAREA)` + `SetWindowPos` 强制主显示器（修多虚拟适配器白屏）。
- **`ShowWindow(SW_RESTORE)`**：解 `CW_USEDEFAULT` 最小化到 (-32000,-32000) 的 Win32 老 bug。
- 删除 dist/ 二进制，添加 `run.ps1` 源码启动脚本（智能 rebuild、Start-Process 空参处理）。

---

## [P1-22] - 2026-09-11

### Refactored (review fixes)
- `errors.Is` 替代字符串匹配 (L5 修复)。
- `AGENTS.md` / `README.md` 文档同步。
- `net.go` User-Agent 区分。
- `loop.go` 删死代码 + strings import。

---

## [P1-0 ~ P1-14] - 2026-09-11 (18 commits)

### Phase 1: Core skeleton + 14 工具 + LLM 适配 + loop + main

| Batch | 范围 |
|---|---|
| P1-0 | `src/` 目录骨架 |
| P1-1 | `win/wstr.go` (M1 + L5) |
| P1-2 | `win/api_kernel.go` |
| P1-3 | `win/sysinfo.go` (L1) |
| P1-4 | `win/api_user_gdi.go` |
| P1-5a/b | `win/job.go` + `proc.go` (M2) |
| P1-6 | `cfg/ini.go` |
| P1-7 | `logx/log.go` + `win/msgs.go` |
| P1-8 | `win/gui.go`（三区 GUI + LockOSThread + strKeep + WM_LOG_LINE 处理） |
| P1-9a | `tools/registry.go` + `exec.go` + `run_script.go` |
| P1-9b | `tools/` 其余 12 工具 + screenshot |
| P1-10a | `agent/llm.go` + `prompt.go`（Anthropic/OpenAI/DeepSeek 适配层 + 15 测试） |
| P1-10b | `agent/loop.go` + `history.go` + `verdict.go`（13 测试） |
| P1-11 | `main.go` 启动序列 + GUI 钩子 + worker 编排 + cfg.Default/Provider |
| P1-12 | `build.cmd` + `dist/smith{,64}.exe` |
| P1-13 | `smith.ini.example` 模板 + 3 测试 |
| P1-14 | 集成测试 e2e (9) + smoke_bin |

---

## [Phase 0] - 2026-09-11 (5 commits)

### Phase 0: 5 个独立 spike 程序（read-only 探针）

| Spike | 验证 |
|---|---|
| `spike/job` | Job Object + 进程快照 + 杀树 + 386 字节缓冲 |
| `spike/gui` | Win32 窗口 + LockOSThread + UTF-16 持引用 |
| `spike/hello` | 提交限制自检（输出全部 ASCII） |
| `spike/https` | 嵌入 CA bundle + TLS 1.2 + 4 步对照 |
| `spike/dlls` | 可加载 DLL 清单 |

`spike/*` 5 个产物保留在 `dist/spike{386,64}/*.exe`（拷 U 盘验 PE 用）。

---

## 项目里程碑

| 日期 | 里程碑 |
|---|---|
| 2026-09-11 | **P0 全部 5 个 spike 完成**（独立审计 + 3 轮复核） |
| 2026-09-11 | **P1 全部 18 步完成**（GUI 骨架 + 14 工具 + 3 LLM 适配 + loop + main + build + 集成测试） |
| 2026-09-11 | **rename owl → smith**（P1-23~28） |
| 2026-09-11 | **P2-0 5 CRITICAL 修复**（verifier 复核全 CONFIRMED） |
| 2026-09-11 | **P2-1 GUI 日志优化**（word-wrap + 60000 截断 + Copy/Clear/Save + 时间戳） |
| 2026-09-11 | **P2-2 think 块单独缩字号**（EM_SETCHARFORMAT CHARFORMATW 92 字节） |
| 2026-09-11 | **P2-4 排版修复**（双重 [I] 去除 + 防御性 \r\n + 5pt 字号） |

## 仓库统计

- **32 个 commit** on `master` @ `mojingjing-git/PE-worker`
- **6 个测试包**：agent (15+ tests) / cfg (3) / logx (10) / test (9 e2e) / tools / win (8+)
- **2 个 spike 平台产物**：386 (5.4MB) + amd64 (5.5MB)
- **5 个 spike 探针产物**：spike386 (5) + spike64 (5)
- **0 依赖外部库**（无 CGO / 无 -race / 无 go.mod 依赖）
- **GUI 100% 原生 Win32**（user32 内建控件 + gdi32 字体，不引 comctl32 / 浏览器 / 任何库）

## 待办（按优先级）

- [ ] **Batch 1**（LLM 适配层 9 条）— 解锁视觉/多模态 + 中文 PE 编码
- [ ] **Batch 2**（Win 互操作 + Job 杀树大改）— 接入 exec/run_script 杀进程树
- [ ] **Batch 3**（GUI 交互 5 条）— 同 PR atomic
- [ ] **Batch 4**（杂项/安全/健壮）— 含 L-2 kill 工具
- [ ] **真机 PE 测试**（spike/{job,gui,hello} 拷 U 盘进 Win7/10/11 PE 验）

## 关键文档

- `PLAN.md` — 设计源头（v1 第四轮 + v2 复审合并的 9 条硬规则 + 字段表 + 阶段计划）
- `AGENTS.md` — 给 AI 编程 agent 的工作约定（5 条契约 + 步间审核 + 386/amd64 + 中文 path）
- `.workbuddy/audit/2026-09-11-P1-audit.md` — 6 切片并行审计报告（35+ bug + 5 Batch 修复计划）
- `.workbuddy/audit/2026-09-11-P1-verify.md` — verifier 独立审核 6 维度
- `.workbuddy/memory/MEMORY.md` — 顶部 5 CRITICAL 摘要（永久 trace）
- `docs/01-05, 07-08` — 7 篇专项设计（06 编号空缺）
- `spike/*` — Phase 0 5 个独立探针（read-only）
