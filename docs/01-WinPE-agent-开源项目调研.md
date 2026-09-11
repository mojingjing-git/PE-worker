# Win7 PE 可运行的「极简 Agent」开源项目调研

> 调研日期：2026-09-11（含当日**二次检索修正**）
> 目标：找一个能跑在 PE（最好是 Win7 PE / WinPE 3.x，32 位）里的极简 agent，作为自研的参考或起点。
>
> **结论速览（修正版，重要）**
> - 首次结论「没有同类型项目」**不准确**。首次用 `winpe agent` 做关键词，被 "PE = Portable Executable" 严重污染，漏掉了一批项目。
> - 改用 `topic:winpe` / `topic:windows-pe` / `winpe ai` 重查后，**确实存在同类型项目**，其中 `PE_SmartFixer` 与目标高度接近 —— 详见下方「二·补」。
> - 但所有已有项目都**只覆盖 Win7 PE 及以上**（多数只覆盖 Win10/11 PE），**没有任何一个覆盖 XP PE**。
> - 另一个重要技术认知：**Go 自带 TLS**（`crypto/tls` 静态链入，不走 SChannel），选 Go 就完全不需要打包 curl.exe。代价是 Go 对 XP 的支持止于 **Go 1.10**。

---

## 一、先明确「agent」的三种含义

这三个方向的选型完全不同，建议先定一个：

| 类型 | 在 PE 里干什么 | 典型形态 | 本文是否覆盖 |
|---|---|---|---|
| **A. AI Agent** | 在 PE 里和人/自动流程对话，调用 LLM API 诊断、排障、生成脚本 | 一个 exe + config，连公网或内网 LLM | ✅ 有强参考 |
| **B. 管控 / 运维 Agent** | 开机自动上报资产/状态、接收指令、远程控制 | 单 exe + INI，连自建服务端 | ✅ 有参考 |
| **C. 装机 / 部署 Agent** | PE 内自动分区、灌镜像、注驱动、跑后置脚本 | 脚本 + 宿主程序，由 winpeshl.ini 拉起 | ⚠️ 生态很多但都是工具链，不是「agent」 |

---

## 二、Win7 PE 的硬约束（这是决定性的，先看这个）

「Win7 PE」通常指 **WinPE 3.0 / 3.1**（WAIK 3.0，内核 6.1，2009 年）。中文装机圈流传的 PE 大量是这一类，多为 **32 位 x86**。

### 1. 运行时几乎什么都没有
默认镜像只有「支持部署场景的最小资源」，以下全部是**可选组件（OC）**，要用 DISM 加包才有：

| 能力 | 组件名 | 加了之后的问题 |
|---|---|---|
| .NET | `WinPE-NetFx` | 只是 .NET **精简子集**；官方明确列出加密模型（Cryptography Model）、COM 互操作等功能**受限或不可用** |
| PowerShell | `WinPE-PowerShell` | **不支持 remoting**、不支持 ISE；需依赖链 WMI → NetFx → Scripting |
| WMI / HTA / VBScript | `WinPE-WMI` / `-HTA` / `-Scripting` | 分别加包，体积和依赖链都在涨 |

> 所以在 WinPE 里写 agent，**默认假设是「裸 Win32 API + 自带一切依赖」**，不能假设 .NET / PowerShell / WMI 存在。

### 2. 网络：只有有线，HTTPS 基本废掉
- **无 WLAN**：官方文档明确写「Windows PE 和 Windows RE 不支持通用无线网络功能」→ 只能走有线网，或把无线驱动和配置预先塞进 boot.wim。
- **TLS 是最大的坑**：Windows 7 的 SChannel **默认只启用 TLS 1.0 / SSL 3.0**，TLS 1.2 需要 `KB3080079` + `KB3140245` + 注册表 `SchUseStrongCrypto`，而 WinPE 3.x 的 schannel 组件更残缺。
  → **结论：不要指望在 Win7 PE 上用系统自带的 HTTPS 栈去调 OpenAI / Claude / 自建云 API，这条路技术上走不通。**
