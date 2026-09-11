# AGENTS.md

> 给 AI 编程 agent（OpenCode / Codex / Cursor / Aider / Devin / Gemini CLI 等）
> 阅读的项目工作约定。本文件由 `init` skill 生成结构，人工维护内容。

---

## 项目一句话

**PE-agent**（代号 `owl` / 夜枭）—— 一个**单 exe、纯 Go、32 位为主**的 Windows PE 应急助手。GUI 三区 + 内置 14 工具 + 多 LLM provider（Anthropic / OpenAI / DeepSeek），跑在精简的 Win7/Win10/Win11 PE 镜像里，不依赖系统根证书库。

主要交付：`dist/owl.exe`（5.4MB, 386）+ `dist/owl64.exe`（5.5MB, amd64）。

---

## 必读

| 顺序 | 路径 | 用途 |
|---|---|---|
| 1 | [`PLAN.md`](./PLAN.md) | 设计源头：决策、风险、字段表、阶段计划 |
| 2 | `docs/01-05,07-08` | 各专项设计（06 编号空缺，详见目录结构） |
| 3 | `spike/` | Phase 0 技术预研产物（5 个独立程序，可读不可 import） |
| 4 | `src/` | **本项目唯一可改的代码区** |

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
| **M1** | `strKeep` 持有 `*uint16` / `[]uint16` 永不 `[:0]` 重置；PostMessage 后必须 `win.KeepAlive(lp)` | `src/win/wstr.go` `strKeep` |
| **M2** | PID 复用双层防护：root 名字不符 → 整轮中止；非 root 节点查 `InheritedFromUniqueProcessId` | `src/win/job.go` + `src/win/proc.go` |
| **L1** | 所有 API 返 `(T, error)`；不返裸 T（除 `void` 等价物） | 全包 |
| **L4** | 多步测试的 VERDICT 必须把**每一步**都纳入判据 | `src/agent/verdict.go` |
| **L5** | 不吞错：err 一律透传到底层 caller；可加 `fmt.Errorf("ctx: %w", err)` 链 | 全包 |

**改代码前问自己：会不会破坏这 5 条？**

### 4. 386 vs amd64

