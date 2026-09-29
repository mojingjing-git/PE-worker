# PE-agent (smith / 铁匠)

[![Go 1.20.14](https://img.shields.io/badge/Go-1.20.14-00ADD8?logo=go)](https://go.dev/dl/)
[![386 + amd64](https://img.shields.io/badge/arch-386%20%2B%20amd64-blue)](#build)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](./LICENSE)

> 一个**单 exe、纯 Go、32 位为主**的 Windows PE 应急助手。
> 单 exe GUI + 17 工具（全部已接线）+ 多 LLM provider。工具清单与接线状态见 [`docs/12` §十一](docs/12-收尾与功能补齐计划.md)。
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

- ✅ **单 exe 部署** —— **约 5.5–6 MB**，无外部 DLL 依赖（> 体积由 `build.cmd` 构建时实测输出，此处勿手写精确字节数 >）
- ✅ **双架构** —— 386 是主战场（Win7 PE 主力 32 位）
- ✅ **GUI 三区** —— 多行日志 + 单行输入 + 状态栏（v1-M1 持引用 + LockOSThread）
- ✅ **17 个工具** —— exec / run_script / ls / cat / grep / find / write / edit / append / http_get / https_get / ps / help / selftest / diskinfo / sysinfo / kill。全部已接线；逐个的执行路径见 [`docs/12` §十一](docs/12-收尾与功能补齐计划.md)。
- ✅ **3 LLM provider** —— Anthropic / OpenAI / DeepSeek（DeepSeek 特有的 `reasoning_content` 单独吸收）
- ✅ **自带 CA bundle** —— `assets/cacert.pem` 用 `//go:embed` 嵌入，绕过 PE 镜像里过时的系统根库
- ✅ **早期文件日志** —— GUI 起来前就能 trace，方便排查 PE 里看不到控制台的情况
- ✅ **启动失败弹 MessageBox** —— 所有非零退出码统一收口到 `main.go` 的 `fatalExit`：先写日志再弹窗（弹窗里带日志绝对路径，PE 现场照着就能把日志带回来）。`--no-gui` 不弹（MessageBox 是模态阻塞调用，无人点 OK 会把 smoke 测试挂死）
- ✅ **错误透传** —— 4xx body 全文带回（PE 里没浏览器能查文档）
- ✅ **Esc / Stop 中止** —— 取消信号贯通到工具层（`tools.Context.Ctx`，`exec` / `run_script` 从 `runCtx` 派生），且 **`exec` / `run_script` 都能杀整棵进程树**：都走 `win.StartJobCmd`（CreateProcess + Job Object + `KILL_ON_JOB_CLOSE`），Stop 即 `TerminateJobObject`。
  → **PE 场景限定（务必读）**：
  1. **Win7 无嵌套 job** —— `AssignProcessToJobObject` 失败时降级链是 `TerminateJobObject` → `KillTreeSelfContained`（M2 双层 PID 复用防护）→ `OpenProcess`+`TerminateProcess`（**此时只剩直接子进程**，孙进程需手动 `taskkill /T /F /PID`）。**"Win7 上 Assign 实际会不会失败"至今没有真机实测**（`docs/07` P0-5 只在 Win11 上验过，Win11 支持嵌套 job）。
- ✅ **Job Object 杀整棵进程树** —— **已接入 `exec` 与 `run_script`**（`tools/exec.go` / `tools/run_script.go` → `win.StartJobCmd` → `win/jobexec.go`）。降级链与上面同款限定。
- ✅ **`kill`** —— 走完整 M2 双层 PID 复用防护（root 名字校验 + 子节点 `InheritedFromUniqueProcessId` 校验）。建议带期望进程名调用（`kill <pid> <name>`）：PID 被系统复用给别人时会拒绝误杀。详见 [`docs/12` §十一](docs/12-收尾与功能补齐计划.md)。
- ⛔ **未实现**（Phase 4 视觉）—— `screenshot` 工具、图片 `attach_image` 多模态回传；`vision = 1` 目前不产生任何工具。
- ⛔ **未实现**（PLAN §3 P3-2 系统类）—— **`netinfo`**（无 tools 层工具；可先用 `exec ipconfig` 顶替）、**`download`**（由 `http_get` / `https_get` 承担）、**`hash`**。


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

> **体积约 5.5–6 MB 量级，不要在本文件手写精确字节数。**
> 精确值由 `build.cmd` 收尾的 PE 校验步骤（`verify-pe.ps1`）实测输出，且随代码变动漂移 —— 手写的数字必然腐烂。
> 核对命令：`Get-ChildItem dist -File -Filter '*.exe' | Select-Object Name, Length`
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

| 参数 | 状态 | 说明 |
|---|---|---|
| `--key <key>` | ✅ | 覆盖 `smith.ini` 里的 key。**明文在 tasklist 可见**，不推荐 |
| `--no-gui` | ✅ | headless 冒烟：发一条 `"ver"` → 等 worker 跑完 → exit 0。**此模式下所有启动错误只写日志 + stderr，不弹 MessageBox** |
| ~~`--console`~~ | ⛔ **已删除** | 产物是 `-H windowsgui` 子系统，**没有控制台**，`os.Stderr` 全丢弃；而 `AttachConsole(ATTACH_PARENT_PROCESS)` 只在父进程有控制台时才成功 —— U 盘双击场景**必然失败**。PE 里唯一可靠的可读输出通道是 **MessageBoxW**（见上面的「启动失败弹 MessageBox」）。留一个"承诺弹控制台但什么也不做"的假开关比没有更坏 |

**退出码**（`main.go` 包注释里的约定，现已全部可达）：

| 码 | 含义 | 现在的表现 |
|---|---|---|
| `0` | 正常退出（用户关闭窗口 / 取消 key 对话框 / `--no-gui` 跑完） | 静默 |
| `1` | 启动错误（日志开不了 / `smith.ini` 格式错 / LLM 客户端初始化失败 / key 写盘失败） | 弹 MessageBoxW |
| `2` | GUI 错误（`RegisterClassEx` / `CreateWindowEx` / `GetMessage` 失败） | 弹 MessageBoxW |
| `3` | worker 异常退出（`tools`/`agent` 任一层 panic） | 日志 + MessageBox + 退出 |

---

## 项目结构

```
peagent/
├── PLAN.md                # 设计源头（决策 / 风险 / 字段表）
├── AGENTS.md              # AI agent 工作约定
├── README.md              # 本文件
├── CHANGELOG.md           # 变更日志
├── LICENSE                # MIT
├── go.mod                 # module peagent, go 1.20
├── build.cmd              # 一键 vet + gofmt + build + PE 校验 + test
├── verify-pe.ps1          # PE 头校验（Subsystem / 导入表 / 体积）
├── run.ps1                # 开发用启动器（锁 Go 1.20.14，编到 .tmp/ 再起 GUI / -NoGui 烟雾测试）
├── smith.ini.example      # 配置文件模板
├── .gitattributes         # 行尾强制：*.cmd/*.bat/*.ps1 = CRLF
├── .gitignore             # dist/ 逐个放行 + 隐私/临时文件排除（细则见 AGENTS.md §7）
├── docs/                  # 12 篇专项设计（06 编号空缺；11 = 审计整改，12 = 收尾与功能补齐，13 = 死代码与过时规则整改）
│   ├── 01-WinPE-agent-开源项目调研.md
│   ├── 02-工具集设计建议.md
│   ├── 03-GUI设计与命名建议.md
│   ├── 04-单机传输与部署方案.md
│   ├── 05-视觉与截图支持设计.md
│   ├── 07-Phase0-验证报告.md
│   ├── 08-PE测试清单.md
│   ├── 09-HTA前端方案.md
│   ├── 10-Sciter前端方案.md
│   ├── 11-审计整改计划.md      # S0~S8 八批整改的定义与实施记录
│   ├── 12-收尾与功能补齐计划.md # T0~T5 五批（T0-T4 已落地，见实施记录）
│   └── 13-死代码与过时规则整改计划.md # A~F 批：死代码清理 + 过时断言订正
├── spike/                 # Phase 0 预研 + 前端探针（只读，9 个探针 + 1 个 launcher = 10 个 main）
│   ├── job/ gui/ hello/ https/ dlls/   # Phase 0 五项
│   ├── hta/ oem2utf8/ richedit/ screenshot/   # 后续新增四项探针（hta/ 另有 app.hta 运行时模板）
│   └── hta/launcher/       # CreateProcessW 拉 mshta（绕开沙箱 LOLBin 检测，不进 dist/）
├── assets/                # 嵌入资源
│   ├── assets.go
│   └── cacert.pem         # Mozilla CA bundle
├── src/                   # 唯一可改区
│   ├── main.go            # 入口 + boot 序列 + fatalExit 错误收口
│   ├── tinker.c           # C 实现参考（不编进 exe）
│   ├── win/               # Win32 互操作（api_kernel / api_user_gdi / wstr / gui / keydialog / msgs / job / jobexec / proc / sysinfo / oem / msgbox）
│   ├── agent/             # LLM 客户端 + 适配层 + loop + history + verdict
│   ├── tools/             # 17 工具（read / write / net / ps / exec / run_script / meta / sysinfo + limited_writer + runneresult）
│   ├── cfg/               # INI 解析
│   ├── logx/              # 日志
│   └── test/              # 集成测试（e2e + smoke_bin）
└── dist/                  # 产物（部分入仓）
    ├── smith.exe            # Phase 1 主产物
    ├── smith64.exe
    └── spike{386,64}/     # 9 个 spike exe / 架构 + app.hta（PE 测试用；launcher 不构建，不在此列）
```

---

## 17 工具

| 工具 | 用途 | 风险等级 |
|---|---|---|
| `exec` | 执行命令（白名单软护栏 + confirm 拦危险）；**已接 Job Object，Stop 杀整棵树** | exec |
| `run_script` | 执行 .bat 脚本（仅 ASCII）；**已接 Job Object，Stop 杀整棵树** | dangerous |
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
| `diskinfo` | 逻辑盘容量/剩余/类型；无参则列所有盘 | read |
| `sysinfo` | OS 版本 / 机型 / 内存 / 计算机名 / 用户名 / 已运行秒数 | read |
| `kill` | ✅ 按 PID 杀整棵进程树，带期望进程名时校验进程身份 | dangerous |

**白名单是软护栏**（不阻断，只 warn），**真正拦危险操作的是 confirm 交互**。

> ⚠️ **`confirm` 当前是"默认同意"**（`main.go` 的 `makeConfirm` 简化实现，注释里写明"Phase 2 后期再换成真弹窗"）。上面「风险等级」列描述的是**设计意图**，不是运行时强制。


---

## 阶段进度

> 批次定义见 [`docs/11-审计整改计划.md`](./docs/11-审计整改计划.md)（S0~S8）与
> [`docs/12-收尾与功能补齐计划.md`](./docs/12-收尾与功能补齐计划.md)（T0~T5）。每批的"实施记录"章节记录**实际做了什么 / 门禁自己抓到的问题**，不要只看勾选框。

- [x] **Phase 0** —— 技术预研（5 个 spike 程序 + 3 轮代码审计 + 25 个问题修复）
- [x] **Phase 1** —— 骨架打通（GUI + exec + 14 工具 + LLM 适配 + loop + e2e 测试）
- [x] **P2-0** —— 5 CRITICAL bug 修复（verifier 复核全 CONFIRMED）
- [x] **P2-1** —— GUI 日志优化（word-wrap + 60000 截断 + Copy/Clear/Save 按钮 + 时间戳）
- [x] **P2-2** —— think 块单独缩字号（EM_SETCHARFORMAT 5pt；宿主控件已从 EDIT 换成 RichEdit20W，docs/12 T4-12）
- [x] **P2-4** —— 排版修复（双重 [I] 去除 + 防御性 \r\n）
- [~] **Batch 1**（部分完成）—— LLM 适配层 9 条。**已落地**：H-1 OEM→UTF8（commit `4f7ced6`，`win/oem.go`，被 `exec.go` / `run_script.go` / `read.go` 调用）；H-3 重试 + H-4 CheckRedirect（`llm.go` `checkRedirect`，含 `x-api-key` 跨 host 剥离）、S4-2 Anthropic `/v1` 自适应、S4-4 `ResponseHeaderTimeout` 放宽至 90s、S4-5 `extractInputArg`、S4-10 scheme 校验。**未落地**：H-2 image wire / M-6 `attach_image` / M-7 think 剥离 / M-8 空 tool 占位（均属 Phase 4 多模态通道，见 docs/11 §四 "明确不做"）。
- [~] **Batch 2**（部分完成）—— **ctx 贯通已落地**（`tools.Context.Ctx`，`exec` / `run_script` 从 `runCtx` 派生，docs/11 §S1-1）；**Job 杀树已接入 `exec` 与 `run_script`**（docs/12 T2 / `P3-30`：`win/jobexec.go` + `tools/exec.go` + `tools/run_script.go`）。**仍未做**：docs/11 §S1-2 原定的"生产零调用点"结论已翻转。
- [x] **Batch 3** —— `IsDialogMessage` 已接（`keydialog.go`）；**S7-6 `--console` 已删除**（docs/12 T1-4，commit `c5d82da`），改用 `win.MessageBoxW` 收口所有非零退出码。
- [x] **Batch 4** ✅ 完整性 / 安全性 / 可观测性 —— 含 `kill` 工具接线（commit `P3-21`）
- [x] **S6 产物门禁** —— `verify-pe.ps1` PE 头校验 + smoke 新鲜度断言 + **68 条 Win32 常量门禁** + gofmt 门禁（详见 docs/11 §S6 实施记录）
- [x] **S8 文档对齐** —— README / PLAN / CHANGELOG / AGENTS / smith.ini.example 与代码现状对齐
- [x] **T0 线程安全** —— `wstrKeep` 加锁（commit `637d0f8` / 复审 `338ae9a`）
- [x] **T1 可诊断性** —— `win/msgbox.go` + `fatalExit` 错误收口 + 退出码 3 通路（commit `c5d82da`）
- [x] **T2 Job 杀树** —— `win/jobexec.go` 接入 `exec`（commit `40cfa2e`）
- [x] **T3 功能补齐** —— `diskinfo` / `sysinfo` / `kill` 全部已接线（commit `c5d82da` + `P3-21`）
- [~] **T4 健壮性收尾** —— 12 项中 **10 项已落地**；**T4-6 / T4-7 未做**（见 docs/12 实施记录）
- [ ] **T5 文档对齐** —— 本批（见 docs/12 §五之二）
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
| 9 | Job Object 杀树 | **已接入 `exec`**（`win/jobexec.go` + `tools/exec.go`，commit `40cfa2e`）。Esc 中止时用，PID 复用双层防护（M2）在 `win/proc.go` 就位。**限定**：Win7 无嵌套 job 时降级链可能退到"只杀直接子进程"；`run_script` 尚未接线 |
| 10 | 早期文件日志 | GUI 起来前 trace，PE 没控制台也能查 |
| 11 | 启动失败用 MessageBoxW，不用 `--console` | 产物 `-H windowsgui` 无控制台；`AttachConsole(ATTACH_PARENT_PROCESS)` 在双击场景必然失败。**必须用 W 版不用 A 版** —— A 版按 ANSI 代码页（本机 ACP=936/GBK）解释字节，Go 的 UTF-8 字符串会乱码 |

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

> **不要在本文件手写用例总数。** 精确数以 `go test -list '.*' ./src/...` 的实际输出为准 —— 手写数字必然腐烂。
> 核对命令（顶层 `Test*` 函数数，不含 `t.Run` 子测试）：
> ```powershell
> go test -list '.*' ./src/... | Select-String -Pattern '^Test' | Measure-Object
> # 或不跑测试直接数源码：
> Get-ChildItem src -Recurse -Filter '*_test.go' | ForEach-Object { $n=(Select-String -Path $_.FullName -Pattern '^func Test' | Measure-Object).Count; "$($_.Name) $n" }
> ```
> 规模约 **200+ 个顶层用例**。

| 包 | 覆盖 | 文件 |
|---|---|---|
| `agent` | LLM 客户端（3 provider mock）/ loop / history / verdict / 重定向与重试加固 | `llm.go` `llm_openai.go` `llm_anthropic.go` `loop.go` `history.go` `verdict.go` |
| `cfg` | INI 解析 + provider/default + `Save` 合并模式防写坏 | `ini.go` |
| `logx` | PostMessage 投递 + fallback sink | `log.go` |
| `test` | e2e（loop+tools+cfg 串通，含 17 工具注册断言与 ctx 取消贯通）+ smoke_bin（跑 `dist/smith.exe` + 产物新鲜度） | `e2e_test.go` `smoke_bin_test.go` |
| `tools` | 17 工具 smoke + 输出硬上限 + 注入防护 + 通配匹配 + edit 空 old 防护 + `DRIVE_*` / `PROCESSOR_ARCHITECTURE_*` 常量断言 | `read.go` `write.go` `net.go` `ps.go` `exec.go` `run_script.go` `meta.go` `sysinfo.go` `limited_writer.go` `sysinfo_test.go` |
| `win` | UTF-16 持引用 + 线程安全 / Job Object 386 字节缓冲契约 / **Job 杀树端到端 + 降级链注入测试** / 进程快照 / OEM→UTF8 / **Win32 常量门禁（断言表 × AST 真 diff）** / GUI 消息 | `wstr.go` `job.go` `jobexec.go` `proc.go` `oem.go` `sysinfo.go` `consts_test.go` `gui.go` `msgbox.go` |

> ⚠️ **T4-6 未做**：`appendLog` 只发 `EM_SETSEL`，**没有 `EM_GETSEL`**，所以日志追加
> 会抢走用户当前选区、`truncateLogIfNeeded` 会在**用户选区**上做 `EM_SETSEL + WM_CLEAR`。
> 需真机 desktop 才能验。
> （原并列为"T4-6 / T4-7 / T4-9 未做"的 **T4-9 已由 `P3-25` 补上** ——
> `src/win/proc_test.go:190-193` 现在有真断言 `if ptrSize==4 && sz != 556 { t.Fatalf }`。）

---

## 贡献

读 [`AGENTS.md`](./AGENTS.md) —— 给 AI agent 写的工作约定，里面有人类也适用的：

- Go 1.20.14 锁死 + 禁用 API 清单
- 10 条项目硬规则（M1/M2/L1/L4/L5/B2/V1/S1/C1/J1）
- 386/amd64 双架构注意事项
- 步间审核流程
- 提交 message 格式

---

## 许可

MIT —— 见 [LICENSE](./LICENSE)。