- 三条出路（按推荐度）：
  1. **自带 crypto**：静态链接 mbedTLS / BearSSL / WolfSSL / OpenSSL，自己实现或打包 curl。`xpharness` 项目已用「curl 7.42.1 + OpenSSL 1.0.2u 静态版」在 XP 上跑通 TLS 1.2，实证可行。
  2. **内网明文中转**：PE 里只发明文 HTTP 到内网一台中转机 / Ollama，由中转机去连 HTTPS。`retro-agent` 就是这个思路。最省 PE 侧资源。
  3. **外部 relay**：PE 里只跑极简 stub（自定义二进制协议），现代机器上的 relay 负责协议转换 + TLS。PE 侧开销最小，但要自己设计协议。

### 3. 部署与运行环境
- **CRT**：不要依赖 VC++ 运行库。要么 `/NODEFAULTLIB` 完全免 CRT，要么静态链接 CRT。`retro-agent` 特意强调「no UCRT/MSVC runtime」。
- **入口**：PE 里没有资源管理器和开机启动项，靠 `winpeshl.ini` 指定启动命令拉起自己的程序。
- **资源**：WinPE 3.0 默认 scratch space 512MB（1GB 以上内存的机器），最小可跑的内存门槛很低（`retro-agent` 目标是 64MB）。
- **72 小时强制重启限制**：WinPE 会强制重启，长驻 agent 要自己处理。
- **无持久化**：注册表关机即失，配置只能靠 INI / 命令行 / 网络下发。

---

## 二·补、同类型项目（二次检索补充 —— 首次调研遗漏）

首次检索用 `winpe agent` 关键词，被 "PE = Portable Executable" 严重污染。改用 `topic:winpe`、`topic:windows-pe`、`winpe ai`、`winpe llm` 重新检索后，找到以下**同类型**项目。

### ⭐⭐⭐ `ziyouzhiyi666888/PE_SmartFixer` —— 与目标最接近的同类

| 项 | 内容 |
|---|---|
| 语言 / 许可 | **Go** ／ GPL-3.0 |
| 星标 / 时间 | 5★，2026-08-20 创建，最后推送 2026-08-28 |
| 目标环境 | **Windows PE（Win7 ～ Win11 内核均可），需联网** |
| 产物 | 单 exe（`amd64` + `386` 双架构）+ `config.json`；**零外部依赖，成品仅数 MB** |
| GUI | **有**（文本框 + 截图选择 + 按钮），**纯原生 Win32 API 消息循环**，不是控制台 |
| 线程模型 | **`PostMessage` 异步消息分发**，README 明确说是为了"规避跨线程野指针与死锁风险" |
| 网络处理 | 启动时做网络预检，无网自动降级，主 UI 不卡顿 |
| LLM | 阿里云百炼 **Qwen-VL（多模态视觉）**，看图诊断蓝屏 / 驱动问题 |
| 系统操作 | **离线注册表 Hive 挂载**（脱机修复目标系统注册表）；系统文件补全 |
| 安全机制 | SAFE / WARNING / CRITICAL 三级风险熔断（CRITICAL 自动禁止执行）；修改前自动生成 `.reg` 备份 |
| 构建 | **Go 1.20**（正是最后一个支持 Win7 的 Go 版本） |

**它几乎就是本项目架构设计的实物版**：Go 单 exe、Win32 消息循环、`PostMessage` 跨线程、配置文件放 API Key、风险分级、自动备份回滚。动手前应当先精读它的源码。

**它不是通用 agent 的地方（也是本项目的差异点）：**
- 只做两种动作：注册表服务修复（`REG|服务名`）、系统文件补全（`FILE|文件名|相对路径`）
- 是"专用蓝屏修复工具"，不是"读 / 写 / 执行"三件套的通用 agent
- **不覆盖 XP PE**
- **GPL-3.0 有传染性** —— 如果本项目要闭源或商用，**不能抄它的代码**，只能参考架构

### ⭐⭐ `XYLOHEAT/portable-ai-ventoy-winpe` —— 在 PE 里跑现成 AI CLI

