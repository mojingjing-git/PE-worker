# AGENTS.md

> 给 AI 编程 agent（OpenCode / Codex / Cursor / Aider / Devin / Gemini CLI 等）
> 阅读的项目工作约定。本文件由 `init` skill 生成结构，人工维护内容。

---

## 项目一句话

**PE-agent**（代号 `smith` / 铁匠）—— 一个**单 exe、纯 Go、32 位为主**的 Windows PE 应急助手。GUI 三区 + 内置 14 工具 + 多 LLM provider（Anthropic / OpenAI / DeepSeek），跑在精简的 Win7/Win10/Win11 PE 镜像里，不依赖系统根证书库。

主要交付：`dist/smith.exe`（386）+ `dist/smith64.exe`（amd64），各约 5~6 MB。

> **能力现状（2026-09-28 实测）**：14 工具已注册并可跑；**Job Object 杀进程树代码就绪但尚未接入生产路径**（docs/11 §S1-2），Esc 中止目前只杀直接子进程；`screenshot` 等 Phase 4 视觉工具、`sysinfo/diskinfo/netinfo/kill` 均未实现。详见 README 特性表。

---

## 必读

| 顺序 | 路径 | 用途 |
|---|---|---|
| 1 | [`PLAN.md`](./PLAN.md) | 设计源头：决策、风险、字段表、阶段计划 |
| 2 | [`docs/11-审计整改计划.md`](./docs/11-审计整改计划.md) | **当前待办的权威来源**：S0~S8 八批整改的定义 + 实施记录 |
| 3 | `docs/01-05,07-10` | 各专项设计（06 编号空缺） |
| 4 | `spike/` | 技术预研产物（**只读，9 个独立程序**，可读不可 import） |
| 5 | `src/` | **本项目唯一可改的代码区** |

**改代码前先查 PLAN.md §0.9**（v1 第四轮 + v2 复审合并的 9 条项目硬规则）。`src/win/` 是最容易踩雷的（Win32 ABI + 386 结构体对齐）。

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
- 禁用清单（`build.cmd` 启动时硬 grep）：
  - `tls.VersionName`（1.21+）
  - `os/user`（拉 netapi32/userenv.dll，PE 镜像里缺）
  - `GetTickCount64(` / `pGetTickCount64.Call`（Win7 PE 缺，会运行时炸）
  - `GetVersionExA(`（Win8.1+ 无 manifest 返假值）
  - `RegGetValueA(`（Win7 注册表 API 不全）
- `ticker.NewTicker` 1.20 有；`time.AfterFunc` 1.20 有 — 别用 1.21+ 才加的

### 3. 项目硬规则（PLAN §0.9 v1 第四轮 + v2 复审）

| ID | 规则 | 落地位置 |
|---|---|---|
| **M1** | `wstrKeep []*uint16` / `wstrKeepSlices [][]uint16` 持有 `*uint16` / `[]uint16`，**永不 `[:0]` 重置**；PostMessage 后必须 `win.KeepAlive(lp)` | `src/win/wstr.go`（**注意实际符号是 `wstrKeep` / `wstrKeepSlices`，不是 `strKeep`**） |
| **M2** | PID 复用双层防护：root 名字不符 → 整轮中止；非 root 节点查 `InheritedFromUniqueProcessId` | **两层全在 `src/win/proc.go` 的 `KillTreeSelfContained` / `treeOf`**。`src/win/job.go` **只是 Job Object API 的封装**（Create/SetKillOnClose/Assign/Terminate/IsProcessInJob），不含防护逻辑 |
| **L1** | 所有 API 返 `(T, error)`；不返裸 T（除 `void` 等价物） | 全包 |
| **L4** | 多步测试的 VERDICT 必须把**每一步**都纳入判据 | `src/agent/verdict.go` |
| **L5** | 不吞错：err 一律透传到底层 caller；可加 `fmt.Errorf("ctx: %w", err)` 链 | 全包 |
| **B2** | `runtime.LockOSThread()` 必须是线程入口函数**第一行**（含 keydialog 的子消息循环） | 全 `win/` |
| **V1** | **`win/` 的 `go vet` 必须用 `-unsafeptr=false`**，否则 Win32 互操作必需的 `uintptr` ↔ `unsafe.Pointer` 互转会刷屏告警 | `build.cmd` 收尾门禁 |
| **S1** | **含 64 位成员的手写 Win32 结构体有 386 对齐风险** → 用**手工构造字节缓冲 + 显式偏移**，不靠 `unsafe.Sizeof` 猜 | `src/win/job.go` `buildJobExtLimitInfo`（386=112 / amd64=144，`LimitFlags` 恒在偏移 16）。**这条铁律的来源：386 上写 108 字节时 `SetInformationJobObject` 返 `ERROR_BAD_LENGTH` 但不抛错，`KILL_ON_JOB_CLOSE` 静默失效** |
| **C1** | **Win32 常量必须对照 SDK 头文件 + 实测验证**，禁止凭记忆写 | `src/win/consts_test.go`（68 条断言 + 覆盖度自检 `expectedCount = 68`，新增常量漏加断言直接红）。**真实踩坑：`WM_TIMER` 曾写成 `0x0118`（那是 `WM_SWITCHWINDOW`）** |

**改代码前问自己：会不会破坏这 9 条？**（M1 / M2 / L1 / L4 / L5 来自 PLAN §0.9；B2 / V1 / S1 / C1 是 Phase 1 之后踩坑补上的）

### 4. 386 vs amd64

