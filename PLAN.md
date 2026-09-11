# PLAN.md — 极简 PE agent 实施计划

> 决策时间：2026-09-11
> 目标：一个**单文件 exe**，丢进 Win7 PE / Win10 / Win11 PE 就能跑，带 GUI，能读写文件、执行命令、看图诊断。

---

## 0. 决策快照（已锁定）

| 项 | 决定 | 依据 |
|---|---|---|
| **目标环境** | **Win7 PE（WinPE 3.x）及以上 + Win10/11 PE**，32 位为主 + 64 位 | `docs/01` |
| **放弃 XP PE** | ✅ 已放弃 | 用户 2026-09-11 决定 |
| **语言** | **Go 1.20**（最后一个支持 Win7 的版本） | `docs/01` 二·补 + 用户决定 |
| **形态** | 单 exe + INI 配置，无外部文件 | `docs/04` |
| **GUI** | 原生 Win32，只用 user32 内建控件（EDIT / BUTTON / STATIC） | `docs/03` |
| **工具数** | 14 个功能工具 + `selftest` + `help` | `docs/02` / `docs/05` |
| **视觉** | 支持（`screenshot` + `read` 图片分支） | `docs/05` |
| **传输** | Go 自带 `crypto/tls` 直连 LLM API，**不需要打包 curl** | `docs/01` 二·补 |
| **驱动方式** | 控制台交互式（人在 ToDesk 里敲） | `docs/04` |
| **命名** | **暂定 `owl`** ← 待确认，改动只在一处（见 §8） | `docs/03` |

---

## 0.5 审核修订（2026-09-11 · 对抗性审核后）

派了一个独立 agent 对本文做批判性审核，找出 12 条问题。以下是**逐条核实后的处置**（已核实项标注证据）。

### 🔴 致命（3 条，全部接受）

**A1. TLS「白送」是错的说法 —— 信任库不是白送的。**
- **证据**（已查 Go 1.20 源码 `crypto/x509/root_windows.go`）：`loadSystemRoots()` 只返回标记 `&CertPool{systemPool: true}`，实际校验走 `systemVerify()` → `CertGetCertificateChain` / `CertVerifyCertificateChainPolicy`，**都是 crypt32.dll 的 CryptoAPI，读 Windows 系统根证书库**。
- **后果**：WinPE 的根证书库烘焙在镜像里、**不随 Windows Update 更新**，缺新根 CA（如 Let's Encrypt 的 ISRG Root X1）时，连公网 API 会 `x509: unknown authority`。
- **修正（比审核建议更确定的做法）**：
  - **捆绑 Mozilla CA bundle，用 `//go:embed` 嵌进 exe → `x509.NewCertPool()` + `AppendCertsFromPEM()` → 赋给 `tls.Config.RootCAs`。**
  - ⚠️ **不要用「`SystemCertPool()` + 追加 PEM」** —— 那样的 pool 仍带 `systemPool` 标记，Go 会**优先走 CryptoAPI**，你追加的证书在链构建阶段可能根本不被看到，行为不确定。
  - **只有设成「只由捆绑 PEM 构成的 pool」，`systemPool` 才为 false，Go 才会走纯 Go 校验路径**，彻底不依赖 PE 的证书库。
  - 体积代价：CA bundle 约 200KB，仍是一个文件。
- **Phase 0 新增 P0-4**：在目标 PE 里实跑一次到真实端点的 HTTPS 握手，断言校验通过。

**A2. `exec` 输出的编码没规定 —— 中文必然乱码。**
- **问题**：cmd.exe 输出是 **OEM 代码页**（中文系统 GBK/CP936），计划只说"内部统一 UTF-8"，没说怎么转。
- **后果**：中文 PE 下 `ver`、报错信息静默乱码，模型据此误判。
- **修正**：exec 捕获到字节后，显式 `MultiByteToWideChar(CP_OEMCP)` → `WideCharToMultiByte(CP_UTF8)`。写成 `tools/exec.go` 里的一个必过函数，所有子进程输出统一走它。

**A3. Job Object 嵌套 —— Win7 上的隐藏地雷。**
- **问题**：**Windows 7 不支持嵌套 job**（嵌套是 Win8 才有的）。若本进程已被父 job 包含（winpeshl.exe 或 cmd 拉起时都可能），`AssignProcessToJobObject` 会直接失败。
- **后果**：**超时和停止按钮的"杀整棵进程树"静默失效** —— `ping -t` 杀不掉，这正是中止机制的核心。
- **修正（三层兜底）**：
  1. 启动时 `IsProcessInJob(GetCurrentProcess(), NULL, &b)` 检测
  2. 创建子进程时带 `CREATE_BREAKAWAY_FROM_JOB`（需父 job 允许 breakaway）
  3. 以上都不行时降级用 `taskkill /T /F /PID <pid>`（PE 里有 taskkill）—— 保住"杀进程树"这个能力

### 🟠 重要（5 条，全部接受）

**A4. 文档没随「选 Go」清理，互相打架。**
`docs/01` §五 仍推荐 C/Zig 免 CRT；`docs/02` §1 仍首推"打包 curl"；`docs/04` §2/§5/§6 仍是 curl/BearSSL 全文。**→ 已在这三处加「已作废」横幅**（见本次修订）。

**A5. 缺「首次运行 / 密钥注入」。**
PE 里**没有记事本**，验收里写"填好 Key"假设了不存在的编辑手段。
→ 修正：GUI 加一个**首次运行密钥输入框**（复用已有的 EDIT 控件，成本几乎为零）；同时支持 `--key` 命令行参数；文档说明「Key 明文落在 U 盘上」的安全风险。

**A6. 缺 PE 内的可调试性。**
若 GUI 因缺 DLL 根本起不来，用户一无所见，也不知道为什么。
→ 修正：**GUI 初始化之前先开文件日志**（写到 exe 同目录，失败则退到 `X:\tmp`）；提供 `--console` 参数创建控制台窗口看错误；`selftest` 输出版本 + 已加载 DLL 列表。

**A7. 缺错误与降级策略。**
Phase 2 只有"超时、重试、错误透传"。单机抢修时断网 / 429 / 5xx / 模型返回非法 `tool_calls` 都会卡死。
→ 修正：离线明确提示 + 指数退避 + `tool_calls` 解析失败的兜底（把原始响应当文本回灌让模型自我纠正）+ 空响应保护。

**A8. "照 `tinker.c` 翻成 Go"被低估了。**
`os/exec` 拿不到 job 所需的一切。可用 `syscall.SysProcAttr.CreationFlags` 传创建标志 + `OpenProcess(pid)` 拿 handle 再 `AssignProcessToJobObject`，但**有竞态**（进程可能已退出）；稳妥做法是直接调 `CreateProcess` 自己拿 `PROCESS_INFORMATION`。
→ 修正：Phase 1 里给这项单列，按**约 150 行精细移植**估工，不按"翻一下"估。