- PowerShell 写的便携启动器，从 Ventoy U 盘在 **64 位 WinPE** 里启动 Codex CLI / Claude Code / OpenCode / Crush / Goose
- **值得抄的技巧**：把 `HOME` / `USERPROFILE` / `APPDATA` 及工具专属配置路径重定向到 U 盘，避免写进 PE 的 RAM 盘
- 自带 MinGit 供 agent 做 Git 操作；public 仓库刻意**不含二进制**，靠 updater 从官方源拉取并校验 SHA-256
- **硬要求：64 位 PE + ≥4GB 内存**；README 明说「WinPE 不受这些 agent 官方支持，兼容性 best-effort」，出问题建议换更新的 PE 或改用 Linux live ISO
- **无法用于 Win7 / XP PE**

### ⭐ `MilkyWay008/Hermes-OTG`

- U 盘上的便携 Hermes Agent，定位 IT 救援（自带 31 个 Sysinternals 工具、无头浏览器、MCP）
- 明确**只支持 Windows 10/11**，README 系统需求表写死；未来计划只有 Linux/macOS
- 值得参考的是它的**便携化手法**：捆绑 CPython 3.12、重定向所有 profile 路径到 U 盘、零注册表写入、零管理员权限

### 相关但非同类

| 项目 | 说明 |
|---|---|
| `intelfans/WinDeployStudio`（Dart/Flutter，37★） | WinPE 部署工具箱，带 "AI Assistant" |
| `59de44955ebd/WinSetupShell`（Python，28★） | 给 Win11PE 用的桌面 shell（不是 agent） |
| `slorelee/PExplorer`（173★） | WinXShell 的 shell 部分，给 WinPE 提供桌面 / 任务栏 / 开始菜单 |

### ⚠️ 修正后的结论表

| 目标环境 | 有没有现成的可直接用 |
|---|---|
| Win10 / 11 PE（64 位） | **有** —— `portable-ai-ventoy-winpe`、`Hermes-OTG` |
| Win7 PE | **部分有** —— `PE_SmartFixer` 明确支持 |
| **XP PE** | **空白** —— 没有任何现成项目 |

> **准确的说法是**：不是"没有同类项目"，而是"**同类项目都停在 Win7 PE 及以上，XP PE 无人覆盖**"。
> 如果 XP PE 是硬需求，确实没有能直接拿来用的东西 —— 但**架构可以照 `PE_SmartFixer` 抄**。

### 🔑 由 PE_SmartFixer 得到的两个重要认知

1. **Go 自带 TLS，Win7 PE 的 TLS 难题对 Go 二进制不存在。**
   Go 的 `crypto/tls` 是纯 Go 实现，静态链进 exe，**完全不经过 SChannel**。所以 PE 侧可以直接 `net/http` 调 HTTPS 的 LLM API，不需要打包 curl.exe、不需要 OpenSSL、不需要 cacert.pem。
   → **这直接推翻了「必须自带 crypto，最省是打包 curl.exe」的结论**（该结论只对 C/C++ 成立）。

2. **Go 对老系统的支持边界必须记清楚：**

   | Go 版本 | 最低 Windows |
   |---|---|
   | **Go 1.10** | Windows XP SP3（**最后一个支持 XP 的版本**，2018-02） |
   | Go 1.11 ～ 1.20 | Windows 7 |
   | **Go 1.20** | 最后一个支持 Win7/8/Server 2008/2012 的版本 |
   | Go 1.21+ | Windows 10 |

   → **只做 Win7 PE，Go 1.20 是最优解；XP PE 是硬需求的话，Go 出局，退回 C / Zig。**

---

## 三、GitHub 上的现成项目（按相关性排序）

### ⭐ A 类：面向老系统 / PE 的 AI Agent —— 最接近需求的

