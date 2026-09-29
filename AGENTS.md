# AGENTS.md

> 给 AI 编程 agent（OpenCode / Codex / Cursor / Aider / Devin / Gemini CLI 等）
> 阅读的项目工作约定。本文件由 `init` skill 生成结构，人工维护内容。

---

## 项目一句话

**PE-agent**（代号 `smith` / 铁匠）—— 一个**单 exe、纯 Go、32 位为主**的 Windows PE 应急助手。GUI 三区 + 内置 17 工具 + 多 LLM provider（Anthropic / OpenAI / DeepSeek），跑在精简的 Win7/Win10/Win11 PE 镜像里，不依赖系统根证书库。

主要交付：`dist/smith.exe`（386）+ `dist/smith64.exe`（amd64），各约 5.5~6 MB。

> **能力现状（权威）**：见 [`docs/12` §十一](docs/12-收尾与功能补齐计划.md)。**不要在本文件复述工具数量或接线状态** —— 历史上同一事实被复述 16 处、每次改代码要同步 16 个地方，已漂移过一轮。`run_script` 与 `exec` 均已走 Job Object 杀树。`win.IsProcessInJob` 是诚实降级，生产无调用点。`spike/` 只读且**不受 C1 门禁保护**。`win/` 门禁是 AST 真 diff，非 Win32 常量在 `constsExempt` 显式豁免。`build.cmd` 硬 grep 见 §2（不 grep `tls.VersionName`/`os/user`，1.20 下编译即失败）。`M1` 符号是 `wstrKeep`/`wstrKeepSlices`；`M2` 两层都在 `src/win/proc.go`。

---

## 必读

| 顺序 | 路径 | 用途 |
|---|---|---|
| 1 | [`PLAN.md`](./PLAN.md) | 设计源头：决策、风险、字段表、阶段计划 |
| 2 | [`docs/11-审计整改计划.md`](./docs/11-审计整改计划.md) | **整改批次 S0~S8 的定义 + 实施记录** |
| 3 | [`docs/12-收尾与功能补齐计划.md`](./docs/12-收尾与功能补齐计划.md) | **收尾批次 T0~T5 的定义 + 实施记录**（T0–T4 已落地） |
| 4 | `docs/01-05,07-10` | 各专项设计（06 编号空缺） |
| 5 | `spike/` | 技术预研产物（**只读，9 个探针 + 1 个 launcher = 10 个 `package main`**，可读不可 import） |
| 6 | `src/` | **本项目唯一可改的代码区** |

**改代码前先查 PLAN.md §0.9**（v1 第四轮 + v2 复审合并的硬规则：PLAN 自己的编号是第 **5~9** 条，即本文 §3 表的前 5 行 M1/M2/L1/L4/L5）+ 本文 §3 硬规则表（**共 10 条**：M1/M2/L1/L4/L5 来自 PLAN §0.9，B2/V1/S1/C1/J1 是 Phase 1 之后踩坑补上的）。`src/win/` 是最容易踩雷的（Win32 ABI + 386 结构体对齐）。

---

## 工作约定

### 1. 模块路径

`go.mod` 里 `module peagent` + `go 1.20`。**所有内部 import 必须是 `peagent/src/...`**，不是 `peagent/...`：

```go
import "peagent/src/win"        // 正确
import "peagent/win"            // 错误：会找不到
```

### 2. Go 版本：1.20.14 锁死