### 🟡 次要（4 条）

| # | 问题 | 处置 |
|---|---|---|
| **A9** | 32 位 Go 的堆虚拟地址预留，极低内存 PE 上表现待验 | **需实测**，Phase 0 顺带看；备选 `GOGC` 调小 |
| **A10** | 建议"精读 `PE_SmartFixer` 源码"有 GPL 污染风险 | 收紧为：**只读它的 README 和界面截图了解架构，不逐行读源码、不做逐行翻译** |
| **A11** | provider 差异被低估（DeepSeek 的 `reasoning_content`、各家 `tool_calls` 字段不一致） | 在 `agent/llm.go` 里做**薄适配层**，按 provider 分支；不要假设"OpenAI 兼容"就是统一的 |
| **A12** | WinPE 有 72 小时强制重启；截图落盘会堆在内存盘 | 加截图文件定时清理（只留最近 N 张）+ 状态栏显示已运行时长 |

---

## 0.6 二轮审核修订（2026-09-11 · 独立复审）

又派了一个**全新 agent**（无前一轮上下文）从「明天就要动手写代码的工程师」视角走查，找出 12 条。以下是核实后的处置。**它同时确认了 §0.5 的 A1（`systemPool` 判定）和 A2（`CP_OEMCP → UTF-8`）技术核实成立。**

### 🔴 致命（3 条）

**B1. P0-2 的验证方法本身是错的 —— 静态导入表看不到 Go 的依赖。**
- **问题**：Go 用 `syscall.NewLazyDLL` **延迟加载** DLL，所以产物的静态导入表里**不会出现** user32 / gdi32 / psapi 等，`dumpbin /imports` 那套查不全。
- **后果**：Phase 0 会"验证通过"，到 Phase 3 才在 PE 里崩。
- **修正**：P0-2 改成 **运行期枚举** —— spike 里把每条代码路径都跑一遍，再用 `EnumProcessModules` / `CreateToolhelp32Snapshot(TH32CS_SNAPMODULE)` 列出**实际加载的模块**，回写到 PE 里比对。
- **补全要核对的 DLL 清单**：`crypt32`（TLS 校验）、`ws2_32` / `mswsock` / `iphlpapi`（网络、网卡信息）、**`psapi`（ps / kill）**、**`gdi32`（screenshot）**、`advapi32` / `ntdll`（sysinfo）、`cryptbase`（Go 运行期取随机数）。

**B2. 中止机制的最终兜底依赖一个可能不存在的 exe。**
- **问题**：A3 的三层兜底最后一层是 `taskkill /T /F`，但**最小 WinPE 3.x 可能根本没有 `taskkill.exe`**（需查证）；而 Win7 又建不了嵌套 job —— 两条路同时断 → **中止机制彻底失效**。
- **修正**：**不要依赖任何外部 exe**。自己实现：`CreateToolhelp32Snapshot(TH32CS_SNAPPROCESS)` 从 pid 出发按 `th32ParentProcessID` 递归收集整棵子进程树，逐个 `OpenProcess` + `TerminateProcess`。约 60 行，无任何外部依赖。
- P0-5 增加一项：把 `taskkill.exe` 是否存在也一并验（结论只影响"要不要保留一个额外兜底"，不影响主方案）。

**B3. A3 与 A8 关于「怎么把子进程放进 job」自相矛盾 —— 且审核建议的修法用不了。**
- **问题**：§0.5 里 A3 写 `CREATE_BREAKAWAY_FROM_JOB`，A8 又写 `OpenProcess(pid)` + `AssignProcessToJobObject`（**有竞态**，进程可能已退出）。两套做法并存。
- ⚠️ **审核建议的修法不可用**：它建议用 `PROC_THREAD_ATTRIBUTE_JOB_LIST` 原子入 job —— **微软文档明确写 "Supported in Windows 10 and newer and Windows Server 2016 and newer"**，**Win7 用不了**（已查证）。
- **定案做法（无竞态且 XP+ 全兼容）**：
  1. 自己调 `CreateProcess`，带 **`CREATE_SUSPENDED | CREATE_BREAKAWAY_FROM_JOB`**（后者失败则去掉重试）
  2. 拿到 `PROCESS_INFORMATION.hProcess` —— **句柄在手，就不存在竞态**
  3. `AssignProcessToJobObject(hJob, hProcess)`
  4. `ResumeThread(hThread)`
  5. job 建不起来时，降级走 B2 的自实现进程树终止

### 🟠 重要（5 条）

**B4. 多模态回传通道的接口没定义 —— 图片会被静默丢弃。**
- **问题**：`docs/05` §3 说了"图片不进 `tool_result`、改插 user 消息"，但没定义 **工具怎么把"我有图要附"这件事告诉 loop**。只按"返回 JSON 文本"实现的话，`read` 看图和 `screenshot` 都会静默失效。
- **修正**：**工具返回值统一带一个可选附件字段**：`{"ok":true,"data":{...},"attach_image":"X:\\tmp\\shot1.png"}`。loop 见到 `attach_image` 就按 docs/05 §3 的规则插一条带图的 user 消息。所有产图工具（`screenshot`、`read` 遇图片扩展名）都走这一个通道。

**B5. `run_script` 的编码没定 —— 中文批处理必失败。**
- **问题**：临时 `.bat` 若按 UTF-8 写，cmd 按 OEM 代码页解读 → 中文脚本乱码、甚至命令都认不出来。**这是 A2 的镜像问题**。
- **修正**：`read` 是 `OEM → UTF-8`，`run_script` 就要 `UTF-8 → OEM`（同一个转换函数的反向），或者**明确声明脚本只支持 ASCII** 并在工具描述里写死。

**B6. `owl.ini` 从未给出完整字段表，且与 `docs/04` 打架。**
- **问题**：Key 存哪有两套说法（`docs/04` 是 `keyfile = X:\tinker\key.txt`，PLAN 里又出现 `--key`），文件名还是 `tinker.ini`。实现者不知道按哪个来。
- **修正：定一份权威字段表（`owl.ini`，UTF-8 无 BOM）**

  | 段 | 键 | 默认 | 说明 |
  |---|---|---|---|
  | `[llm]` | `base` | — | 完整 chat completions URL |
  | | `model` | — | 模型名 |
  | | `keyfile` | `owl.key` | **优先**：Key 单独一个文件（同目录） |
  | | `key` | — | 兜底：直接写在这里（不推荐） |
  | | `vision` | `0` | 1=把 `screenshot` 发给模型 |
  | | `timeout` | `120` | 秒 |
  | `[agent]` | `confirm` | `1` | 1=危险操作要确认 |
  | | `whitelist` | 内置清单 | 逗号分隔 |
  | | `maxturns` | `10` | 工具循环上限 |
  | | `imghistory` | `2` | 保留最近几轮的图片 |
  | `[ui]` | `font` / `fontsize` | 空 / `12` | 空=系统默认字体 |