#### 1. `benmaster82/retro-agent` —— **最值得作为架构蓝本**
- **语言**：Zig ｜ **License**：MIT ｜ ⭐18 ｜ 最后提交 2026-03（6 个 commit）
- **目标环境**：Windows XP SP3 x86，64MB 内存即可跑
- **产物**：单文件、无依赖、strip 后 **约 750KB**
- **架构**：
  - Agent loop：对话历史 + 工具定义 → 发给 Ollama / 任意 OpenAI 兼容接口 `/v1/chat/completions` → 解析 `tool_calls` → 执行工具 → 结果回填 → 循环（最多 10 轮）
  - 传输：HTTP + 自己写的 JSON 构造/解析（`transport/transport.zig`、`utils/json.zig`）
  - TUI：直接用 Win32 Console API，CP437 制表符；**CP850 → UTF-8 自动转换**处理本地化 Windows 输出，再把 LLM 返回的 UTF-8 降级成控制台能显示的 ASCII
  - 安全：命令白名单、路径白名单、审批模式、子进程超时强杀
- **内置工具**：`system_info` / `list_processes` / `network_status`(netstat) / `network_config`(ipconfig) / `check_disk_space`(wmic) / `check_memory_usage`(wmic) / `list_services`(sc) / `get_service_details` / `diagnose_high_cpu` / `ping_host` / `exec` / `file_read` / `file_write` / `list_dir` / `alert`
- **构建**：`build-xp.zig` 里设 `os_version_min = .xp`、`-OReleaseSmall`、strip、单线程；还带了 `RtlGetSystemTimePrecise` → `GetSystemTimeAsFileTime` 的兼容 shim
- **局限 / 注意**：
  - 它连的是 `http://` 明文 Ollama，**没有解决 HTTPS**，假设内网可信
  - Linux 侧只有 `system_info`，实质是 Windows-only
  - 2026-03 之后没再更新，工具集是「诊断」向，不是「操作」向
- 🔗 https://github.com/benmaster82/retro-agent

#### 2. `skibare87/xpharness` —— **TLS 难题的现成解法**
- **语言**：C + PowerShell 2 ｜ ⭐1 ｜ 2026-06 创建
- **做的事**：在 XP 的 PowerShell 2 里跑一个「迷你 Claude Code」，有 run/read/write/edit/grep/find 工具、**内置 TCC 本地编译 C 代码并运行**、离线小模型（TinyLlama 1.1B int8）
- **核心贡献 —— 彻底解决老系统 TLS**：
  > XP 的 TLS 在 SChannel，上限 TLS 1.0；Anthropic API 要求 TLS 1.2；PowerShell 的 `WebClient` / `Invoke-WebRequest` 都走 SChannel，**任何设置都救不了**。
  > 解法：**自带 crypto** —— 打包一个自带 OpenSSL 的 curl（7.42.1 + OpenSSL 1.0.2u 静态），绕过 SChannel，在 2001 年的系统上谈成 TLS 1.2。和 Supermium 浏览器在 XP 上的做法一致。
- **踩坑记录（非常有价值）**：
  - 需要 **.NET 3.5**（PowerShell 2 没有 `ConvertTo-Json`，要靠 `JavaScriptSerializer`）
  - 需要 **VC++ 2005 运行库**（`curl.exe` 依赖 `msvcr80.dll`，原版 XP 没有 → 启动直接失败。这是最隐蔽的坑）
  - 二进制不提交仓库，靠 `setup.sh` 在现代机器上拉取/编译（体积 + License 原因）
- **局限**：依赖 PowerShell 2 → **WinPE 默认没有 PowerShell**，此路线在 PE 里需要先加 `WinPE-PowerShell` 组件，或把逻辑改写成 cmd + 自带 exe
- 🔗 https://github.com/skibare87/xpharness

#### 3. `the-open-agent/openagent` —— **反面参考**
- Go 写的单文件 AI Agent，23MB，零依赖双击运行，功能很全（30+ 模型、浏览器自动化、Office 读写、MCP、RAG）
- **但需要 Windows 10+，Win7 PE 上跑不了**。价值在于看「现代 agent 该有哪些能力」，以及理解为什么它在老系统上不可行。
- 🔗 https://github.com/the-open-agent/openagent

