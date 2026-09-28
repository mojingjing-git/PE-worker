# PE-agent (smith / 铁匠)

[![Go 1.20.14](https://img.shields.io/badge/Go-1.20.14-00ADD8?logo=go)](https://go.dev/dl/)
[![386 + amd64](https://img.shields.io/badge/arch-386%20%2B%20amd64-blue)](#build)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](./LICENSE)

> 一个**单 exe、纯 Go、32 位为主**的 Windows PE 应急助手。
> 三区 GUI + 14 工具 + 多 LLM provider，
> 跑在精简的 Win7 / Win10 / Win11 PE 镜像里，不依赖系统根证书库。

中文代号：**铁匠 (smith)**。窗口标题 `smith - PE agent`，日志前缀 `ai > you > -> <- !!`。

---

## 截图占位

> Phase 1 完成 GUI 骨架（win/gui.go）。Phase 4 视觉阶段补真实截图。

```
┌────────────────────────────────────────────────────┐
│ [I] ** ver boot start log=...\smith.log            │
│ [I] ** ver cfg loaded ini=...\smith.ini            │
│ [W] !! llm: 缺配置（base/model/key）...            │
│ [I] you > 看看 C 盘还剩多少                          │
│ [I] -> diskinfo C:                                 │
│ [I] <- diskinfo: ...                                │
│ [I] ai  > C 盘剩余 12.4 GB（已用 67%）              │
├────────────────────────────────────────────────────┤
│ ver>_                                            Send│Stop
├────────────────────────────────────────────────────┤
│ ready                                              │
└────────────────────────────────────────────────────┘
```

---

## 特性

- ✅ **单 exe 部署** —— **约 5–6 MB**，无外部 DLL 依赖（> 体积由 `build.cmd` 构建时实测输出，此处勿手写精确字节数 >）
- ✅ **双架构** —— 386 是主战场（Win7 PE 主力 32 位）
- ✅ **GUI 三区** —— 多行日志 + 单行输入 + 状态栏（v1-M1 持引用 + LockOSThread）
- ✅ **14 工具** —— exec / run_script / ls / cat / grep / find / write / edit / append / http_get / https_get / ps / help / selftest
- ✅ **3 LLM provider** —— Anthropic / OpenAI / DeepSeek（DeepSeek 特有的 `reasoning_content` 单独吸收）
- ✅ **自带 CA bundle** —— `assets/cacert.pem` 用 `//go:embed` 嵌入，绕过 PE 镜像里过时的系统根库
- ✅ **早期文件日志** —— GUI 起来前就能 trace，方便排查 PE 里看不到控制台的情况
- ✅ **错误透传** —— 4xx body 全文带回（PE 里没浏览器能查文档）
- ⚠️ **Esc / Stop 中止** —— **取消信号已贯通到工具层**（`tools.Context.Ctx` + `exec` / `run_script` 从 `runCtx` 派生，见 docs/11 §S1-1）；**但杀进程树未接入**：Job Object 杀整棵进程树的代码已就绪（`win/job.go` + M2 双层 PID 复用防护），**生产路径零调用点**，见 docs/11 §S1-2。
  → **实测后果**：Stop 只杀 `cmd.exe` 这一个直接子进程，`diskpart` / `dism` / `ping` 等孙子进程继续存活并持裸盘句柄。
- ⚠️ **Job Object 杀整棵进程树** —— **代码就绪，尚未接入**（同上）。
- ⛔ **未实现**（Phase 4 视觉）—— `screenshot` 工具、图片 `attach_image` 多模态回传；`vision = 1` 目前不产生任何工具。
- ⛔ **未实现**（PLAN §3 P3-2 系统类）—— `sysinfo` / `diskinfo` / `netinfo` / `kill` 工具（底层 `win/sysinfo.go` 已有 Windows API 封装，但**没有对应的 tools 层工具**）。

---

## 快速开始

### 准备

- Go 1.20.14（[下载](https://go.dev/dl/go1.20.14.windows-amd64.zip)）
- Windows 7 / 10 / 11 或对应 PE 镜像（验证用）

### 构建

```cmd
:: 完整构建（vet + test + 双架构 build）
build.cmd

:: 含测试
build.cmd test

:: 清空 dist/
build.cmd clean
```

产物：

```
dist\smith.exe     386    Win7 PE 主力
dist\smith64.exe   amd64  新 PE
```

> **体积约 5–6 MB 量级，不要在本文件手写精确字节数。**
> 精确值由 `build.cmd` 收尾的 PE 校验步骤（`verify-pe.ps1`）实测输出，且随代码变动漂移 —— 手写的数字必然腐烂。
> 实测口径见 docs/11 §S6。

### 配置

复制 `smith.ini.example` → `smith.ini`（同目录），填 LLM 配置：

```ini
[llm]
base = https://api.openai.com/v1
model = gpt-4
provider = openai
keyfile = smith.key     ; 优先 keyfile（不暴露在 tasklist）
; key = sk-...          ; 兜底用 ini literal（不推荐）
timeout = 120
```

`smith.key` 放同目录，单行 API key。

**首次运行体验**（PLAN §0.6 A5）：

- 没 key 也没 smith.ini → 启动时弹 modal 对话框（**520x220**），4 个输入：
  - **Provider**: 3 个 radio (OpenAI / Anthropic / DeepSeek) — 切换自动填 Base URL
  - **Base URL**: 文本框（OpenAI / DeepSeek 走 Chat Completions，URL 带 `/v1`；Anthropic 走 Messages，URL 不带 `/v1/messages`）
  - **API Key**: 密码框（ES_PASSWORD 显示 ●）
  - **保存到磁盘**: checkbox（勾上 = 写 `smith.ini` + `smith.key`；不勾 = 仅当次）
- 用户勾选"保存" → 写 `smith.ini`（[llm] 段含 base/model/provider）+ `smith.key`（key 单行）
- 用户不勾 → 全部仅内存，进程退出就丢（**U 盘发给别人用**就这模式）
- 已有 key 的话对话框预填，点 OK 直接覆盖/保留
- Esc / 关窗 = 取消 = 进程退出（明确"没配好就跑不动"）

CLI `--key` 跳过 dialog；`--no-gui` 也不弹。

### 跑

```cmd
:: GUI 模式
dist\smith.exe

:: 无 GUI 烟雾测试（headless，自动跑 "ver" → cancel → exit 0）
dist\smith.exe --no-gui

:: CLI 应急：用 --key 临时覆盖 ini（不推荐，明文在 tasklist 可见）
dist\smith.exe --key sk-...
```

---

## 项目结构

```
peagent/
├── PLAN.md                # 设计源头（决策 / 风险 / 字段表）
├── AGENTS.md              # AI agent 工作约定
├── README.md              # 本文件
├── CHANGELOG.md           # 变更日志
├── go.mod                 # module peagent, go 1.20
├── build.cmd              # 一键 vet + gofmt + build + PE 校验 + test
├── verify-pe.ps1          # PE 头校验（Subsystem / 导入表 / 体积）
├── smith.ini.example      # 配置文件模板
├── docs/                  # 10 篇专项设计（06 编号空缺；09/10/11 为前端方案与审计整改）
│   ├── 01-WinPE-agent-开源项目调研.md
│   ├── 02-工具集设计建议.md
│   ├── 03-GUI设计与命名建议.md
│   ├── 04-单机传输与部署方案.md
│   ├── 05-视觉与截图支持设计.md
│   ├── 07-Phase0-验证报告.md
│   ├── 08-PE测试清单.md
│   ├── 09-HTA前端方案.md      # 未入 commit（工作区新增）
│   ├── 10-Sciter前端方案.md    # 未入 commit（工作区新增）
│   └── 11-审计整改计划.md      # S0~S8 八批整改的定义与实施记录
├── spike/                 # Phase 0 预研 + 前端探针（只读，9 个独立程序）
│   ├── job/ gui/ hello/ https/ dlls/   # Phase 0 五项
│   └── hta/ oem2utf8/ richedit/ screenshot/   # 后续新增四项探针
├── assets/                # 嵌入资源
│   ├── assets.go
│   └── cacert.pem         # Mozilla CA bundle
├── src/                   # 唯一可改区
│   ├── main.go            # 入口
│   ├── tinker.c           # C 实现参考（不编进 exe）
│   ├── win/               # Win32 互操作（api_kernel / api_user_gdi / wstr / gui / job / proc / sysinfo / oem / keydialog）
│   ├── agent/             # LLM 客户端 + 适配层 + loop + history + verdict
│   ├── tools/             # 14 工具（read / write / net / ps / exec / run_script / meta + limited_writer）
│   ├── cfg/               # INI 解析
│   ├── logx/              # 日志
│   └── test/              # 集成测试（e2e + smoke_bin）
└── dist/                  # 产物（部分入仓）
    ├── smith.exe            # Phase 1 主产物
    ├── smith64.exe
    └── spike{386,64}/     # 9 个 spike 程序（PE 测试用）
```

---

## 14 工具

| 工具 | 用途 | 风险等级 |
|---|---|---|
| `exec` | 执行命令（白名单软护栏 + confirm 拦危险） | exec |
| `run_script` | 执行 .bat 脚本（仅 ASCII） | dangerous |
| `ls` | 列目录 | read |
| `cat` | 读文件 | read |
| `grep` | 文件内搜索 | read |
| `find` | 按名字搜文件 | read |
| `write` | 写文件 | write |
| `edit` | 替换文件内字符串 | write |
| `append` | 追加到文件末尾 | write |
| `http_get` | HTTP GET | read |
| `https_get` | HTTPS GET（自带 CA bundle） | read |
| `ps` | 列运行中进程 | read |
| `help` | 工具自描述（JSON schema） | read |
| `selftest` | 健康检查 | read |

**白名单是软护栏**（不阻断，只 warn），**真正拦危险操作的是 confirm 交互**。

---

## 阶段进度

> 批次定义见 [`docs/11-审计整改计划.md`](./docs/11-审计整改计划.md)（S0~S8）。每批的"实施记录"章节记录**实际做了什么 / 门禁自己抓到的问题**，不要只看勾选框。

- [x] **Phase 0** —— 技术预研（5 个 spike 程序 + 3 轮代码审计 + 25 个问题修复）
- [x] **Phase 1** —— 骨架打通（GUI + exec + 14 工具 + LLM 适配 + loop + e2e 测试）
- [x] **P2-0** —— 5 CRITICAL bug 修复（verifier 复核全 CONFIRMED）
- [x] **P2-1** —— GUI 日志优化（word-wrap + 60000 截断 + Copy/Clear/Save 按钮 + 时间戳）
- [x] **P2-2** —— think 块单独缩字号（EM_SETCHARFORMAT 5pt）
- [x] **P2-4** —— 排版修复（双重 [I] 去除 + 防御性 \r\n）
- [~] **Batch 1**（部分完成）—— LLM 适配层 9 条。**已落地**：H-1 OEM→UTF8（commit `4f7ced6`，`win/oem.go`，被 `exec.go` / `run_script.go` / `read.go` 调用）；H-3 重试 + H-4 CheckRedirect（`llm.go` `checkRedirect`，含 `x-api-key` 跨 host 剥离）、S4-2 Anthropic `/v1` 自适应、S4-4 `ResponseHeaderTimeout` 放宽至 90s、S4-5 `extractInputArg`、S4-10 scheme 校验。**未落地**：H-2 image wire / M-6 `attach_image` / M-7 think 剥离 / M-8 空 tool 占位（均属 Phase 4 多模态通道，见 docs/11 §四 "明确不做"）。
- [~] **Batch 2**（部分完成）—— **ctx 贯通已落地**（`tools.Context.Ctx`，`exec` / `run_script` 从 `runCtx` 派生，docs/11 §S1-1）；**Job 杀整棵进程树尚未接入**（§S1-2，`CreateJobObject` / `SetKillOnJobClose` / `AssignProcessToJobObject` / `KillTreeSelfContained` 全仓**生产零调用点**，唯一调用方是各自 `_test.go`）。
- [~] **Batch 3**（部分完成）—— `IsDialogMessage` 已接（`keydialog.go`）；**S7-6 `--console` 仍是空实现**（`main.go` 只有 `_ = *consoleFlag`），`-H windowsgui` 下 PE 里双击会闪退且无任何线索。
- [ ] **Batch 4** —— 杂项 / 安全 / 健壮（含 L-2 kill 工具）
- [x] **S6 产物门禁** —— `verify-pe.ps1` PE 头校验 + smoke 新鲜度断言 + **68 条 Win32 常量门禁** + gofmt 门禁（详见 docs/11 §S6 实施记录）
- [x] **S8 文档对齐** —— README / PLAN / CHANGELOG / AGENTS / smith.ini.example 与代码现状对齐（本批次）
- [ ] **真机 PE 验收**（spike/{job,gui,hello} 拷 U 盘进 Win7/10/11 PE 验）

**详细变更**：见 [CHANGELOG.md](./CHANGELOG.md)
**整改依据**：见 [`docs/11-审计整改计划.md`](./docs/11-审计整改计划.md)（S0~S8 八批）
> ⚠️ 早期版本的本文件引用过 `.workbuddy/audit/*.md`。**该目录被 `.gitignore` 排除（`.gitignore:2`），不随仓库分发 —— clone 下来是死链。** 整改依据已整理进 `docs/11`。

---

## 真 PE 验收

> 工程师做不了的部分 —— 需要 Win7/10/11 PE 镜像。

把 `dist/spike386/` 整个文件夹拷到 U 盘，进 PE 后按 `docs/08-PE测试清单.md` 跑一遍。
**最关键**：`hello.exe` 跑起来（总开关）→ `dlls.exe` 看 DLL 依赖 → `gui.exe` 看窗口 → `https.exe` 验 TLS → `job.exe` 验杀树。

---

## 关键设计决策

| # | 决策 | 理由 |
|---|---|---|
| 1 | Go 1.20.14 锁死 | 1.21+ 产物要 Win10+；PE 主力是 Win7（最后支持 1.20） |
| 2 | 32-bit 为主（386） | Win7 PE 默认 32 位 |
| 3 | 不带 CGO | PE 镜像里无 C 编译器 |
| 4 | 不带 -race | race detector 386 + 无 CGO 不可用 |
| 5 | 不用 `os/user` | 拉 netapi32 / userenv.dll，精简 PE 可能没有 |
| 6 | 不用 `GetVersionExA` | Win8.1+ 无 manifest 返假值；用 `ntdll!RtlGetVersion` |
| 7 | 不用 `GetTickCount64` | Win7 PE 缺，会运行时炸；用 `GetTickCount` |
| 8 | 自带 CA bundle | PE 根证书库不更新；`//go:embed` + `tls.Config.RootCAs` |
| 9 | Job Object 杀树 | **已决定且代码就绪，但尚未接入生产路径**（docs/11 §S1-2）。Esc 中止时用，PID 复用双层防护（M2）已在 `win/proc.go` 就位 |
| 10 | 早期文件日志 | GUI 起来前 trace，PE 没控制台也能查 |

详见 `PLAN.md`。

---

## 测试

```bash
# 全部测试（双架构）
build.cmd test

# 单包
GOARCH=386 go test -count=1 ./src/agent/...

# 集成测试
GOARCH=386 go test -v -run TestE2E ./src/test/...
```

**当前状态**：6 包全过（386 + amd64）。

> **不要在本文件手写用例总数。** 精确数以 `go test -v ./src/...` 的实际输出为准 —— 手写数字必然腐烂。
> 核对命令（顶层 `Test*` 函数数，不含 `t.Run` 子测试）：
> ```powershell
> go test -list '.*' ./src/... | Select-String -Pattern '^Test' | Measure-Object
> ```

| 包 | 覆盖 | 文件 |
|---|---|---|
| `agent` | LLM 客户端（3 provider mock）/ loop / history / verdict / 重定向与重试加固 | `llm.go` `llm_openai.go` `llm_anthropic.go` `loop.go` `history.go` `verdict.go` |
| `cfg` | INI 解析 + provider/default + `Save` 合并模式防写坏 | `ini.go` |
| `logx` | PostMessage 投递 + fallback sink | `log.go` |
| `test` | e2e（loop+tools+cfg 串通，含 14 工具注册断言与 ctx 取消贯通）+ smoke_bin（跑 `dist/smith.exe` + 产物新鲜度） | `e2e_test.go` `smoke_bin_test.go` |
| `tools` | 14 工具全部 smoke + 输出硬上限 + 注入防护 + 通配匹配 + edit 空 old 防护 | `read.go` `write.go` `net.go` `ps.go` `exec.go` `run_script.go` `limited_writer.go` |
| `win` | UTF-16 持引用 / Job Object 386 字节缓冲契约 / 进程快照 / OEM→UTF8 / **Win32 常量门禁（68 条）** / GUI 消息 | `wstr.go` `job.go` `proc.go` `oem.go` `sysinfo.go` `consts_test.go` `gui.go` |

---

## 贡献

读 [`AGENTS.md`](./AGENTS.md) —— 给 AI agent 写的工作约定，里面有人类也适用的：

- Go 1.20.14 锁死 + 禁用 API 清单
- 5 条项目硬规则（M1/M2/L1/L4/L5）
- 386/amd64 双架构注意事项
- 步间审核流程
- 提交 message 格式

---

## 许可

MIT —— 见 [LICENSE](./LICENSE)。