**B7. P0-4 的验收标准设计得不对，而且工程师无法独立自测。**
- **问题①**：原写"不捆 PEM 的对照组必须失败" —— 但**如果目标 PE 的根库较新（已经含 ISRG X1 等），对照组也会成功**，就没有对照信号了。
- **问题②**：P0-4 需要 PE 内有网 + 真实 API base + 有效 Key，这些要靠用户准备，工程师**没法独立跑**。
- **修正**：① 验收标准简化为「**捆绑 PEM 时必须成功**」，去掉"对照组必须失败"；对照组只作诊断输出（打印实际用的是哪条校验路径）。② 在 PLAN §9 里明确标注 **P0-4 需要用户先配好网络和 Key**。

**B8. 白名单是可以绕过的 —— 别把它当沙箱。**
- **问题**：`exec` 只校验首 token，`run_script` 可以直接跑任意批处理，`download` 是自己走 `net/http` 完全不受白名单约束。
- **修正**：文档里**如实写明它是"软护栏 + 审计"，不是安全边界**；真正拦住危险操作的是 `confirm` 交互确认。不要在文档里造成"有白名单就安全"的错觉。

### 🟡 次要（4 条）

| # | 问题 | 处置 |
|---|---|---|
| **B9** | `-H windowsgui` 子系统**没有控制台**，`--console` 参数不会自动生效 | 明确要调 `kernel32.AllocConsole()` + 重设 std 句柄；或干脆额外出一个不带 `-H windowsgui` 的调试版 exe |
| **B10** | `selftest` 是**聊天里才能触发的工具**，GUI 都起不来时根本进不去 | 版本 / 已加载 DLL 清单**必须写进启动早期文件日志**（A6 那条），`selftest` 只作在线补充 |
| **B11** | `--key` 明文在命令行上，`tasklist /v` 能看到 —— **`docs/04` 自己就警告过这件事** | 优先 `keyfile`，`--key` 降级为"应急且不推荐"并在帮助里写明风险 |
| **B12** | `assets/cacert.pem` 从哪来、怎么更新，从没写 | 写明来源 `https://curl.se/ca/cacert.pem`，**锁一个版本**并在文件头注明取用日期；更新策略写进 README |

### 本轮确认无误的两条（§0.5 的核实是对的）

- ✅ **A1**：Go 在 Windows 上确实走 `systemVerify()` + CryptoAPI，`systemPool` 判定成立 → **必须自带 CA bundle 设 `tls.Config.RootCAs`**
- ✅ **A2**：cmd.exe 输出确为 OEM 代码页，`CP_OEMCP → UTF-8` 转换方案正确

---

## 0.7 Phase 0 实测结果（2026-09-11 已完成本机部分）

> 完整报告见 **`docs/07-Phase0-验证报告.md`**。六项里五项在本机通过，一项（P0-5 的 job 路径）必须在真 PE 里定论。

### 🔴 最重要的发现：一个只在 32 位出现、不报错、又正好打在主战场上的真 bug

`job` spike 在 386 上 `SetInformationJobObject` 返回 `ERROR_BAD_LENGTH`（cbSize=108），amd64 上正常（144）。

**根因（用 `-diag` 实测）**：**Go 在 386 上把 `int64`/`uint64` 对齐到 4 字节，MSVC 默认对齐到 8。**

```
                     Go(386)   Go(amd64)   MSVC(x86)   MSVC(x64)
Alignof(int64)          4          8           8           8
BasicLimitInformation  44         64          48          64
JOBOBJECT_EXT_...     108        144         112         144
```

`JOBOBJECT_EXTENDED_LIMIT_INFORMATION` 以两个 `LARGE_INTEGER` 开头，于是 386 上整个结构体错位 4 字节。

**后果（为什么不报错却致命）**：job 对象本身建好了，只是 `KILL_ON_JOB_CLOSE` **从来没设上**。tinker 崩溃或被强杀时，**子进程和孙进程不会被带走，变成孤儿继续跑** —— 在 PE 里就是一堆僵死的 `diskpart` / `dism` 还锁着磁盘。32 位正是 Win7 PE 的主力架构。

**已修**：改用**手工构造字节缓冲 + 显式偏移**（架构无关）。修复后 amd64=144 / 386=112，两边都 `ret=1 KILL_ON_JOB_CLOSE armed`。

### ⚠️ 由此新增的项目硬规则

> **1. 手写 Win32 结构体，只要含 64 位成员（`LARGE_INTEGER` / `ULONGLONG` / `DWORD64`），就存在 386 对齐风险。**
> 处理方式：**手工构造字节缓冲 + 显式偏移**（推荐），或 build tag 分架构 + 编译期尺寸断言。
> **2. 所有带 `cbSize` 参数的 Win32 API（`Set*` / `Get*`），必须检查返回值**，不能假设结构体定义是对的。

已排查确认**无此问题**的结构体：`STARTUPINFOW`(68)、`PROCESS_INFORMATION`(16)、`PROCESSENTRY32W`(556)、`MODULEENTRY32W`(1064)、`MEMORYSTATUSEX`(72)、`RTL_OSVERSIONINFOW`(276)。**只有 `JOBOBJECT_EXTENDED_LIMIT_INFORMATION` 中招。**

### 其他关键实测结论

| 项 | 结论 |
|---|---|
| **P0-2 方法** | ✅ 验证成功，且**证实了 B1 的担心**：`LazyDLL` 延迟加载，静态导入表看不到任何东西。Win11 上枚举出 **46 个运行期模块**。**但这份清单不等于 Win7 的** —— `bcryptprimitives`、`win32u`、`gdi32full` 都是 Win10+ 才有的，必须在真 PE 里重新量 |
| **P0-3** | ✅ `syscall.NewCallback` 可用，纯 Go 建窗口 + 三区布局 + 消息循环 + 定时器全部正常 |
| **P0-4** | ✅ [4] 捆绑 bundle 的握手成功（`verifiedChains=2`）。**关键实证：`x509.SystemCertPool()` 返回 `0 subjects`** —— 从实测上确认 A1：Windows 上系统证书池只是惰性标记，证书不在 Go 这边 |
| **P0-5** | ⚠️ 本机 `IsProcessInJob` 返回 **1**（进程已在 job 里），`CREATE_BREAKAWAY_FROM_JOB` **被拒："Access is denied"** —— 但 Win11 的嵌套 job 救回来了。**Win7 没有嵌套 job，这步必然失败**，降级路径是必需的。**自实现杀树已验证可用**（杀 3 个进程含 conhost，0 错误，无残留） |
| **P0-6** | ✅ 386 启动后 VA 可用 **1372 MB**，在探针自己的安全余量刹车前**实际持有 1646 MB**。agent 实际只需几十 MB，不是风险。**⚠️ 但发现 OOM 是 `runtime.throw` 不是 `panic`** —— 见 §0.8 |