- toolchain 位置：`C:\Users\wrz20\.workbuddy\binaries\go\versions\1.20.14\`
- **不能用 1.21+ 的 stdlib / 新 API**（`go build` 会过、`go vet` 报 ban 列表或直接 1.20 编不过）
- 禁用清单（`build.cmd` 启动时硬 grep，**只有这 3 条 Win32 规则**）：
  - `GetTickCount64(` / `pGetTickCount64.Call`（Win7 PE 缺，会运行时炸）
  - `GetVersionExA(`（Win8.1+ 无 manifest 返假值）
  - `RegGetValueA(`（Win7 注册表 API 不全）

> **为什么不 grep 1.21+ 的 stdlib**：`tls.VersionName` / `os/user` / `slices` / `maps` /
> `log/slog` / 内建 `min`·`max`·`clear` 在 Go 1.20 上**编译即失败**，`go build` 自己会红。
> `build.cmd` 的 findstr **区分不了注释和代码** —— 曾加 `max[ ]*(` 后命中
> `src/agent/llm.go:195` 注释里的 `max(timeoutS, ...)`，导致 `build.cmd` 每次 `exit /b 1`。
> 所以门禁只保留"**1.20 能编过、但 Win7 PE 上运行才炸**"的那 3 条 Win32 API。
>
> ⚠️ 这条推理对 `os/user` **并不完全成立**：`os/user` 从 Go 1.0 就在，1.20 上**编得过**
> （真禁用理由是它拉 `netapi32.dll` / `userenv.dll`，PE 镜像里缺）。但那个理由属于
> 导入表范畴，而 `verify-pe.ps1` 只查 `msvcrt` / `api-ms-win-crt-*` / `ucrt*` / `vcruntime`，
> **不查 `netapi32`** —— 所以 `os/user` 目前没有自动化门禁，靠本节这条人工规则兜。
> 真要自动化，正确位置是 `verify-pe.ps1` 的导入表白名单，不是 findstr。

- `ticker.NewTicker` 1.20 有；`time.AfterFunc` 1.20 有 — 别用 1.21+ 才加的

### 3. 项目硬规则（PLAN §0.9 v1 第四轮 + v2 复审）

| ID | 规则 | 落地位置 |
|---|---|---|
| **M1** | `wstrKeep []*uint16` / `wstrKeepSlices [][]uint16` 持有 `*uint16` / `[]uint16`，**永不 `[:0]` 重置**；PostMessage 后必须 `win.KeepAlive(lp)` | `src/win/wstr.go`（**注意实际符号是 `wstrKeep` / `wstrKeepSlices`，不是 `strKeep`**） |
| **M2** | PID 复用双层防护：root 名字不符 → 整轮中止；非 root 节点查 `InheritedFromUniqueProcessId` | **两层全在 `src/win/proc.go` 的 `KillTreeSelfContained` / `treeOf`**。`src/win/job.go` **只是 Job Object API 的封装**（Create/SetKillOnClose/Assign/Terminate/IsProcessInJob），不含防护逻辑 |
| **L1** | 所有 API 返 `(T, error)`；不返裸 T（除 `void` 等价物） | 全包 |
| **L4** | 多步测试的 VERDICT 必须把**每一步**都纳入判据 | `src/agent/verdict.go` |
| **L5** | 不吞错：err 一律透传到底层 caller；可加 `fmt.Errorf("ctx: %w", err)` 链 | 全包 |
| **B2** | `runtime.LockOSThread()` 必须是线程入口函数**第一行**（含 keydialog 的子消息循环） | 全 `win/`。**两处**：`win.Run()`（`gui.go`，主消息循环）+ `win.PromptAPIKey()`（`keydialog.go`，首次运行密钥对话框）。后者跑在 `win.Run()` **之前**（`main.go` boot 第 [3.5] 步）且自带一个 `CreateWindowExW` + `GetMessage` 子循环，**它是本进程的第一个消息循环** —— 漏了就锁错线程，窗口冻结 |
| **V1** | **`win/` 的 `go vet` 必须用 `-unsafeptr=false`**，否则 Win32 互操作必需的 `uintptr` ↔ `unsafe.Pointer` 互转会刷屏告警 | `build.cmd` 收尾门禁 |
| **S1** | **含 64 位成员的手写 Win32 结构体有 386 对齐风险** → 用**手工构造字节缓冲 + 显式偏移**，不靠 `unsafe.Sizeof` 猜 | `src/win/job.go` `buildJobExtLimitInfo`（386=112 / amd64=144，`LimitFlags` 恒在偏移 16）。**这条铁律的来源：386 上写 108 字节时 `SetInformationJobObject` 返 `ERROR_BAD_LENGTH` 但不抛错，`KILL_ON_JOB_CLOSE` 静默失效** |
| **C1** | **Win32 常量必须对照 SDK 头文件 + 实测验证**，禁止凭记忆写 | `src/win/consts_test.go` + `consts_scan_test.go`（断言表 × **AST 扫描全包 const 定义**做真 diff；非 Win32 的项目内部常量在 `constsExempt` 显式豁免）。**真实踩坑：`WM_TIMER` 曾写成 `0x0118`（那是 `WM_SWITCHWINDOW`）** |
| **J1** | **Job Object 装配顺序不可换**：`CreateProcess(SUSPENDED)` → `CreateJobObject` → `SetKillOnJobClose` → `AssignProcessToJobObject` → `ResumeThread` | `src/win/jobexec.go`（`StartJobCmd`）。**先 Resume 再 Assign 会留一个窗口期**：子进程已经跑起来了却还没进 job，这段时间里 Stop 杀不掉它，而它可能已经 fork 出孙进程。`ResumeThread` 成功后**必须立刻关 `hThread`**。另：关 `hJob` 会因 `KILL_ON_JOB_CLOSE` **连带杀掉整棵树**，所以"启动中途失败"的路径不能无脑 `defer Close()` |

**改代码前问自己：会不会破坏这 10 条？**（M1 / M2 / L1 / L4 / L5 来自 PLAN §0.9；B2 / V1 / S1 / C1 是 Phase 1 之后踩坑补上的；J1 是 T2 接线时补的）

> ⚠️ **C1 门禁的边界（容易误以为"全覆盖"）**：
> `consts_test.go` 的门禁**只覆盖 `win/` 包**。
> `tools/` 包自己也有 Win32 常量 —— `DRIVE_*`（0–6，对照 `winbase.h`）与
> `PROCESSOR_ARCHITECTURE_*`（0/5/6/9/12，对照 `winnt.h`）**故意放在
> `tools/sysinfo.go`**，由 `tools/sysinfo_test.go` 单独断言（含
> `driveTypeMaxLen` 覆盖度自检），**不在 `win/consts_test.go` 的断言表里**。
> 改 `tools/` 里的 Win32 常量要去 `tools/sysinfo_test.go` 找门禁，别在 `win/` 里找。
>
> ⚠️ **C1 门禁的第三块边界**：`spike/` 包的 Win32 常量**不受任何门禁保护**
> （`consts_test.go` 的 AST 扫描只扫 `src/win/`，`build.cmd` 的禁用 grep 也只覆盖 `src/`）。
> `spike/` 是 Phase 0 的**只读预研**，定位是"拷 U 盘进 PE 做一次性验证"，
> 不是持续维护的产品代码 —— 代价是**错常量会长期潜伏**（`docs/13` A4 抓到过
> `spike/richedit` 的 `emExLimitText=0x0437` 其实是 `EM_EXSETSEL`、
> `emSetLimitText=0x00D5` 其实是 `EM_GETLIMITTEXT`（在**读**上限不是**设**上限），
> 以及 `spike/hello` 的 `archARM=12` 其实是 `PROCESSOR_ARCHITECTURE_ARM64` 的值）。
> **改 `spike/` 里的 Win32 常量时必须对照 SDK 头文件**，且改完要手工重建对应
> `dist/spike{386,64}/*.exe`（`build.cmd` 不构建 spike，见「已知陷阱 5」）。

### 4. 386 vs amd64

- **386 是主战场**（Win7 PE 主力 32 位）
- 不带 CGO（PE 镜像里没 C 编译器）
- 不带 `-race`（race detector 386 + 无 CGO 不可用）
- `unsafe.Sizeof(struct{})` 在两架构下**必然不同**（108 vs 112 这种）。**含 64 位成员时按 S1 处理：手工字节缓冲 + 显式偏移**
- Win32 互操作遵循 spike/* 的**只读探针**（**9 个探针程序** + `spike/hta/launcher` 这个 1 个 launcher，合计 10 个 `package main`）：`spike/job` (Job+进程快照+杀树) / `spike/gui` (LockOSThread+窗口) / `spike/hello` (提交限制自检) / `spike/https` (CA bundle) / `spike/dlls` (DLL 依赖) / `spike/hta` (HTA 前端) / `spike/oem2utf8` (编码转换) / `spike/richedit` (RichEdit 控件) / `spike/screenshot` (抓屏)；`spike/hta/launcher` 用 `CreateProcessW` 拉起 `mshta` 绕开沙箱的命令行 LOLBin 检测。**注意：`launcher` 不进 `dist/spike{386,64}`，那两目录仍是 9 个 exe。**
- **新加 Win32 常量必须同时在 `src/win/consts_test.go` 加断言**（有 AST 真 diff 门禁：包内 `const` 定义与断言表机械比对，漏了会红）。⚠️ **但这只对 `win/` 包成立** —— `tools/` 包的 Win32 常量门禁在 `tools/sysinfo_test.go`（见 §3 顶部注）
- `runtime.LockOSThread()` **必须是**线程入口函数第一行（晚于 `CreateWindowExW` 会让消息循环线程 ≠ 建窗线程 → 窗口冻结）。**`win.Run()` 和 `win.PromptAPIKey()` 两处都要**，后者跑在前者之前（B2）

### 5. 步间审核（当前工作模式）

每次提交前必须：

1. `go vet -unsafeptr=false ./src/win/...` + `go vet ./src/{agent,cfg,logx,tools,test}/...`
2. `go test -count=1 ./src/...`（386 必跑，amd64 跑一次确认）
3. `go build` 双架构产物（如改 win/ 或 main.go）
4. **或直接 `build.cmd test`** —— 它把上面三步 + gofmt 门禁 + `verify-pe.ps1` PE 校验都串起来了（顺序：`vet → gofmt → build → PE 校验 → test`）
5. commit message 格式：`P{阶段}-N: <area> (<contracts>)` —— 例：`P1-9b: tools (read/write/net/ps) + 11 tests (L1)` / `P3-7: build.cmd 产物门禁 (L1+L5)`
   - ⚠️ **不要加 `[xxx]` 前缀**：历史上出现过 `[shared] P3-1: ...` / `[hta] P0-1: ...`，按 `P` 前缀解析阶段序列会漏掉这两步（docs/11 §S8-11）

**win/ 包的 vet 用 `-unsafeptr=false`**：`uintptr` ↔ `unsafe.Pointer` 互转是 Win32 互操作必须的（`lparam`/`wparam` 是 `uintptr`，要用 `KeepAlive` 配套保护）。`win.KeepAlive()` 已封装，调用方只需 `KeepAlive(p)`。

### 6. 长文件路径（中文 path）

项目根 `F:\AI\01_项目\PE-agent\` 含中文。Go 工具链 OK，**但**：

- `git add path/` 加 trailing slash 在 Windows 上可能报 "fatal: bad config" —— 去掉 trailing slash
- `Remove-Item` 被沙箱拦截（"Local hard safety policy"）—— 用 `mavis-trash` 或挪到 `.tmp/_archive/`
- PowerShell `&&` 是非法分隔符 —— 用 `;` 或单行
- `go env GOOS=windows GOARCH=386` 设环境变量后 `go test` 一次过；不要写进 `~/.bashrc`

### 7. dist/ 入仓但 .tmp/ 不入仓

`.gitignore` 规则：

- `*.exe` 排除所有 exe
- `!dist/smith.exe` / `!dist/smith64.exe` 重新放行（用户拷 U 盘用）
- **spike 产物是逐个枚举放行，不是整目录**：`dist/spike{386,64}/*.exe` 先忽略、
  再对 9 个已登记的 spike 逐个 `!` 放行。原因（commit `4581a9d` / docs/11 §S6-7）：
  整目录放行会让 `build.cmd` 新增 spike 时静默把新二进制纳入跟踪，产生没人
  review 的二进制 diff。**新增 spike 时必须同步在 `.gitignore` 里登记**。
  - ⚠️ **真实后果是"悄悄丢"而不是"悄悄吞"**：未登记的 spike 产物被
    `.gitignore` 静默丢弃 —— `git status` **看不到**它、`git add .` **不会**
    加它、显式 `git add` 会被拒（"path is ignored"）。结果是你以为"拷 U 盘
    的东西在版本控制里"，实际 clone 出来的仓库**缺这些验证产物**。
  - ⚠️ **顺序约束（改这块时最容易踩）**：兜底忽略规则**必须**写在 `!` 枚举
    **之前**。git 是"后匹配覆盖先匹配"，放反了会让整块白名单作废
    （复审实测：把 `dist/spike386/*.exe` 挪到 `!dist/spike386/hello.exe` 之后，
    hello.exe 立刻变成 IGNORED）。
- `.tmp/` / `.workbuddy/` 完全排除（Mavis runtime + 工作缓存）
- `dist/smith.key` / `dist/smith.ini` / `dist/*.log` / `dist/*.session.log` 也排除
  —— dist/ 是"拷 U 盘交付"的目录，但运行时会在自己旁边写这些文件，
  `smith.key` 是真实 API key，`smith.session.log` 是真实 LLM 对话（隐私同级）

**新建可执行产物时**：要么改 `.gitignore` 放行，要么不 commit（看是不是用户要拷 U 盘的产物）。

---

## 目录结构

```
F:\AI\01_项目\PE-agent\
├── PLAN.md                 # 设计源头（必读）
├── AGENTS.md               # 本文件
├── README.md               # GitHub 入口
├── CHANGELOG.md            # 变更日志
├── LICENSE                 # MIT（README 徽章 + 许可节指向它）
├── go.mod                  # module peagent, go 1.20
├── build.cmd               # 一键 vet + gofmt + build + PE 校验 + test（默认/clean/test）
├── verify-pe.ps1           # PE 头校验（Subsystem / 导入表 / 体积）
├── run.ps1                 # 开发用启动器（锁 Go 1.20.14，编到 .tmp/ 再起 GUI / -NoGui 烟雾测试）
├── smith.ini.example       # 配置文件模板（字段表见 PLAN §0.6 B6）
├── .gitattributes          # 行尾强制：*.cmd/*.bat/*.ps1 = CRLF（陷阱 6 的 git 侧兜底）
├── .gitignore              # dist/ 逐个放行 + 隐私/临时文件排除（细则见 §7）
├── docs/                   # 12 篇专项设计（06 编号空缺；11/12/13 = 整改计划）
│   ├── 01-05, 07-10        #   各专项设计（逐篇文件名见 README 的同名树）
│   ├── 11-审计整改计划.md    #   S0~S8 的整改前后对照与实施记录
│   ├── 12-收尾与功能补齐计划.md #   T0~T5 的批次定义与实施记录（T0–T4 已落地）
│   └── 13-死代码与过时规则整改计划.md # A~F 批：死代码清理 + 过时断言订正
├── spike/                  # Phase 0 预研 + 前端探针（只读，9 个探针 + 1 个 launcher = 10 个 main）
│   ├── job/                #   Job Object + 进程快照 + 杀树 + 386 字节缓冲
│   ├── gui/                #   Win32 窗口 + LockOSThread + UTF-16 持引用
│   ├── hello/              #   提交限制自检（输出全部 ASCII）
│   ├── https/              #   嵌入 CA bundle + TLS 1.2 + 4 步对照
│   ├── dlls/               #   可加载 DLL 清单
│   ├── hta/                #   HTA 前端探针（app.hta = 运行时模板，读 exe 同目录）
│   │   └── launcher/       #     CreateProcessW 拉 mshta（不进 dist/spike{386,64}）
│   ├── oem2utf8/           #   OEM → UTF-8 转换探针
│   ├── richedit/           #   RichEdit 控件探针
│   └── screenshot/         #   抓屏探针
├── assets/                 # //go:embed 静态资源
│   ├── assets.go           #   var CACertPEM []byte
│   └── cacert.pem          #   Mozilla CA bundle（更新见 assets.go 注释）
├── src/                    # 唯一可改区
│   ├── main.go             #   入口：boot 序列（早期日志→cfg→LLM→worker→GUI）
│   ├── tinker.c            #   C 骨架参考（不编进 exe，docs/03 里引用设计）
│   ├── win/                #   Win32 互操作（最易踩雷）：api_kernel / api_user_gdi / wstr /
│   │                       #   gui / keydialog / msgs / job / jobexec / proc / sysinfo / oem /
│   │                       #   msgbox + consts_test/consts_scan_test（C1 门禁那两个）
│   ├── agent/              #   LLM 客户端 + 适配层 + loop + history + verdict
│   ├── tools/              #   17 工具注册表 + 业务实现 + limited_writer（输出硬上限）
│   ├── cfg/                #   INI 解析
│   ├── logx/               #   日志 + PostMessage 投递
│   └── test/               #   集成测试（e2e + smoke_bin）
└── dist/                   # 产物（部分入仓）
    ├── smith.exe             #   Phase 1 主产物（386，约 5.5~6 MB）
    ├── smith64.exe           #   Phase 1 主产物（amd64，约 5.5~6 MB）
    ├── spike386/            #   9 个 spike 386 exe + app.hta（拷 U 盘验 PE）
    └── spike64/             #   同上 amd64（9 个 exe + app.hta；launcher 不构建）
```

> 产物精确体积由 `build.cmd` 收尾的 `verify-pe.ps1` 实测输出，**不要在文档里手写字节数**。

---

## 改代码前的 checklist

- [ ] 看了 PLAN.md 相关章节
- [ ] 看了 docs/ 相关专项（尤其 **docs/11** 的对应批次）
- [ ] 看了对应 spike 程序的对应行
- [ ] 改动没破坏 **10 条硬规则**（M1/M2/L1/L4/L5/B2/V1/S1/C1/J1，见 §3）
- [ ] 没引入 1.21+ 的 stdlib API（grep 一下）
- [ ] 没引入 `GetTickCount64` / `GetVersionExA` / `RegGetValueA`
- [ ] 没引入 `os/user`（拉 netapi32.dll）
- [ ] 新增工具：`tools/` + 在 `tools/tools_test.go` 的 `allToolNames` 加名字 + `meta.go` 的 `expectRegisteredTools` 加 1 + **重命名 `src/test/e2e_test.go` 的 `TestE2E_All17ToolsRegistered`**（共 3 处）
- [ ] 新增 Win32 proc：`win/api_*.go` 声明 + 386 字节缓冲（如有 struct，按 S1 手工构造）
- [ ] **新增 Win32 常量：`win/` 包 → `win/consts_test.go` 加断言**（AST 真 diff 门禁会自动红；非 Win32 的项目内部常量需在 `constsExempt` 补一行）；**`tools/` 包 → `tools/sysinfo_test.go`**（不在 `win/` 门禁范围内）
- [ ] 386 + amd64 双 build + test 都过
- [ ] **`build.cmd` 仍是 CRLF**（LF 会让构建永久挂死，见「已知陷阱」6）
- [ ] **`git archive HEAD` 干净检出能 `go build ./src/...`**（新增文件漏 add 的头号症状，见「已知陷阱」7）

---

## 跑测试

```bash
# 全部测试（双架构）
build.cmd test

# 单包
$env:GOARCH=386
go test -count=1 ./src/agent/...

# 看 VERDICT 多步测试
go test -v -run TestE2E ./src/test/...
```

**注意**：`gui.exe` / spike 里真开窗口的程序需要 desktop，**无头环境**（CI / 远程 shell）会卡死或失败。`smith.exe --no-gui` 是 headless 烟雾测试入口（自动跑一条 "ver" → cancel → exit 0）。

---

## 提交约定

格式：`P{阶段}-N: <area> (<contracts>)`

例子：

- `P1-1: win/wstr.go (M1 + L5)` —— 第一轮的第 1 步，涉及 M1 和 L5 两条契约
- `P1-9b: tools (read/write/net/ps) + 11 tests (L1)` —— 9b 是 9 的扩展
- `P1-12: build.cmd + dist/smith{,64}.exe` —— 跨多文件的批量提交（**体积不写进 commit message**，随代码漂移）

**禁止**：

- 提交 `*.exe` 到 `bin/` / `obj/`（已被 .gitignore 排除的）
- 提交到 `master` 之外的分支（单分支线性历史）
- 跳过 vet + test 强行 commit

---

## 已知陷阱

1. **GUI 测试**：WM 命令循环阻塞 → 没法 `go test` 验证 GUI 行为。改 gui.go 后**至少** `386 + amd64 build` + 手动跑 `dist/smith.exe`。
2. **386 struct 对齐**：`wstrKeep`、`jobExtLimitInfo`、`processEntry32` 等跨架构结构体大小不同；用 `unsafe.Sizeof` 验，不要凭直觉。
3. **Mock LLM 测试**：用 `httptest.NewServer`（HTTP），不是 HTTPS。生产路径的 TLS 在 `spike/https` 验证，本机 mock 只验 wire 格式。
4. **spike/* 不可 import**：是独立 main 程序，import 会循环。
5. **spike 产物不在门禁保护范围内**（2026-09-28 复审实测确认）：
   - `build.cmd` 只构建 `dist\smith.exe` / `smith64.exe`，**不构建 spike**
   - `verify-pe.ps1` 也只校验这两个
   - 实测 9 个 spike exe 的 PE Subsystem **全是 3 (CONSOLE)** —— 这对 spike
     是**合理的**（它们就是要看 stdout 的控制台诊断程序），与主产品的
     `Subsystem==2` 断言不是同一套规则，两者不冲突
   - 后果：**"重建 dist/" 不会更新 spike 产物**，它们会静默漂移

   所以：改 spike 源码后必须**手工重建对应 exe** 并自己 commit：
   `go build -trimpath -ldflags "-s -w" -o dist\spike386\xxx.exe .\spike\xxx`
   （`GOARCH` 386 / amd64 各一次）。
6. **`build.cmd` 必须是 CRLF**：写成 LF 时 cmd.exe 会吞掉每行前几个字节，在解析器里空转 —— **表现是构建永久挂死**（不报错、不退出）。核对：
   ```powershell
   $b=[System.IO.File]::ReadAllBytes('build.cmd'); $crlf=0;$lf=0
   for($i=0;$i -lt $b.Length;$i++){ if($b[$i] -eq 10){ if($i -gt 0 -and $b[$i-1] -eq 13){$crlf++}else{$lf++} } }
   "$crlf CRLF / $lf LF-only"   # LF-only 必须是 0
   ```
7. **提交时别用 `git add --update`**：它**只更新已跟踪文件，新增文件会静默漏掉**。曾导致 `src/tools/limited_writer.go` 未入库 → `git archive HEAD` 出来 HEAD **不可编译**（`undefined: newCapWriter`）。**工作区绿 ≠ HEAD 绿** —— `build.cmd test` 验的是工作区。新增文件必须显式 `git add <file>`，收尾用 `git archive HEAD` 干净检出复验一次。
8. **中文 path + `git add path/`**：trailing slash 会让 git 报 "fatal: bad config"，去掉。
9. **PowerShell 不支持 `&&`**：用 `;`。
10. **PE 真机测试**：需要 Win7/10/11 PE 镜像 + 虚拟机，本机跑不了 → 用 `dist/spike386/*.exe` 拷 U 盘进 PE 验（见 `docs/08-PE测试清单.md`）。