---

### ⭐ B 类：能在 PE 里跑的远程控制 / 主机端

#### 4. `Terence0816/RustDesk-QuickHost` —— **单 exe + 老系统兼容的工程样板**
- **语言**：C++ ｜ ⭐32 ｜ 活跃更新（2026-09）
- **明确支持**：Windows XP / 7 / 10 / 11 **以及 WinPE**
- host-only 极简客户端，INI 配置自定义服务器 / ID / 密码 / 语言，单 exe 分发
- 值得抄：单文件分发、INI 配置（PE 无注册表持久化）、老系统兼容的工程取舍
- 🔗 https://github.com/Terence0816/RustDesk-QuickHost

#### 5. `sjkingo/winpe_vnc` —— 把 VNC server 塞进 WinPE 的工具 + 教程
- 🔗 https://github.com/sjkingo/winpe_vnc

---

### ⭐ C 类：WinPE 应用开发模板 —— 写程序的编译参数样板

#### 6. `YuuyaGitHub/Windows-PE-App-Sample` —— **免 CRT 的最小样板**
- **语言**：C ｜ MIT
- 演示「只在 Windows PE 里运行」的程序：**CRT-free（无 C 运行库）**、编译体积极小
- 编译参数可直接抄：
  ```
  cl /nologo /O1 /GS- winpe.c ^
     /link /SUBSYSTEM:CONSOLE /ENTRY:mainCRTStartup /NODEFAULTLIB ^
     kernel32.lib advapi32.lib
  ```
- 关键提醒（原文提到）：**必须用 x64 Developer Command Prompt**，否则会编成 x86 而无法在 x64 PE 里运行（反过来，Win7 PE 多为 x86，则要确保编 32 位）
- 🔗 https://github.com/YuuyaGitHub/Windows-PE-App-Sample

---

### ⭐ D 类：PE 构建 / 集成侧（把 agent 塞进 PE 镜像）

| 项目 | 语言 | ⭐ | 用途 |
|---|---|---|---|
| `pebakery/pebakery` | C# | 360 | WinBuilder 后继，脚本引擎定制 PE/WinRE |
| `VirtualHotBar/HotPEToolBox` | Batchfile | 2228 | 国产纯净 PE 工具箱，看内容组织方式 |
| `mattifestation/WinPETools` | PowerShell | 155 | 简化 WinPE 镜像创建/定制/部署的模块 |
| `cmartinezone/WinPEBuilder` | PowerShell | 133 | 带驱动/脚本/可选包构建 PE 启动介质 |
| `HaroldMitts/Build-CustomPE` | Batchfile | — | **添加 WinPE 可选组件的命令样板**（NetFx/PowerShell/Scripting 全都有） |
| `slorelee/PExplorer` | — | 173 | WinXShell 的 shell 部分，给 WinPE 提供桌面/任务栏/开始菜单 |
| `EdgelessPE/Samare` | — | 46 | 实验性 WinPE 项目 |
| `FirPE-Team/WinPEBuilder` | Batchfile | 22 | FirPE 的构建脚本 |
| `CK-Technology/ghostwin` | Rust | — | Rust 写的 WinPE 部署工具，含 VNC，`Slint` GUI 在 PE 内渲染 |

---

### ⚠️ 检索提示（踩过的坑）

1. **`PE` 是重灾区**：搜 `PE agent` / `pe loader` / `pe tools` 会大量命中 **Portable Executable 文件格式**相关项目（`ttmmcc/peinjector`、`0xDemonCall/pe-loader`、`Torashi1069/petools` 等），**和 WinPE 完全无关**。
2. **`winpe agent` 这种多词 AND 查询会严重漏检** —— 首次调研就是这么漏掉 `PE_SmartFixer` 的。项目名里同时出现 "winpe" 和 "agent" 的概率极低。
3. **真正有效的检索方式**：

   | 查询 | 命中 |
   |---|---|
   | `topic:winpe` | 40+ 项目，覆盖到位 |
   | `topic:windows-pe` | 40+ 项目（注意混入大量 PE 文件格式项目） |
   | `winpe ai` / `winpe llm` | 精确命中 AI 方向 |
   | `"windows pe" agent client` | 0 结果（说明这个词组确实没人这么写） |

