# PE-agent (owl / 夜枭)

[![Go 1.20.14](https://img.shields.io/badge/Go-1.20.14-00ADD8?logo=go)](https://go.dev/dl/)
[![386 + amd64](https://img.shields.io/badge/arch-386%20%2B%20amd64-blue)](#build)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](./LICENSE)

> 一个**单 exe、纯 Go、32 位为主**的 Windows PE 应急助手。
> 三区 GUI + 14 工具 + 多 LLM provider，
> 跑在精简的 Win7 / Win10 / Win11 PE 镜像里，不依赖系统根证书库。

中文代号：**夜枭 (owl)**。窗口标题 `owl - PE agent`，日志前缀 `ai > you > -> <- !!`。

---

## 截图占位

> Phase 1 完成 GUI 骨架（win/gui.go）。Phase 4 视觉阶段补真实截图。

```
┌────────────────────────────────────────────────────┐
│ [I] ** ver boot start log=...\owl.log              │
│ [I] ** ver cfg loaded ini=...\owl.ini              │
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

- ✅ **单 exe 部署** —— 5.4MB (386) / 5.5MB (amd64)，无外部 DLL 依赖
- ✅ **双架构** —— 386 是主战场（Win7 PE 主力 32 位）
- ✅ **GUI 三区** —— 多行日志 + 单行输入 + 状态栏（v1-M1 持引用 + LockOSThread）
- ✅ **14 工具** —— exec / run_script / ls / cat / grep / find / write / edit / append / http_get / https_get / ps / help / selftest
- ✅ **3 LLM provider** —— Anthropic / OpenAI / DeepSeek（DeepSeek 特有的 `reasoning_content` 单独吸收）
- ✅ **自带 CA bundle** —— `assets/cacert.pem` 用 `//go:embed` 嵌入，绕过 PE 镜像里过时的系统根库
- ✅ **Job Object 杀整棵进程树** —— 双层 PID 复用防护（v1-M2）
- ✅ **早期文件日志** —— GUI 起来前就能 trace，方便排查 PE 里看不到控制台的情况
- ✅ **Esc 中止** —— 杀当前工具调用 + 取消 LLM 请求
- ✅ **错误透传** —— 4xx body 全文带回（PE 里没浏览器能查文档）

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
dist\owl.exe     5,385,728 bytes (386,  Win7 PE 主力)
dist\owl64.exe   5,529,600 bytes (amd64, 新 PE)
```

### 配置

复制 `owl.ini.example` → `owl.ini`（同目录），填 LLM 配置：

```ini
[llm]
base = https://api.openai.com/v1
model = gpt-4
provider = openai
keyfile = owl.key       ; 优先 keyfile（不暴露在 tasklist）
; key = sk-...          ; 兜底用 ini literal（不推荐）
timeout = 120
```

`owl.key` 放同目录，单行 API key。

### 跑

```cmd
:: GUI 模式
dist\owl.exe

:: 无 GUI 烟雾测试（headless，自动跑 "ver" → cancel → exit 0）
dist\owl.exe --no-gui

:: CLI 应急：用 --key 临时覆盖 ini（不推荐，明文在 tasklist 可见）
dist\owl.exe --key sk-...
```

---

## 项目结构

```
peagent/
├── PLAN.md                # 设计源头（决策 / 风险 / 字段表）
├── AGENTS.md              # AI agent 工作约定
├── README.md              # 本文件
├── go.mod                 # module peagent, go 1.20
├── build.cmd              # 一键 vet + test + build
├── owl.ini.example        # 配置文件模板
├── docs/                  # 7 篇专项设计（06 编号空缺）
│   ├── 01-WinPE-agent-开源项目调研.md
│   ├── 02-工具集设计建议.md
│   ├── 03-GUI设计与命名建议.md
│   ├── 04-单机传输与部署方案.md
│   ├── 05-视觉与截图支持设计.md
│   ├── 07-Phase0-验证报告.md
│   └── 08-PE测试清单.md
├── spike/                 # Phase 0 预研（只读，5 个独立程序）
├── assets/                # 嵌入资源
│   ├── assets.go
│   └── cacert.pem         # Mozilla CA bundle
├── src/                   # 唯一可改区
│   ├── main.go            # 入口
│   ├── win/               # Win32 互操作
│   ├── agent/             # LLM 客户端 + loop
│   ├── tools/             # 14 工具
│   ├── cfg/               # INI 解析
│   ├── logx/              # 日志
│   └── test/              # 集成测试
└── dist/                  # 产物（部分入仓）
    ├── owl.exe            # Phase 1 主产物
    ├── owl64.exe
    └── spike{386,64}/     # 5 个 spike 程序（PE 测试用）
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

- [x] **Phase 0** —— 技术预研（5 个 spike 程序 + 3 轮代码审计 + 25 个问题修复）
- [x] **Phase 1** —— 骨架打通（GUI + exec + 14 工具 + LLM 适配 + loop + e2e 测试）
- [ ] **Phase 2** —— agent loop 真实跑通（mock 已验，待真 API 验）
- [ ] **Phase 3** —— 14 工具在 PE 里手测（需要真 PE 镜像）
- [ ] **Phase 4** —— screenshot + 视觉
- [ ] **Phase 5** —— 加固与分发

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
| 9 | Job Object 杀树 | Esc 中止时用，PID 复用双层防护 |
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

**当前状态**：6 包全过、113 个测试 PASS（386 + amd64）。

| 包 | 测试数 | 覆盖 |
|---|---|---|
| `win` | 35 | UTF-16 持引用 / Job Object / 进程快照 / GUI 消息 |
| `agent` | 28 | LLM 客户端（3 provider mock）/ loop / history / verdict |
| `tools` | 22 | 14 工具全部 smoke + help/selftest 元工具 |
| `cfg` | 12 | INI 解析 + provider/default |
| `logx` | 5 | PostMessage 投递 + fallback sink |
| `test` | 11 | e2e（loop+tools+cfg 串通）+ smoke_bin（跑 dist/owl.exe） |

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