### Go 1.20 的 API 差异（版本锁定的现实代价）

写代码时撞到一个 **Go 1.21+ 才有** 的东西：`go -C <dir>`。另外 `tls.VersionName()` 也是 1.21+。都要自己绕。

> **修正（三轮审计 §0.8）**：这里原本还把 `tls.CipherSuiteName` 列进了禁用清单 —— **那是错的**，它 Go 1.14 就有了（`crypto/tls/cipher_suites.go:100`）。清单里只有 `go -C` 和 `tls.VersionName`。

**Phase 1 起 build 脚本要加 `go vet`，CI 只认 Go 1.20。**

### 产物尺寸（`-trimpath -ldflags "-s -w"`）

386：hello 1.46MB / gui 1.33MB / job 1.37MB / dlls 4.44MB / https 4.71MB
amd64：hello 1.51MB / gui 1.39MB / job 1.42MB / dlls 4.56MB / https 4.83MB
**推算正式版 owl.exe ≈ 6~8 MB（386）**，与 §5 估算吻合。

### 还需要在真 PE 里做的（我做不了）

P0-1~P0-6 的 PE 侧验证。详见 `docs/07` §5、`docs/08`。

> 附加项（`tasklist.exe` / `taskkill.exe` 是否存在）**已降级为"纯好奇"** —— 自实现杀树不依赖任何外部 exe。

---

## 0.8 三轮代码审计（2026-09-11 · 5 片并行 + 1 独立复核）

> 完整清单见 **`docs/07` §7**。**26 条发现：18 条成立、7 条部分成立、1 条误报。**
> 修掉 25 条，全部重新构建并回归验证通过。

**为什么值得做这一轮**：`spike/` 不只是"跑过就算"，**它会作为正式产品 Win32 绑定层的参考实现被复用**，带着 bug 留着会直接传播进产品。

### 最有价值的发现（都是"静默失效"类）

| 位置 | 问题 | 为什么危险 |
|---|---|---|
| `spike/gui` `wcs()` | `UTF16PtrFromString` 的 `*uint16` 转成 `uintptr` 返回后不再被 Go 引用，**GC 可在 Win32 调用期间回收它** | `CreateWindowExW` 那一行同一实参表里连调两次 `wcs()`，第二次分配就可能触发 GC → 类名读到野内存 → 窗口建不起来，而且**看起来像"PE 不支持 GUI"** |
| `spike/gui` `main()` | `runtime.LockOSThread()` 调用**晚于** `CreateWindowExW` | 建窗线程可能 ≠ 消息循环线程 → 窗口冻结、定时器不触发。**表面症状是"PE 里窗口卡死"，实际跟 PE 无关** |
| `spike/job` `killTreeSelfContained` | 单次静态快照 | 快照后派生的子孙**漏杀**；root PID 被复用会**误杀无关进程** |
| `spike/job` step[6] | `TerminateJobObject` 失败**无降级** | 中止机制在 job 内静默失效 |
| `spike/dlls` | 网络相关 DLL **只通过真实网络 I/O** 被加载 | 离线 PE 上清单**漏报** `ws2_32`/`crypt32`/`dnsapi` → 工程师误判"PE 里不需要这些" |

### 🔴 新增项目硬规则（第 3、4 条）

> **3. OOM 是 `runtime.throw`，不是 `panic`。** `recover()` **接不住**，`defer` 也不执行。
> → **必须主动限制输入上限**（`read` 单次读入量、会话内图片总量），不能指望出错时兜住。
> **4. 诊断程序的结论必须可证伪。** 如果测试目标选得会让"两种假设都通过"，那它就是在制造虚假安心 —— 比没有测试更糟。

### 被否掉的两条审核建议（已核实）

1. **不要用 `recover()` 兜 OOM** —— 实测推翻，见上。
2. **`tls.CipherSuiteName` 不是 1.21+** —— 去 Go 1.20 源码确认是 1.14 就有。

### 一个判断上的收获

审核把"杀树漏杀"标成致命，把 `wcs()` 的 GC 野指针和 `LockOSThread` 的位置标成次要。**实际情况正好相反** —— 后两者才是真正会让 PE 里的 GUI 直接坏掉的。

> **互操作层的 bug，危险程度跟"代码看起来多可疑"基本无关，只跟"失效是显式报错还是静默"有关。**

---

## 0.9 v1 第四轮独立审计 + v2 复审合并（2026-09-11 · spike 落地版）

> §0.7/§0.8 是 docs/07 的三轮审计结论。本节是**第四轮独立审计 + v2 全量复审**
> （`.tmp/audit_v2/`）的**增量**：每条都给出**当前 spike 源码里的实施位置**，
> Phase 1 移植到 `src/win/` 时**必须**把对应契约一并带走。
> 长期记忆 `.workbuddy/memory/MEMORY.md` §11-13 是更精炼的版本。

### 🔴 第 5~9 条项目硬规则（v1 第四轮 + v2 复审合并）

> **5. GC 野指针（v1 第四轮 M1 / v2 复审 §11）—— 当前 spike 实施：`spike/gui/main.go:142-150`**
> `syscall.UTF16PtrFromString` 返回的 `*uint16` 一旦转成 `uintptr` 交给 `syscall.Proc.Call`，
> GC **就再也看不到它**；从 `wcs()` 返回到 `.Call()` 真正陷入内核之间发生 GC，内存可能
> 被回收复用 —— 32 位 PE 命中概率远高于 64 位。
> **修法**：`runtime.KeepAlive` 或包级持有引用。**Phase 1 落地为 `src/win/wstr.go`：
> 必须 `[]*uint16` 长期持有 + **永远不要 `[:0]` 重置**（即使有"满了回收"的设计**也是定时炸弹**，
> 一旦在飞 Win32 Call 持有的指针被踢，重置 → append 之间 GC 触发，Win32 读野指针）。

> **6. 杀树 PID 复用（v1 第四轮 M2 / v2 复审 §13）—— 当前 spike 实施：`spike/job/main.go:302-385`**
> 自实现杀树时**每个节点**都做两层防护：
> (a) root 名字不符 → **整轮中止、不再 retry**（v2 修法，line 312-315 入口 + line 352-355 round 开头）
> (b) 非 root 节点 `OpenProcess` 后查 `InheritedFromUniqueProcessId`（v1 第四轮修法，line 375+）
> 两层并存，**不互相替代**。理由：v2 整轮中止只防 root 本身被复用；v1 第四轮 InheritedFromUniqueProcessId
> 校验防**子节点**被回收给同一父链下的无关新进程。少任何一层就有"静默误杀无辜进程"风险。
> **Phase 1 落地为 `src/win/job.go` + `src/win/proc.go`，两层都必须有**。