- **386 是主战场**（Win7 PE 主力 32 位）
- 不带 CGO（PE 镜像里没 C 编译器）
- 不带 `-race`（race detector 386 + 无 CGO 不可用）
- `unsafe.Sizeof(struct{})` 在两架构下**必然不同**（108 vs 112 这种）
- Win32 互操作遵循 spike/* 5 个程序：`spike/job` (Job+进程快照) / `spike/gui` (LockOSThread+窗口) / `spike/hello` (提交限制自检) / `spike/https` (CA bundle) / `spike/dlls` (DLL 依赖)
- `runtime.LockOSThread()` **必须是**线程入口函数第一行（晚于 `CreateWindowExW` 会让消息循环线程 ≠ 建窗线程 → 窗口冻结）

### 5. 步间审核（当前工作模式）

每次提交前必须：

1. `go vet -unsafeptr=false ./src/win/...` + `go vet ./src/{agent,cfg,logx,tools,test}/...`
2. `go test -count=1 ./src/...`（386 必跑，amd64 跑一次确认）
3. `go build` 双架构产物（如改 win/ 或 main.go）
4. commit message 格式：`P1-N: <file> (<contracts>)` —— 例：`P1-9b: tools (read/write/net/ps) + 11 tests (L1)`

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
- `!dist/owl.exe` / `!dist/owl64.exe` / `!dist/spike{386,64}/*.exe` 重新放行（用户拷 U 盘用）
- `.tmp/` / `.workbuddy/` 完全排除（Mavis runtime + 工作缓存）

**新建可执行产物时**：要么改 `.gitignore` 放行，要么不 commit（看是不是用户要拷 U 盘的产物）。

---

## 目录结构

```
F:\AI\01_项目\PE-agent\
├── PLAN.md                 # 设计源头（必读）
├── AGENTS.md               # 本文件
├── README.md               # GitHub 入口
├── go.mod                  # module peagent, go 1.20
├── build.cmd               # 一键 vet + test + build（默认/clean/test 三个子命令）
├── owl.ini.example         # 配置文件模板
├── docs/                   # 7 篇专项设计（06 编号空缺；工具集/GUI/视觉/PE 验收）
├── spike/                  # Phase 0 预研（只读，5 个独立 Go 程序）
│   ├── job/                #   Job Object + 进程快照 + 杀树 + 386 字节缓冲
│   ├── gui/                #   Win32 窗口 + LockOSThread + UTF-16 持引用
│   ├── hello/              #   提交限制自检（输出全部 ASCII）
│   ├── https/              #   嵌入 CA bundle + TLS 1.2 + 4 步对照
│   └── dlls/               #   可加载 DLL 清单
├── assets/                 # //go:embed 静态资源
│   ├── assets.go           #   var CACertPEM []byte
│   └── cacert.pem          #   Mozilla CA bundle（更新见 assets.go 注释）
├── src/                    # 唯一可改区
│   ├── main.go             #   入口：boot 序列（早期日志→cfg→LLM→worker→GUI）
│   ├── tinker.c            #   C 骨架参考（不编进 exe，docs/03 里引用设计）
│   ├── win/                #   Win32 互操作（最易踩雷）
│   ├── agent/              #   LLM 客户端 + 适配层 + loop + history + verdict
│   ├── tools/              #   14 工具注册表 + 业务实现
│   ├── cfg/                #   INI 解析
│   ├── logx/               #   日志 + PostMessage 投递
│   └── test/               #   集成测试（e2e + smoke_bin）
└── dist/                   # 产物（部分入仓）
    ├── owl.exe             #   Phase 1 主产物（386, 5.4MB）
    ├── owl64.exe           #   Phase 1 主产物（amd64, 5.5MB）
    ├── spike386/*.exe      #   5 个 spike 386 产物（拷 U 盘验 PE）
    └── spike64/*.exe       #   5 个 spike amd64 产物
```

---

## 改代码前的 checklist

- [ ] 看了 PLAN.md 相关章节
- [ ] 看了 docs/ 相关专项
- [ ] 看了对应 spike 程序的对应行
- [ ] 改动没破坏 M1/M2/L1/L4/L5 五条硬规则
- [ ] 没引入 1.21+ 的 stdlib API（grep 一下）
- [ ] 没引入 `GetTickCount64` / `GetVersionExA` / `RegGetValueA`
- [ ] 没引入 `os/user`（拉 netapi32.dll）
- [ ] 新增工具：`tools/` + 在 `loop_test.go` 的 `allToolNames` 加名字
- [ ] 新增 Win32 proc：`win/api_*.go` 声明 + 386 字节缓冲（如有 struct）
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

**注意**：`gui.exe` / spike 里真开窗口的程序需要 desktop，**无头环境**（CI / 远程 shell）会卡死或失败。`owl.exe --no-gui` 是 headless 烟雾测试入口（自动跑一条 "ver" → cancel → exit 0）。

---

## 提交约定

格式：`P{阶段}-N: <area> (<contracts>)`

例子：

- `P1-1: win/wstr.go (M1 + L5)` —— 第一轮的第 1 步，涉及 M1 和 L5 两条契约
- `P1-9b: tools (read/write/net/ps) + 11 tests (L1)` —— 9b 是 9 的扩展
- `P1-12: build.cmd + dist/owl{,64}.exe (5.4/5.5MB)` —— 跨多文件的批量提交

**禁止**：

- 提交 `*.exe` 到 `bin/` / `obj/`（已被 .gitignore 排除的）
- 提交到 `master` 之外的分支（单分支线性历史）
- 跳过 vet + test 强行 commit

---

## 已知陷阱

1. **GUI 测试**：WM 命令循环阻塞 → 没法 `go test` 验证 GUI 行为。改 gui.go 后**至少** `386 + amd64 build` + 手动跑 `dist/owl.exe`。
2. **386 struct 对齐**：`wstrKeep`、`jobExtLimitInfo`、`processEntry32` 等跨架构结构体大小不同；用 `unsafe.Sizeof` 验，不要凭直觉。
3. **Mock LLM 测试**：用 `httptest.NewServer`（HTTP），不是 HTTPS。生产路径的 TLS 在 `spike/https` 验证，本机 mock 只验 wire 格式。
4. **spike/* 不可 import**：是独立 main 程序，import 会循环。
5. **中文 path + `git add path/`**：trailing slash 会让 git 报 "fatal: bad config"，去掉。
6. **PowerShell 不支持 `&&`**：用 `;`。
7. **PE 真机测试**：需要 Win7/10/11 PE 镜像 + 虚拟机，本机跑不了 → 用 `dist/spike386/*.exe` 拷 U 盘进 PE 验（见 `docs/08-PE测试清单.md`）。