- **386 是主战场**（Win7 PE 主力 32 位）
- 不带 CGO（PE 镜像里没 C 编译器）
- 不带 `-race`（race detector 386 + 无 CGO 不可用）
- `unsafe.Sizeof(struct{})` 在两架构下**必然不同**（108 vs 112 这种）。**含 64 位成员时按 S1 处理：手工字节缓冲 + 显式偏移**
- Win32 互操作遵循 spike/* 的**只读探针**（当前 9 个程序）：`spike/job` (Job+进程快照+杀树) / `spike/gui` (LockOSThread+窗口) / `spike/hello` (提交限制自检) / `spike/https` (CA bundle) / `spike/dlls` (DLL 依赖) / `spike/hta` (HTA 前端) / `spike/oem2utf8` (编码转换) / `spike/richedit` (RichEdit 控件) / `spike/screenshot` (抓屏)
- **新加 Win32 常量必须同时在 `src/win/consts_test.go` 加断言**（有 `expectedCount` 覆盖度自检，漏了会红）
- `runtime.LockOSThread()` **必须是**线程入口函数第一行（晚于 `CreateWindowExW` 会让消息循环线程 ≠ 建窗线程 → 窗口冻结）

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
  review 的二进制 diff。**新增 spike 时必须同步在 `.gitignore` 里登记**，
  否则产物会以 `??` 状态一直飘着（`git add .` 时会被静默吞进下一个 commit）。
- `.tmp/` / `.workbuddy/` 完全排除（Mavis runtime + 工作缓存）
- `dist/smith.key` / `dist/smith.ini` / `dist/*.log` 也排除 —— dist/ 是"拷 U 盘交付"
  的目录，但运行时会在自己旁边写这些文件，`smith.key` 是真实 API key

**新建可执行产物时**：要么改 `.gitignore` 放行，要么不 commit（看是不是用户要拷 U 盘的产物）。

---

## 目录结构

```
F:\AI\01_项目\PE-agent\
├── PLAN.md                 # 设计源头（必读）
├── AGENTS.md               # 本文件
├── README.md               # GitHub 入口
├── CHANGELOG.md            # 变更日志
├── go.mod                  # module peagent, go 1.20
├── build.cmd               # 一键 vet + gofmt + build + PE 校验 + test（默认/clean/test）
├── verify-pe.ps1           # PE 头校验（Subsystem / 导入表 / 体积）
├── smith.ini.example       # 配置文件模板（字段表见 PLAN §0.6 B6）
├── docs/                   # 10 篇专项设计（06 编号空缺；11 = 审计整改计划 S0~S8）
│   └── 11-审计整改计划.md    #   **整改前后对照与实施记录的权威来源**
├── spike/                  # Phase 0 预研 + 前端探针（只读，9 个独立 Go 程序）
│   ├── job/                #   Job Object + 进程快照 + 杀树 + 386 字节缓冲
│   ├── gui/                #   Win32 窗口 + LockOSThread + UTF-16 持引用
│   ├── hello/              #   提交限制自检（输出全部 ASCII）
│   ├── https/              #   嵌入 CA bundle + TLS 1.2 + 4 步对照
│   ├── dlls/               #   可加载 DLL 清单
│   ├── hta/                #   HTA 前端探针
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
│   │                       #   gui / keydialog / msgs / job / proc / sysinfo / oem + consts_test
│   ├── agent/              #   LLM 客户端 + 适配层 + loop + history + verdict
│   ├── tools/              #   14 工具注册表 + 业务实现 + limited_writer（输出硬上限）
│   ├── cfg/                #   INI 解析
│   ├── logx/               #   日志 + PostMessage 投递
│   └── test/               #   集成测试（e2e + smoke_bin）
└── dist/                   # 产物（部分入仓）
    ├── smith.exe             #   Phase 1 主产物（386，约 5~6 MB）
    ├── smith64.exe           #   Phase 1 主产物（amd64，约 5~6 MB）
    ├── spike386/*.exe      #   9 个 spike 386 产物（拷 U 盘验 PE）
    └── spike64/*.exe       #   9 个 spike amd64 产物
```

> 产物精确体积由 `build.cmd` 收尾的 `verify-pe.ps1` 实测输出，**不要在文档里手写字节数**。

---

## 改代码前的 checklist

- [ ] 看了 PLAN.md 相关章节
- [ ] 看了 docs/ 相关专项（尤其 **docs/11** 的对应批次）
- [ ] 看了对应 spike 程序的对应行
- [ ] 改动没破坏 **9 条硬规则**（M1/M2/L1/L4/L5/B2/V1/S1/C1，见 §3）
- [ ] 没引入 1.21+ 的 stdlib API（grep 一下）
- [ ] 没引入 `GetTickCount64` / `GetVersionExA` / `RegGetValueA`
- [ ] 没引入 `os/user`（拉 netapi32.dll）
- [ ] 新增工具：`tools/` + 在 `tools/tools_test.go` 的 `allToolNames` 加名字 + `expectRegisteredTools` 加 1
- [ ] 新增 Win32 proc：`win/api_*.go` 声明 + 386 字节缓冲（如有 struct，按 S1 手工构造）
- [ ] **新增 Win32 常量：`win/consts_test.go` 加断言**（`expectedCount` 同步 +1，否则门禁红）
- [ ] 386 + amd64 双 build + test 都过

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
5. **中文 path + `git add path/`**：trailing slash 会让 git 报 "fatal: bad config"，去掉。
6. **PowerShell 不支持 `&&`**：用 `;`。
7. **PE 真机测试**：需要 Win7/10/11 PE 镜像 + 虚拟机，本机跑不了 → 用 `dist/spike386/*.exe` 拷 U 盘进 PE 验（见 `docs/08-PE测试清单.md`）。