> **7. mem API 必须返回 error（v1 第四轮 L1）—— 当前 spike 实施：`spike/hello/main.go:83-89, 217-228`**
> `GlobalMemoryStatusEx` / `RtlGetVersion` 等"小结构体 + 必填 cbSize"API 调用**必须**返回
> `(value, error)`，调用方见 err 立即 return + 打 WARN，绝不能让零值结构体继续走探针逻辑
> —— 零值会被读为"地址空间 0 MB"，污染整条探针结论。
> **Phase 1 落地为 `src/win/sysinfo.go`：所有 sysinfo 函数统一签名为 `(T, error)`**。

> **8. VERDICT 必须看测试的每一步（v1 第四轮 L4）—— 当前 spike 实施：`spike/https/main.go:241-246`**
> 多步对照测试的 VERDICT **必须**把每一步的结果都纳入判据，不能只挑"重要的"几条。
> 不然 `https-get` 拿到 5xx 也会被报 PASS —— 用户看到 PASS 就以为整条 HTTPS 链都好。
> **Phase 1 落地为 `src/agent/verdict.go`（如有多步结果汇总需求）**。

> **9. UTF16FromString 显式判 err（v1 第四轮 L5）—— 当前 spike 实施：`spike/job/main.go:670`**
> `syscall.UTF16FromString` / `UTF16PtrFromString` 显式判 err，**不**用 `_` 吞掉。
> 命令字符串来自外部输入时尤其重要：含 NUL 字符串会让 `&cl2[0]` 在异常路径上
> 下标越界 panic，且 panic 发生在 spike 关键路径上，没有任何恢复机制。
> **Phase 1 落地为 `src/win/wstr.go`：所有 UTF-16 构造函数**必须**返回 `(*uint16, error)`，
> 不许有不暴露 err 的便利包装**。

### v2 复审补的"已修但已沉淀"清单（不再列为硬规则，只作 reference）

| 修复 | 位置 | 备注 |
|---|---|---|
| `spike/dlls` declaredDeps 从 11 扩到 16 | `spike/dlls/main.go:96-100` | 5 个标 `(E)`：`cryptbase/bcrypt/cryptsp/msasn1/rsaenh` |
| `spike/gui` `-secs 0` 仍自动关闭 | `spike/gui/main.go:189-193, 384-387` | 退出定时器只在 main() 创建 |
| `spike/https` `https-get` 状态码非 2xx 报 FAIL | `spike/https/main.go:210-218` | `resp.StatusCode/100 != 2` |
| `spike/job` 错误分支 `os.Exit` 显式关 hJob | `spike/job/main.go:531-562, 714-722` | 多处 `pCloseHandle.Call(hJob)` |
| `spike/dlls` `Module32NextW` 区分 `ERROR_NO_MORE_FILES` | `spike/dlls/main.go:106, 272-279` | 真出错时打 warning + 标"清单不完整" |
| `spike/gui` 窗口硬编码 (10,10) → 按屏幕居中 | `spike/gui/main.go`（窗口创建段） | docs/07 阶段已修 |
| `spike/gui` `GetMessageW=-1` FATAL + ExitCode=5 | `spike/gui/main.go:392-396` | docs/07 阶段已修 |
| `spike/https` CheckRedirect 拒绝 https→http 降级 | `spike/https/main.go:180-190` | docs/07 阶段已修 |
| `spike/https` `cl.Get` 错误分支关闭 resp.Body | `spike/https/main.go:197-199` | docs/07 阶段已修 |

### 未修但已记录（不阻塞 Phase 1）

- `src/tinker.c` `/ENTRY:WinMainCRTStartup` 引用未定义的符号（line 8 注释 + line 572 只定义 `WinMain`）—— tinker.c 是 C 参考实现，Phase 1 翻成 Go 时重写，**不**直接编译
- `src/tinker.c` 单条超长日志不裁剪 —— 翻 Go 时补硬上限
- `src/tinker.c` `PostMessageW` 返回值未检查 —— 翻 Go 时补
- `spike/job` step[7] 缺 breakaway 重试路径 —— 仅影响 PE 探针的 [7] 步骤，与产品路径无关

---

## 1. 已核实的关键事实（2026-09-11 实测）

| # | 事实 | 影响 |
|---|---|---|
| 1 | 本机 Go 是 **go1.26.4** | 🔴 **不能用它构建** —— 产物要求 Win10+。必须单独装 `Go 1.20.14` |
| 2 | `syscall.NewCallback` **在 Go 1.20 中存在**（`src/syscall/syscall_windows.go:180`，同文件 190 行还有 `NewCallbackCDecl`） | ✅ 不用 cgo 就能写 Win32 GUI，`CGO_ENABLED=0` 保持不变 |
| 3 | `windows/386` 与 `windows/amd64` 均已支持 | 可出双架构 |
| 4 | `syscall.NewLazyDLL` / `LazyProc` 在 Go 1.20 中存在 | 可薄封装 user32 / kernel32 / gdi32 |
| 5 | **回调数量有上限** —— `NewCallback` + `NewCallbackCDecl` 合计"至少能创建 1024 个" | 设计约束：**WndProc 只在启动时注册一次**，绝不放进循环 |
| 6 | go.dev 可正常访问 | 能下载 Go 1.20.14 |

---

## 2. 目录结构