4. **教训**：找这类项目应该**先走 topic 页再收窄**，而不是一上来就用多词 AND 查询。

---

## 四、语言 / 工具链选型（针对 Win7 PE 32 位）

| 方案 | 可行性 | 说明 |
|---|---|---|
| **C / C++ (MSVC)** | ★★★★★ | `/NODEFAULTLIB` 免 CRT 或静态 CRT，`_WIN32_WINNT=0x0601`；兼容性最好，体积最小，首选 |
| **Zig** | ★★★★★ | 明确支持老 Windows target，可设 `os_version_min = .win7 / .xp`，单文件小体积（`retro-agent` 实证 750KB）；交叉编译方便 |
| **Rust** | ★★★☆☆ | **1.78 起** Tier 1 的 `*-pc-windows-*` 最低要求升到 **Windows 10**；要 Win7 得用 `x86_64-win7-windows-msvc` / `i686-win7-windows-msvc`（推出时为 Tier 3），或锁 1.77 及更早；MSVC 目标还依赖 VC 运行库 |
| **Go** | ★★★★☆（限 Win7） | **自带 TLS**（见下方说明），单文件即含一切，开发效率最高。但版本红线很陡：**Go 1.10 是最后一个支持 XP 的版本**，**Go 1.20 是最后一个支持 Win7 的版本**，Go 1.21+ 要求 Win10。`CGO_ENABLED=0 GOOS=windows GOARCH=386` 出静态单文件 |
| **.NET** | ★★☆☆☆ | 需加 `WinPE-NetFx` 组件，且是精简子集，加密模型受限 |
| **PowerShell** | ★★☆☆☆ | 需加 `WinPE-PowerShell` + 依赖链，无 remoting |
| **Python / Node** | ☆ | 老 PE 里不现实，放弃 |

### ⚠️ 表格外的一条关键差异：TLS 是不是白送的

这一列直接决定"要不要打包 curl.exe"，所以选语言时必须一起考虑：

| 语言 | TLS 从哪来 | 是否要多带文件 |
|---|---|---|
| **Go** | `crypto/tls` 纯 Go 实现，**静态链进 exe，完全不经过 SChannel** | **不用**，单文件搞定 |
| **Rust** | `rustls`（纯 Rust）可静态链 | 不用，但要自己写 HTTP + JSON |
| **Zig** | 标准库有 TLS 客户端，可静态链 | 不用，同样要自己写 HTTP |
| **C / C++** | 标准库什么都没有 | **要** —— 打包静态 `curl.exe`，或静态链 BearSSL / mbedTLS |

> **`PE_SmartFixer` 用 Go 1.20 在 WinPE 里直接调 HTTPS 的 Qwen-VL API 并成功运行** —— 这是"Go 的 TLS 不走 SChannel"最硬的实证。
> 因此本项目若选 Go，`docs/04` 里"打包 curl.exe + cacert.pem"那一整套就不需要了，结论要相应调整。


---

## 五、推荐路线

> ✅ **已定案（2026-09-11）：语言选 Go 1.20，用户已放弃 XP PE。**
> 本节下面"C 或 Zig 写免 CRT 单 exe"的结论**已被取代**，同样地被取代的还有：`docs/02` §1 的"打包 curl"、`docs/04` 的整套 curl 方案。
> 但本节的核心洞察仍然成立：**免 CRT 编译参数样板**（`YuuyaGitHub/Windows-PE-App-Sample`）和**系统级操作全走 `exec`** 这两条不受语言影响。
> 实施计划见项目根目录 `PLAN.md`。

### 技术选型
**C 或 Zig 写一个免 CRT 依赖的 32 位单 exe**，理由：
- 一个 exe 丢进 PE 就能跑，没有部署成本
- 配置用 **INI / 命令行参数**（PE 无持久化，注册表重启即丢）
- 由 `winpeshl.ini` 自动拉起，或手工双击运行
- 网络层二选一：自带静态 crypto 做 HTTPS，或内网明文中转 / 外部 relay

### 参考项目拼接方式
| 抄什么 | 抄谁 |
|---|---|
| **整体架构（Go 单 exe + Win32 GUI + PostMessage 线程模型 + 离线注册表 + 风险分级）** | **`ziyouzhiyi666888/PE_SmartFixer`** —— 唯一跑在 PE 里的同类，优先精读（注意 GPL-3.0，只参考架构不抄代码） |
| Agent loop、工具集设计、白名单/审批、超时控制、单文件小巧体积、Win32 TUI、编码转换 | `benmaster82/retro-agent` |
| TLS 1.2 绕过 SChannel 的完整方案（**仅选 C/C++ 时需要**）、依赖坑（.NET 3.5 / VC++ 2005 运行库）、工具清单 | `skibare87/xpharness` |
| 免 CRT 程序的最小编译参数模板（**仅选 C/C++ 时需要**） | `YuuyaGitHub/Windows-PE-App-Sample` |
| 单 exe + INI 配置 + 老系统兼容的工程做法 | `Terence0816/RustDesk-QuickHost` |
| 便携化手法：把 `HOME` / `APPDATA` 等 profile 路径重定向到 U 盘，避免写进 PE RAM 盘 | `XYLOHEAT/portable-ai-ventoy-winpe`、`MilkyWay008/Hermes-OTG` |
| 把程序集成进 PE 镜像、加可选组件 | `HaroldMitts/Build-CustomPE`、`pebakery/pebakery` |

### 必须先决策的三个问题
1. **agent 是「对话型」还是「无头管控型」？** 决定要不要 TUI、要不要审批交互。
2. **连公网 HTTPS 还是内网？** 决定要不要自带 crypto（工作量差别很大）。
3. **目标 PE 到底是 32 位 Win7 PE，还是也要兼顾 x64 / 新 PE？** 决定单架构还是双架构。

---

## 六、主要参考链接汇总

**同类型项目（二次检索补充，优先看）**
- PE_SmartFixer（Go，WinPE 内的 AI 修复工具，最接近的同类）：https://github.com/ziyouzhiyi666888/PE_SmartFixer
- portable-ai-ventoy-winpe（64 位 WinPE 里跑 Codex CLI / Claude Code）：https://github.com/XYLOHEAT/portable-ai-ventoy-winpe
- Hermes-OTG（U 盘便携 agent，仅 Win10/11）：https://github.com/MilkyWay008/Hermes-OTG

**参考项目**
- retro-agent：https://github.com/benmaster82/retro-agent
- xpharness：https://github.com/skibare87/xpharness
- Windows-PE-App-Sample：https://github.com/YuuyaGitHub/Windows-PE-App-Sample
- RustDesk-QuickHost：https://github.com/Terence0816/RustDesk-QuickHost
- winpe_vnc：https://github.com/sjkingo/winpe_vnc
- Build-CustomPE（加可选组件样板）：https://github.com/HaroldMitts/Build-CustomPE
- WinDeployStudio（WinPE 部署工具箱 + AI Assistant）：https://github.com/intelfans/WinDeployStudio

**官方文档与版本红线**
- WinPE 可选组件官方参考：https://learn.microsoft.com/en-us/windows-hardware/manufacture/desktop/winpe-add-packages--optional-components-reference
- Go Wiki 最低需求（XP=Go 1.10 / Win7=Go 1.11~1.20 / Win10=Go 1.21+）：https://go.dev/wiki/MinimumRequirements
- Go 1.11 发行说明（正式移除 XP 支持）：https://go.dev/doc/go1.11
- Go 1.20 发行说明（Go 1.20 是最后支持 Win7 的版本）：https://go.dev/doc/go1.20
- Rust Windows 目标基线调整（1.78 起最低 Win10）：https://blog.rust-lang.org/2024/02/26/Windows-7/