```
PE-agent/
├─ PLAN.md                      ← 本文
├─ go.mod                       (go 1.20)
├─ build.cmd / build.sh         ← 锁死用 Go 1.20 构建，带版本断言
├─ docs/01..05                  调研 / 工具集 / GUI / 单机方案 / 视觉
├─ spike/                       Phase 0 的验证程序（**已通过三轮审计，留作回归测试**）
│  ├─ hello/main.go             P0-1 能不能跑 + P0-6 内存（`-alloc N`，带余量刹车）
│  ├─ dlls/main.go              P0-2 显式 LoadDLL 声明的依赖集（不依赖网络）+ 枚举运行期模块
│  ├─ gui/main.go               P0-3 纯 Go 建 Win32 窗口（`-secs N` 自动关）
│  ├─ https/main.go             P0-4 五步 TLS 对照（TCP / 跳过校验 / 系统库 / 捆绑 bundle / 完整 GET）
│  └─ job/main.go               P0-5 Job Object + 自实现杀树（`-diag` 打结构体布局）
├─ assets/
│  ├─ assets.go                 `//go:embed cacert.pem`
│  └─ cacert.pem                Mozilla CA bundle，121 张证书 / 189 KB
├─ src/
│  ├─ main.go                   入口 + 装配
│  ├─ win/                      Win32 绑定层（syscall 薄封装）
│  │  ├─ api.go                 user32 / kernel32 / gdi32 的 LazyProc
│  │  ├─ gui.go                 窗口 + 三区布局 + 消息循环
│  │  └─ dpi.go                 字体与屏幕尺寸
│  ├─ agent/
│  │  ├─ loop.go                agent loop：请求 → tool_calls → 执行 → 回填
│  │  ├─ history.go             会话历史 + **图片裁剪**（只留最近 2 轮）
│  │  ├─ prompt.go              系统提示词（目标 <1000 token）
│  │  └─ llm.go                 OpenAI 兼容 API 客户端
│  ├─ tools/
│  │  ├─ registry.go            工具注册表 + JSON Schema + 分发
│  │  ├─ exec.go                exec / run_script（Job Object 杀进程树）
│  │  ├─ file.go                read / write / edit / ls / find / hash
│  │  ├─ sys.go                 sysinfo / diskinfo / netinfo / ps / kill
│  │  ├─ net.go                 download
│  │  └─ vision.go              screenshot + 图片读取
│  ├─ cfg/ini.go                INI 解析（自己写，约 150 行）
│  └─ logx/log.go               日志 → PostMessage 投递到 UI 线程
└─ dist/                        构建产物
```

> `src/tinker.c` **保留**，但角色变了：不再是待实现的骨架，而是**Win32 实现的参考** —— `exec` 的管道捕获、Job Object 杀进程树、日志裁剪那几段可以照着翻成 Go。文件名待 §8 命名确定后一起改。

---

## 3. 分阶段计划

### Phase 0 — 技术预研（**最关键，必须最先做**）

> ✅ **本机部分已完成（2026-09-11）** —— 结果见 §0.7，修复后的代码见 §0.8。
> ⏳ **剩下的是 PE 侧实机验证** —— 清单见 `docs/08-PE测试清单.md`，那部分需要你来做。

先证明三个假设，否则后面全是白做。

| 编号 | 要验证什么 | 怎么验 | 通过标准 |
|---|---|---|---|
| **P0-1** | Go 1.20 编出的 exe **能在 Win7 PE 里起来** | 装 Go 1.20.14 → `spike/hello` 编 386 → 在 PE 里跑 | 打印出预期字符串 |
| **P0-2** | Go 产物**运行期实际加载了哪些 DLL**，WinPE 3.0 里是否都有 | ⚠️ **不能只看静态导入表** —— Go 用 `LazyDLL` 延迟加载，导入表看不到。`spike/dlls` 分两步：① **显式 `LoadDLL` 11 个声明的依赖**并打印 `ABSENT`（**不依赖网络**，所以离线 PE 上的结论也有效）② 跑遍代码路径后用 `EnumProcessModules` 枚举实际模块 | `DECLARED DEPENDENCY CHECK` 里 **`absent=0`**。清单：`crypt32`、`ws2_32`、`mswsock`、`dnsapi`、`iphlpapi`、**`psapi`**、**`gdi32`**、`advapi32`、`ntdll`、`user32`、`kernel32` |
| **P0-3** | **纯 Go 的 Win32 窗口能在 PE 里显示** | `spike/gui`（syscall + NewCallback + 消息循环） | 看到窗口，能拖动、能点到控件 |
| **P0-4** | **HTTPS 能真正握手成功** | `spike/https`，默认目标 `letsencrypt.org`（链末端 ISRG Root X1） | **只有一条门禁：`tls-bundle` 必须 OK。** `tls-system` 只是参考 —— 它成功**也不能**说明 PE 的根库没问题 |
| **P0-5** | **Job Object 能否建立 + 进程树终止兜底** | `spike/job` 检测 `IsProcessInJob`；试 `CREATE_SUSPENDED \| CREATE_BREAKAWAY_FROM_JOB` → `AssignProcessToJobObject` → `ResumeThread`；同时验自实现的递归杀树 | 至少一条路径能建成功；**且自实现的杀树必须可用**（`[7] still alive: 0`）。它**不依赖任何外部 exe** |
| **P0-6** | 32 位 Go 在 PE 的内存表现 | `spike/hello -alloc 256` | 不崩、且能给出干净的内存读数（`throw` 不是 `panic`，崩了就什么也拿不到） |

**产出**：`spike/` **五个**程序 + **`docs/07-Phase0-验证报告.md`** + `docs/08-PE测试清单.md`

**为什么必须放最前面**：P0-1 / P0-2 失败 → Go 路线整体作废，得退回 C；**P0-4 失败 → 整个"连公网 LLM"的假设不成立**。宁可花一天验，不要写两周代码才发现跑不起来。

**前置准备**：
- 下载安装 **Go 1.20.14**（`https://go.dev/dl/go1.20.14.windows-amd64.zip`，解压即用，不用装）
- 准备一个 **Win7 PE** 的可启动 U 盘或虚拟机镜像（推荐虚拟机，回滚快）
- PE 里要能跑 `cmd`（基础镜像就有）

---

### Phase 1 — 骨架打通（目标：`exec` 能跑）

| 任务 | 要点 |
|---|---|
| 项目骨架 | `go.mod`（`go 1.20`）、目录、`build.cmd` 带 `go version` 断言 |
| **启动早期日志**（审核 A6） | **GUI 初始化之前**先开文件日志（exe 同目录，失败退 `X:\tmp`）；加 `--console` 参数创建控制台窗口 |
| Win32 绑定层 | `syscall.NewLazyDLL` 封 user32 / kernel32 / gdi32；**`NewCallback` 只调一次** |
| 三区 GUI | 日志区（只读多行 EDIT）+ 输入区 + 状态栏，见 `docs/03` |
| 线程模型 | UI 线程 `runtime.LockOSThread()`；agent 跑在 goroutine；日志用 `PostMessage` 投递，字符串所有权归 UI 线程 |
| 中止机制 | Esc / 停止按钮 → 取消标志 + **Job Object 杀整棵进程树** |
| **Job Object 三层兜底**（审核 A3） | `IsProcessInJob` 检测 → 子进程带 `CREATE_BREAKAWAY_FROM_JOB` → 降级 `taskkill /T /F /PID`。**Win7 不支持嵌套 job，不处理就静默失效** |
| `exec` 工具 | 匿名管道捕获 stdout+stderr、超时、NUL 重定向 stdin。**按约 150 行精细移植估工**（不是"翻一下"）：`os/exec` 拿不到 job 所需的一切，要 `SysProcAttr.CreationFlags` + `OpenProcess(pid)` 或直接调 `CreateProcess` |
| **exec 输出编码**（审核 A2） | **必过函数**：捕获的字节走 `MultiByteToWideChar(CP_OEMCP)` → `WideCharToMultiByte(CP_UTF8)`。不做的话中文 PE 下输出全乱码 |
| 日志上限 | 60000 字符，超了砍前半截 |

**验收**：在 Win7 PE 里启动，输入 `ver`，窗口里看到输出，按 Esc 能中止一条 `ping -t`。

---

### Phase 2 — Agent loop（目标：能和模型对话并自动调工具）

| 任务 | 要点 |
|---|---|
| INI 配置 | `owl.ini`，按 UTF-8 读；含 `[llm]` / `[agent]` / `[ui]` 三段 |
| **CA bundle 与 TLS**（审核 A1） | `assets/cacert.pem` 用 `//go:embed` 嵌入 → `x509.NewCertPool()` + `AppendCertsFromPEM` → `tls.Config.RootCAs`。**不要用 `SystemCertPool()` + 追加**（仍会被判定为 system pool，走 CryptoAPI，追加的证书可能不被采用） |
| LLM 客户端 | `net/http` + `encoding/json`；超时、重试、错误透传 |
| **provider 适配层**（审核 A11） | **不要假设"OpenAI 兼容"就是统一的**：DeepSeek 走 `reasoning_content`、各家 `tool_calls` 字段不一 → 按 provider 分支的薄适配层 |
| 请求组装 | 消息数组 + 工具 JSON Schema（由 `tools/registry.go` 自动生成） |
| 响应解析 | 提取 `tool_calls`，分发到注册表，结果回填，循环到模型返回纯文本 |
| **错误与降级**（审核 A7） | 断网明确提示 + 指数退避 + **`tool_calls` 解析失败兜底**（把原始响应当文本回灌让模型自我纠正）+ 空响应保护 |
| 会话历史 | 滑动窗口 + 截断；**为 Phase 4 的图片裁剪预留结构** |
| 系统提示词 | 目标 <1000 token（对齐 Pi）；**不写各工具用法，靠 `help` 工具自查** |
| 工具循环上限 | 最多 N 轮（如 10），防止死循环 |

**验收**：在 PE 里输入「看看 C 盘还剩多少空间」，模型自动调 `diskinfo` 并给出回答。

---

### Phase 3 — 补齐 14 个工具

分三批，每批加完就在 PE 里验一次。

| 批次 | 工具 | 依赖 |
|---|---|---|
| **P3-1 文件类** | `read` / `write` / `edit` / `ls` / `find` / `hash` | `os` + 自写 MD5/SHA256 或 `crypto/*`（标准库） |
| **P3-2 系统类** | `sysinfo` / `diskinfo` / `netinfo` / `ps` / `kill` / `run_script` | `syscall` 调 `kernel32` / `iphlpapi` |
| **P3-3 网络类** | `download` | `net/http`（TLS 白送） |

**两个必须注意的点：**
1. **不要用 `os/user`** —— 它会拉进 `netapi32.dll` / `userenv.dll`，精简 PE 可能没有。改用 `GetUserNameW`（advapi32）。
2. `sysinfo` 的 OS 版本要调 `ntdll!RtlGetVersion`，**不要用 `GetVersionEx`**（Win8.1+ 无 manifest 会返回假版本号）。

**验收**：14 个工具在 PE 里逐个手测通过。

---

### Phase 4 — 视觉

| 任务 | 要点 |
|---|---|
| `screenshot` | `GetDC(NULL)` + `BitBlt` 抓屏 → 缩放 → `image/png` 编码 → 落盘 |
| `read` 图片分支 | `.png` / `.jpg` / `.bmp` → 返回图片内容块 |
| 插图协议 | **工具返回文本 + 紧随其后插一条带图的 user 消息**（Anthropic / OpenAI 都通） |
| 历史图片裁剪 | **只保留最近 2 轮的图**，更早替换成 `[截图已省略 路径]` |
| `vision` 开关 | `owl.ini` 里 `vision = 1/0`；为 0 时**把 `screenshot` 从工具列表摘掉** |

**验收**：在 PE 里制造一个错误对话框（或用一张现成的蓝屏截图），模型能读出里面的错误码。

---

### Phase 5 — 加固与分发

| 任务 | 要点 |
|---|---|
| 命令白名单 | 默认只放行 PE 自带工具；`diskpart clean` / `format` / `del /f /s` 单独确认 |
| 审批开关 | `owl.ini` 里 `confirm = 1/0` |
| **首次运行体验**（审核 A5） | PE 里**没有记事本**，"填好 Key"没法做到。三种方式：① GUI 首次运行弹密钥输入框（复用已有 EDIT 控件）② `--key` 命令行参数 ③ 在开发机上预置好 `owl.ini` 一起拷进 U 盘。**文档要写明 Key 明文落在 U 盘上的风险** |
| **内存盘清理**（审核 A12） | 截图会堆在 `X:` 内存盘上（默认 512MB）；只留最近 N 张，旧的自动删。状态栏显示已运行时长（WinPE 有 72 小时强制重启） |
| 分发形态 | `owl.exe` + `owl.ini` 两个文件；附 `winpeshl.ini` 示例（自动拉起） |
| 双架构 | 386（Win7 PE 主力）+ amd64（新 PE） |
| **构建后产物校验**（审核 A6） | build 后自动检查：依赖表里没有 `msvcrt` / `api-ms-win-crt-*`，且子系统为 GUI |
| 真机验收 | 在真 Win7 PE 和 Win10 PE 上各跑一遍完整流程 |

**验收**：U 盘插上 → 进 PE → 双击 → 填好 Key → 能对话能干活。

---

## 4. 风险清单

| # | 风险 | 概率 | 影响 | 缓解 |
|---|---|---|---|---|
| 1 | Go 1.20 产物在 WinPE 3.0 缺 DLL | 中 | 🔴 致命 | **Phase 0 的 P0-2 专门验这个**；必要时去掉 `net` 的 DNS 依赖（改用 IP 直连）；最坏退回 C |
| 2 | `os/user` 拉进 edge DLL | 中 | 🟠 高 | 明确禁用，改用 `GetUserNameW` |
| 3 | 回调数量上限 | 低 | 🟡 中 | WndProc 只注册一次；不动态创建回调 |
| 4 | Go runtime 在 PE 的内存/堆限制 | 中 | 🟡 中 | 限制历史长度；必要时设 `GOMAXPROCS=1` |
| 5 | PE 的 `X:` 是内存盘（默认 512MB scratch） | 中 | 🟡 中 | 截图只留最近几张，及时删旧图；日志上限 |
| 6 | Go GUI 在 PE 里字体/尺寸异常 | 中 | 🟡 中 | 用 `DEFAULT_GUI_FONT` + 固定字号，INI 可覆盖；窗口自适应小屏 |
| 7 | 系统提示词太长吃掉上下文 | 中 | 🟡 中 | 目标 <1000 token，工具说明靠 `help` 自查 |
| 8 | 模型滥用截图烧 token | 高 | 🟡 中 | 提示词明确写「能用文本拿到的信息不要截图」+ 历史图片裁剪 |

| 9 | **TLS 信任库**（审核 A1） | 高 | 🔴 致命 | 捆绑 Mozilla CA bundle 用 `//go:embed` 嵌入，赋给 `tls.Config.RootCAs`；P0-4 实跑验证 |
| 10 | **Job Object 建不起来**（审核 A3） | 中 | 🔴 致命 | 三层兜底：`IsProcessInJob` 检测 → `CREATE_BREAKAWAY_FROM_JOB` → `taskkill /T /F` |
| 11 | **exec 中文输出乱码**（审核 A2） | 高 | 🟠 高 | 统一走 `CP_OEMCP → UTF-8` 转换函数 |
| 12 | 密钥没法注入（PE 无记事本）（审核 A5） | 高 | 🟠 高 | GUI 首次运行输入框 + `--key` 参数 + 预置 INI 三种方式 |
| 13 | GUI 起不来时无从排查（审核 A6） | 中 | 🟠 高 | GUI 初始化前先开文件日志 + `--console` 参数 |
| 14 | provider 差异导致解析失败（审核 A11） | 高 | 🟡 中 | `llm.go` 做 provider 适配层，不假设"OpenAI 兼容"统一 |

**已消除的风险（不用再管）：**
- ✅ CRT 依赖 —— Go 静态链接解决
- ✅ TLS 的**密码学部分** —— Go 自带 `crypto/tls`，不用打包 curl
- ✅ PNG 编码 —— Go 标准库 `image/png`
- ✅ JSON —— Go 标准库 `encoding/json`
- ⚠️ ~~TLS 整件事~~ —— **不成立，见风险 9**：密码学白送，但**信任库要走 CryptoAPI / 系统证书库，必须自带 CA bundle 兜底**

---

## 5. 体积与形态权衡（诚实说明）

| 方案 | 产物 | 体积 | 文件数 |
|---|---|---|---|
| 原 C 方案 | `tinker.exe` + `curl.exe` + `cacert.pem` + `ini` | ~1.8 MB | 4 个 |
| **Go 方案** | `owl.exe`（+ `owl.ini`） | **约 7~10 MB** | **1~2 个** |

**Go 版字节数大了 4~5 倍，但换来的是：**
- 一个文件搞定，不用管 curl 版本 / CA 证书 / DLL 依赖
- TLS + JSON + PNG 全部白送，少了约 1500~2000 行手写代码
- 开发效率高一个数量级（标准库直接可用）

如果最终在意体积，加一步 `upx -9 owl.exe` 能压到 **约 3MB**。对 U 盘和 PE 镜像来说都不是问题（一套 PE 镜像本身 300MB~1GB）。

---

## 6. 构建命令（锁死 Go 1.20）

```bat
@echo off
set GOROOT=C:\go1.20.14
set PATH=%GOROOT%\bin;%PATH%

rem 版本断言：绝不能用本机的 go1.26 构建（产物要求 Win10+）
go version | findstr /C:"go1.20" >nul || (echo [FATAL] need Go 1.20 & exit /b 1)

set CGO_ENABLED=0
set GOOS=windows
cd /d %~dp0src

set GOARCH=386
go build -trimpath -ldflags "-s -w -H windowsgui" -o ..\dist\owl.exe .
if errorlevel 1 exit /b 1

set GOARCH=amd64
go build -trimpath -ldflags "-s -w -H windowsgui" -o ..\dist\owl64.exe .
if errorlevel 1 exit /b 1

echo [OK] dist\owl.exe (386) + dist\owl64.exe (amd64)
```

三个不能省的开关：
- `CGO_ENABLED=0` —— 纯静态，绝不引外部 DLL
- `-H windowsgui` —— 去掉控制台窗口（否则 GUI 程序会带一个黑框）
- `-s -w` —— 去符号表和调试信息，体积减约 30%

---

## 7. 里程碑

| 阶段 | 交付物 | 验收标志 |
|---|---|---|
| **M0** | `docs/07-兼容性验证报告.md` + `spike/` | **PE 里看到窗口** ← 最关键的一关 |
| **M1** | 可运行的 GUI + `exec` | PE 里执行 `ver` 有输出，Esc 能中止 |
| **M2** | Agent loop | PE 里对话 + 自动调工具 |
| **M3** | 14 个工具 | 工具全部在 PE 里手测通过 |
| **M4** | 视觉 | 模型能读出截图里的错误码 |
| **M5** | 分发版 | U 盘插上就能用 |

---

## 8. 待确认

**命名**：暂按 **`owl`** 推进（`owl.exe` / `owl.ini` / 窗口标题 `owl - PE agent` / 日志前缀 `ai > you > -> <- !!` / 中文昵称「夜枭」）。

如果你更倾向 **`smith`**（铁匠，无撞名，语义最贴"修机器"）或别的名字，说一声即可 —— 命名集中在少数几处，改名成本几乎为零，**不阻塞开发**。

---

## 9. 下一步动作

### 已完成（2026-09-11）

1. ✅ Go 1.20.14 已装好：`C:\Users\wrz20\.workbuddy\binaries\go\versions\1.20.14`
2. ✅ Phase 0 的**五个** spike 全部写完（`hello` / `dlls` / `gui` / `https` / `job`）
3. ✅ 本机双架构构建 + 跑通，`go vet ./...` 干净
4. ✅ 三轮代码审计（26 条发现），修掉 25 条并回归验证
5. ✅ 产物已重建到 `dist/spike386/` 和 `dist/spike64/`

### 需要你做的（就这两件）

1. **准备 Win7 PE 测试环境**（虚拟机优先，回滚快）
2. **按 `docs/08-PE测试清单.md` 跑一遍**，把输出贴回来 —— 尤其是 `dlls.exe` 的 `absent=` 和 `job.exe` 的 `assign=`

> **要不要 API Key？** 这一轮**不需要**。P0-4 只验证 TLS 握手能不能成，打的是公开站点，不碰 LLM API。Key 是 Phase 2 的事。

### 我接下来可以做的（不依赖 PE）

**Phase 1 骨架**：`src/win` 绑定层 + 三区 GUI + 线程模型 + `exec`。这部分**不依赖 PE 测试结果**，现在就能开始。

说一句"开始 Phase 1"我就动手。
