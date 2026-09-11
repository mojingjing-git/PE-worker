# Phase 0 验证报告

> 执行日期：2026-09-11
> 环境：本机 Windows 11 build 26200，amd64，Go **1.20.14**（新装）
> 状态：**本机可验证的部分全部通过；PE 专属项待真机验证**

---

## 0. 本轮最大的收获：抓到一个只在 32 位上出现的真 bug

**这是 Phase 0 存在的全部意义 —— 如果直接开写 Phase 1，这个 bug 会一直潜伏到很后面。**

### 现象

`job` spike 在 386 上跑：

```
SetInformationJobObject ret=0
  err = The program issued a command but the command length is incorrect.
  struct size = 108        ← 错
```

amd64 上同一个调用是 144，完全正常。**只在 32 位失败，而且不报错给用户。**

### 根因（用 `-diag` 实测确认）

```
                     Go(386)   Go(amd64)   MSVC(x86)   MSVC(x64)
Alignof(int64)          4          8           8           8
BasicLimitInformation  44         64          48          64
ExtLimitInformation   108        144         112         144
```

**Go 在 386 上把 `int64`/`uint64` 对齐到 4 字节，MSVC 默认对齐到 8 字节。**

`JOBOBJECT_EXTENDED_LIMIT_INFORMATION` 恰好以两个 `LARGE_INTEGER` 开头，于是：
- 前 16 字节两者一致
- 但从 `MinimumWorkingSetSize`（SIZE_T）开始，Go 认为不需要补 4 字节对齐、MSVC 认为需要
- 后面所有字段偏移全部错位 4 字节，总长 108 vs 112

### 后果（为什么不报错却致命）

`SetInformationJobObject` 收到错的 `cbSize` 直接返回 `ERROR_BAD_LENGTH`。而 job 对象**本身已经建好了** —— 如果不检查返回值，你会得到一个"看起来正常"的 job，**但 `KILL_ON_JOB_CLOSE` 从来没设上**。

于是：tinker 崩溃或被强杀时，**子进程（以及孙进程）不会被带走，变成孤儿进程继续跑**。在 PE 里这意味着一堆僵死的 `diskpart` / `dism` 还在锁着磁盘。

32 位是 Win7 PE 的主力架构 —— **这正好会打在主战场上。**

### 修法（已实施并验证）

**不用 Go 结构体，改手工构造字节缓冲 + 显式偏移：**

```go
const (
	jobExtLimitInfoSizeX86 = 112 // MSVC x86
	jobExtLimitInfoSizeX64 = 144 // MSVC x64
	jobLimitFlagsOffset    = 16  // 两种架构下 LimitFlags 都在偏移 16
)

func buildJobExtLimitInfo(killOnJobClose bool) []byte {
	size := jobExtLimitInfoSizeX64
	if unsafe.Sizeof(uintptr(0)) == 4 {
		size = jobExtLimitInfoSizeX86
	}
	buf := make([]byte, size)
	if killOnJobClose {
		binary.LittleEndian.PutUint32(buf[jobLimitFlagsOffset:], jobObjectLimitKillOnJobClose)
	}
	return buf
}
```

修复后实测：

| 架构 | cbSize | SetInformationJobObject | KILL_ON_JOB_CLOSE |
|---|---|---|---|
| amd64 | 144 | ret=1 OK | **armed** |
| 386 | 112 | ret=1 OK | **armed** |

### ⚠️ 由此产生的项目硬规则（新增）

> **凡是手写 Win32 结构体，只要它包含 64 位成员（`LARGE_INTEGER` / `ULONGLONG` / `DWORD64`），就存在 386 对齐风险。**
>
> 处理方式二选一：
> 1. **手工构造字节缓冲 + 显式偏移**（推荐，架构无关，不可能静默出错）
> 2. 用 build tag 分架构定义结构体，**并加编译期尺寸断言**
>
> 且：**所有 `Set*/Get*` 这类带 `cbSize` 参数的 API，必须检查返回值**，不能假设结构体定义是对的。

**已排查的其他结构体（均无此问题）：**

| 结构体 | Go(386) | MSVC(x86) | 结论 |
|---|---|---|---|
| `STARTUPINFOW` | 68 | 68 | ✅ |
| `PROCESS_INFORMATION` | 16 | 16 | ✅ |
| `PROCESSENTRY32W` | 556 | 556 | ✅ |
| `MODULEENTRY32W` | 1064 | 1064 | ✅ |
| `MEMORYSTATUSEX` | 72 | 72 | ✅（8 个 ULONGLONG 天然 8 对齐） |
| `RTL_OSVERSIONINFOW` | 276 | 276 | ✅ |
| ~~`JOBOBJECT_EXTENDED_LIMIT_INFORMATION`~~ | ~~108~~ | 112 | ❌ **已改用字节缓冲** |

---

## 1. 环境准备结果

| 项 | 结果 |
|---|---|
| Go 1.20.14 安装位置 | `C:\Users\wrz20\.workbuddy\binaries\go\versions\1.20.14` |
| 解压完整性 | 12029 / 12029 个文件 ✅ |
| `go version` | `go1.20.14 windows/amd64` ✅ |
| 本机原有 Go | go1.26.4 —— **确认不能用它构建**（产物要求 Win10+） |

**踩到的坑**：解压过程被 `safe-delete` 保护机制和一次 SIGTERM 中断过，第一次装出来的工具链缺 `src/time`、`src/unicode`、`src/unsafe` 等标准库目录，`go build` 直接失败。**判断工具链完整性的最快方法就是编一个 hello world。**

---

## 2. Go 1.20 的 API 差异（版本锁定的现实代价）

写代码时撞到两个 **Go 1.21+ 才有** 的东西：

| 用了 | 实际最低版本 | 替代方案 |
|---|---|---|
| `go -C <dir>` | **Go 1.21** | 改用 `cd` |
| `tls.VersionName()` | **Go 1.21** | 自己写 switch 映射 |

> 这说明**锁 Go 1.20 不是免费的** —— 写代码时要时刻注意"这个 API 1.20 有没有"。建议 Phase 1 起在 build 脚本里加 `go vet`，并且 CI 只认 Go 1.20。

---

## 3. P0-1 ~ P0-6 逐项结果

### ✅ P0-1 「Go 1.20 编出的 exe 能不能跑」— **本机通过，PE 待验**

- amd64 与 386 双架构均编译、运行正常
- `RtlGetVersion` 拿到 `10.0 build 26200`（证明优先用 ntdll 而不是 `GetVersionEx` 的做法可行）
- `GetNativeSystemInfo`、`GetComputerNameW`、`GetUserNameW`、`GetLogicalDrives` + `GetDriveTypeW` 全部返回正确值
- `GetTickCount` 正常工作（Vista+ 才有的 `GetTickCount64` 完全没用）
- 写文件 / 读回：exe 所在目录 ✅、`%TEMP%` ✅

### ✅ P0-2 「运行期到底加载了哪些 DLL」— **方法通过，PE 待验**

**方法本身验证成功，而且证明了二轮审核 B1 的担心是对的。**

在 Win11 上跑遍所有代码路径（建窗口 / GDI / psapi / iphlpapi / DNS / TLS / HTTPS GET）后，`CreateToolhelp32Snapshot` 枚举出 **46 个运行期模块**：

```
advapi32  apphelp   bcrypt    bcryptprimitives  combase  crypt32
cryptbase cryptsp   dhcpcsvc  dnsapi   dsparse   fwpuclnt  gdi32
gdi32full gpapi     imm32     iphlpapi kernel32  kernelbase  msasn1
msctf     msvcp_win msvcrt    mswsock  nsi       ntdll     powrprof
psapi     rasadhlp  rpcrt4    rsaenh   sechost   shcore    shell32
shlwapi   tsbx      ucrtbase  umpdc    user32    uxtheme   win32u
windows.storage  winhttp  winmm  ws2_32
```

**三个关键观察：**

1. **`syscall.NewLazyDLL` 延迟加载是真的** —— 如果只看静态导入表，这些一个都看不到。B1 的修正完全必要。
2. **Win10/11 的依赖图不等于 Win7 的。** 例如 `bcryptprimitives.dll`、`win32u.dll`、`gdi32full.dll` 都是 Win10 之后才拆出来/新增的，**Win7 上根本不存在**；反过来 Win7 会通过 `gdi32.dll` 里的导出直接调用。**所以这份清单不能当作 Win7 PE 的答案，必须在真 PE 里重新量一次。**
3. 出现了一堆看起来"不该有"的 DLL（`shell32`、`windows.storage`、`apphelp`、`uxtheme`、`comb ase`）—— 大概率是转换层/兼容垫片带进来的。**精简 PE 里这些很可能不存在**，是 P0-2 在 PE 里要重点确认的对象。

### ✅ P0-3 「纯 Go 能不能建 Win32 窗口」— **本机通过，PE 待验**

```
RegisterClassExW atom=49982
CreateWindowExW  hwnd=5901000
ShowWindow ret=0（符合预期：之前是隐藏状态）
消息循环跑了 3 个 tick，WM_DESTROY -> PostQuitMessage，干净退出
```

**`syscall.NewCallback` 在 Go 1.20 上确实可用**，纯 Go（`CGO_ENABLED=0`）写 Win32 GUI 这条路走通了。三区布局（只读多行 EDIT + 输入框 + 两个按钮 + 状态栏）正常创建、正常排布。

（本机屏幕 2194×1234，窗口 780×520 显示正常。）

### ⚠️ P0-4 「HTTPS 能不能握手成功」— **生产路径本机通过；"失败对照"无法在本机复现**

| 步骤 | 本机结果 |
|---|---|
| [1] TCP 连接 | ✅ |
| [2] TLS 握手（不校验证书） | ✅ TLS1.3 / TLS_AES_128_GCM_SHA256 |
| [3] TLS 握手（系统证书库 → CryptoAPI） | ✅ |
| [4] **TLS 握手（捆绑 CA bundle → 纯 Go 校验）** | ✅ **verifiedChains=1** |
| [5] 完整 HTTPS GET（捆绑 bundle） | ✅ 200 OK |

**关键实证：`x509.SystemCertPool()` 返回 `0 subjects`。**

这从实测上确认了 A1 的判断：**Windows 上 Go 的系统证书池就是个惰性标记（`systemPool: true`），证书根本不在 Go 这边**，校验全交给 crypt32 的 CryptoAPI。所以：
- 在根库完整的 Win11 上，[3] 和 [4] 都能成功
- **在根库过时的 Win7 PE 上，[3] 可能失败，而 [4] 是唯一可靠的路径**

⚠️ **"对照组必须失败"这个验收标准取消了** —— 本机就复现不出来（根库够新）。P0-4 在 PE 里的验收标准是：**[4] 和 [5] 必须成功**；[3] 的结果只作诊断记录。

### ⚠️ P0-5 「中止机制」— **本机部分通过，但 PE 里才是真正的考场**

这是本轮信息量最大的一项，因为它**抓到了 A3 预测的失败模式**：

```
[1] IsProcessInJob(GetCurrentProcess()) -> inJob=1     ← 进程确实已在某个 job 里
[3] CreateProcess(..., CREATE_BREAKAWAY_FROM_JOB)
      with BREAKAWAY failed (Access is denied.)        ← 父 job 不允许 breakaway
[4] AssignProcessToJobObject -> OK                     ← 但 Win11 支持嵌套 job，救回来了
```

**结论分两半：**

- **在 Win11 上**：即使 breakaway 被拒，嵌套 job 让 `AssignProcessToJobObject` 依然成功，中止机制正常工作 ✅
- **在 Win7 上**：**没有嵌套 job** → 第 4 步会失败 → **必须走降级路径**。这一项**只能在真 Win7 PE 里定论**

**降级路径已验证完全可用（第 7 步独立测试）：**

```
spawned pid=5896, tree size=3   (cmd.exe -> conhost.exe -> ping.exe)
killed 3, errors 0
still alive: 0   -> OK
```

自实现的进程树终止（`CreateToolhelp32Snapshot` 按 `th32ParentProcessID` 递归 + 逐个 `TerminateProcess`）**工作正常，且完全不依赖 `taskkill.exe`**。二轮审核 B2 的修正落地了。

**顺带确认**：进程树里还有 `conhost.exe`（cmd 的控制台宿主），所以"杀整棵树"确实比只杀 `cmd.exe` 更有必要。

### ✅ P0-6 「32 位 Go 的内存表现」— **本机通过**

386 版启动后：

```
VA range : 0x00010000 .. 0xFFFEFFFF      (Go 带 /LARGEADDRESSAWARE，4GB VA)
virt avail: 1372 MB                       ← 实际可用虚拟地址空间
```

分配压力测试（真实触碰每一页，持有不放）。**注意结果与本文初版不同 —— 原因见下面的修正说明**：

```
持有 1646 MB 后探针主动停手
  virt avail 1372 -> 221 MB
  HeapSys   = 1652 MB
  刹车原因：可用虚拟地址空间降到 221 MB（低于 224 MB 安全余量）
```

**结论：32 位 Go 在真正耗尽之前，实际能持有约 1.6 GB。** agent 只需要几十 MB（会话历史 + 几张截图），**差两个数量级，不构成风险。**

#### ⚠️ 这里推翻了一个错误的假设（三轮审计发现并修正）

初版的做法是"一直分配直到失败，用 `recover()` 接住"，报告里写的是"稳定持有 512MB"。**`recover()` 接不住。** 实测：

```
hello_386.exe -alloc 3000   ->  整屏 goroutine dump，进程带崩溃栈退出
```

根因：Go 的分配器在拿不到地址空间时调的是 **`runtime.throw("out of memory")`** —— 那是**不可恢复**的致命错误，不是 `panic`，`recover()` 完全无效。

**正确做法是"分配前先看地址空间余量，快见底就主动停手"**（已实施）。32 位下 Go 的堆是向系统**按 arena 预留**地址空间的（一次 64MB），所以余量必须留得比一个 arena 大，代码里取 224 MB。

**这条经验对正式版有直接意义**：`read` 工具读大文件、或会话里堆了几张截图 base64 时，如果不约束输入上限，32 位进程会以 `throw` 崩掉 —— 那是个**连 `recover` 都不生效、`defer` 也不执行**的崩溃点。所以生产代码必须**主动限制单次读入量和图片总量**，不能指望出错时兜住。

---

## 4. 构建产物尺寸（实测）

`-trimpath -ldflags "-s -w"`，三轮审计修复后重新构建：

| spike | 386 | amd64 |
|---|---|---|
| hello | 1.46 MB | 1.51 MB |
| gui | 1.33 MB | 1.39 MB |
| job | 1.37 MB | 1.42 MB |
| dlls（含 net/http + TLS） | 4.44 MB | 4.56 MB |
| https（含 CA bundle + net/http + TLS） | 4.71 MB | 4.83 MB |

（初版报告的 amd64 一列偏大，是当时漏了 `-trimpath`。两个架构合计 27.0 MB。）

**推算正式版 smith.exe**（2026-09-11 改名） ≈ gui + https + image/png + JSON ≈ **6~8 MB（386）**，与 PLAN 里"7~10MB"的估算吻合。加 UPX 可压到 3MB 左右。

---

## 5. 必须在真机 PE 里做的事（我做不了）

| 编号 | 要验什么 | 注意 |
|---|---|---|
| P0-1 | 五个 spike 在目标 PE 里能不能起来 | 先跑 `hello`，失败就看缺哪个 DLL |
| P0-2 | **在 PE 里重新枚举运行期模块**（Win10/11 的清单不作数） | 现在 spike 会自己 `LoadDLL` 一遍**声明的依赖集**并打印 `ABSENT` 行 —— **不用再人肉比对 System32** |
| P0-3 | PE 里能不能看到窗口 | 注意 PE 的分辨率（常是 800×600）和字体 |
| P0-4 | **[4] 捆绑 bundle 的握手必须成功** —— 这是唯一的生产门禁 | 默认目标已改为 `letsencrypt.org`（链末端 ISRG Root X1）。**"[3] 也成功"不算通过，也不能反过来证明 PE 根库没问题** |
| P0-5 | `AssignProcessToJobObject` 在 Win7 PE 上到底成不成 | **这条决定中止机制是走 job 还是走自实现杀树**（自实现那条已在本机验证可用） |
| P0-6 | 在 PE 的实际可用内存下跑 `hello -alloc 256` | 现在会干净地报告"持有 N MB 后停手"，不再崩 |
| 附加 | `tasklist.exe` / `taskkill.exe` 在 PE 里存不存在 | **已确认不再必要**：自实现杀树不依赖任何外部 exe |

---

## 6. 本轮对计划的净影响

| 项 | 变化 |
|---|---|
| **新增项目硬规则** | Win32 结构体含 64 位成员 → 必须用字节缓冲或分架构 + 尺寸断言；所有带 `cbSize` 的 API 必须查返回值 |
| **P0-6 的实现方式** | 不再单独写 `spike/mem`，**折进 `hello -alloc N`** |
| **P0-4 验收标准** | 取消"对照组必须失败"；改为"[4][5] 必须成功"（唯一门禁） |
| **P0-5 的结论** | 本机证明自实现杀树可用；job 路径的成败留待 PE 判定 |
| **Go 1.20 API 注意清单** | 新增：`go -C`、`tls.VersionName` 不可用（1.21+）。**修正**：`tls.CipherSuiteName` 是 1.14 就有的，**不在**禁用清单里 |
| **构建脚本** | 加 `go vet` + 严禁非 1.20 工具链的断言 |
| **新增硬规则（审计）** | ① 含 `int64`/`uint64`/指针的 Win32 结构体一律字节缓冲；② OOM 是 `throw` 不是 `panic`，**必须主动限制输入上限**；③ 无网络依赖的 DLL 探活；④ 诊断程序的结论必须可证伪 |

---

## 7. 三轮代码审计（2026-09-11）—— 发现与修复

Phase 0 的 spike 不只是"跑过就算"，它们**会作为正式产品 Win32 绑定层的参考实现被复用**，所以带着 bug 留着会直接传播进产品。于是做了一次完整的并行审计。

### 方法

按文件切成 5 个**不重叠**的片，一次并行派出 5 个审核 agent，再派第 6 个 agent **独立复核每一条结论**（过滤误报、校准严重度）。审核期间**不修改任何文件**。

### 结果

| | 条数 |
|---|---|
| 复核后成立 | **18** |
| 部分成立（事实存在但严重度被夸大） | **7** |
| **误报** | **1** |
| 无法验证 | 0 |

**唯一的误报**：审核说 `tls.CipherSuiteName` 是 Go 1.21+ 才有的（本报告初版也这么写）。复核去 Go 1.20 源码 `crypto/tls/cipher_suites.go:100` 确认 —— **它 Go 1.14 就有了**，1.21+ 才加的是 `tls.VersionName`。这个错误已经写进本文档和记忆里，属于"两处都错了"的情况，已一并修正。

### 修复清单（全部已实施并回归验证）

**🔴 致命**

| # | 位置 | 问题 | 修法 |
|---|---|---|---|
| B1 | `spike/gui` `wcs()` | `UTF16PtrFromString` 的 `*uint16` 转成 `uintptr` 返回后**不再被 Go 引用**，GC 可能在 Win32 调用期间回收它。`CreateWindowExW` 那一行同一实参表里连调两次 `wcs()`，第二次分配就可能触发 GC | 加包级 `strKeep []unsafe.Pointer` 持有引用；`appendLog` 加 `runtime.KeepAlive` |
| B2 | `spike/gui` `main()` | `runtime.LockOSThread()` 调用**晚于** `CreateWindowExW` → 建窗线程可能 ≠ 消息循环线程 → 窗口冻结、定时器不触发 | 移到 `main()` 第一行 |
| A1 | `spike/job` `killTreeSelfContained` | 单次静态快照：快照之后派生的子孙**漏杀**；root PID 被复用会**误杀无关进程** | 改"快照→从叶子杀到根→再快照"循环到无进展；入口与每轮都做**映像名比对**防 PID 复用 |
| A2 | `spike/job` step[6] | `TerminateJobObject` 失败只打印、**无降级** → 中止静默失效 | 失败时回退到自实现杀树 |
| C1 | `spike/dlls` | `ws2_32`/`crypt32`/`dnsapi` **只通过真实网络 I/O** 被加载 → 离线 PE 上它们不会被载入，模块清单**漏报**，工程师据此误判"不需要这些 DLL" | 新增 `forceLoadDeclared()`：显式 `LoadDLL` 声明的依赖集（**不依赖网络**），并直接打印 `ABSENT` |

**🟠 重要**

| # | 位置 | 问题 | 修法 |
|---|---|---|---|
| E2 | `src/tinker.c` | `AssignProcessToJobObject` 在 `CreateProcess` **返回之后**才调用 → 子进程可能已派生孙进程，孙进程逃出 job | 改 `CREATE_SUSPENDED` → Assign → `ResumeThread`（wine 上对齐 Go spike 的做法） |
| E3 | `src/tinker.c` | `hNul` 用 `if (hNul)` 判断失败，但 `CreateFileW` 失败返回 `INVALID_HANDLE_VALUE`（非 NULL）→ `CloseHandle((HANDLE)-1)` + 所有命令启动失败 | 改判 `hNul == INVALID_HANDLE_VALUE` |
| C2 | `spike/hello` | 大 `-alloc` 直接以 goroutine dump 退出，探针**唯一要回答的问题**反而答不出来 | 见 §4 的修正说明：改"分配前余量检查"，实测可干净报告 1646 MB |
| D2 | `spike/https` | `tls.DialWithDialer` 的 timeout **只覆盖 TCP 连接，不覆盖握手** → 对端不回握手消息时无限挂住（PE 里就是卡死的 GUI） | 手动 `Dial` → `SetDeadline` → `tls.Client().Handshake()`，并补 `ServerName` |
| C3 | `spike/dlls` | 打印 `"(DLLs were still loaded)"` 是**未经检验的断言** | 改成按实际结果措辞，并把权威结论交给模块枚举 |
| D6 | `assets/assets.go` | 嵌入的 CA bundle 是构建期快照会过期，**没有任何日期或提醒** | 新增 `CACertPEMSnapshot` 常量，并在 https spike 输出里打印 |
| A3 | `spike/job` step[7] | `pi2.Process` / `pi2.Thread` 句柄从不关闭 | 补 `CloseHandle` |
| A4 | `spike/job` step[5] | `ResumeThread` 返回值未检查 → 恢复失败会被误读成"程序没派生子进程" | 检查 `(DWORD)-1` 并报告 |
| A5 | `spike/job` step[1] | `IsProcessInJob` 失败（`r==0`）未识别，`inJob` 保持 0 → 误报"不在 job 里" | 按 `r` 分支，失败时报 UNKNOWN |
| A6 | `spike/job` `snapshotProcs` | `Process32First/NextW` 返回 0 时**不区分"枚举结束"和"真出错"** → 快照被静默截断 | 用 `Call` 带回的 errno 比对 `ERROR_NO_MORE_FILES` |
| A7 | `spike/job` | `os.Exit(0)` 跳过 `defer` → `hJob` 永不关闭 | 改为显式关闭（关它会触发 KILL_ON_JOB_CLOSE，正好清残留） |
| E4/E5 | `src/tinker.c` | 日志裁剪 ① 可能切断 UTF-16 代理对 ② 只在**已经**超限时才裁，单条超大日志永不裁 | 改为"追加前按将要达到的尺寸判断"，并把裁点从代理对中间退出来 |
| D1/D3 | `spike/https` | 默认目标 `example.com` 的根 CA 在旧系统上也普遍存在 → **假设无法证伪**，verdict 会给出虚假安心 | 默认目标改 `letsencrypt.org`（链末端是较新的 ISRG Root X1）；verdict 改为**只断言 bundle 必须成功**，并明确写"tls-system 成功也不能证明 PE 根库没问题" |
| D4/D5 | `spike/https` | `io.ReadAll` 错误被忽略；默认 client 会跟随跨主机重定向 | 检查读错误；加 `CheckRedirect` 限制跳数与同源 |

**🟡 次要（也已修）**

`spike/gui`：`layout` 改用 `GetClientRect`（原来用窗口外框尺寸，状态栏可能被推出可见区）；`GetMessageW` 返回 `-1` 不再被当成正常退出。
`src/tinker.c`：`g_cancel` 加 `volatile`（写侧本来就是 `InterlockedExchange`，读侧缺了对称保护）。
`spike/dlls`：网络步骤失败时明确标注 "NOT exercised" 而不是含糊带过。

### 两处"审核建议本身有问题"——已核实后否掉

1. **不要用 `recover()` 兜 OOM**（审核 C2 的建议）。实测推翻：OOM 走的是 `runtime.throw`，不可恢复。改用"分配前余量检查"。
2. **`tls.CipherSuiteName` 不是 1.21+**（审核 D7，也是本报告初版的错误）。已去 Go 1.20 源码确认。

### 一个判断上的收获

审核说 `A1`（杀树漏杀）是"致命"。**它是真问题，但在 32 位 PE 的实际场景里没那么致命** —— 而 `B1`（GC 野指针）和 `B2`（`LockOSThread` 位置）看起来"只是代码风格问题"，**却是真正会让 PE 里的 GUI 直接坏掉的那类 bug**。

这条经验值得记住：**互操作层的 bug，危险程度跟"代码看起来多可疑"基本无关，只跟"失效是显式报错还是静默"有关。**

---

## 8. 一句话总结

> **六项验证里五项在本机通过，一项（P0-5 的 job 路径）必须在真 PE 里定论。**
> 本轮真正的价值不是那五个 ✅，而是**抓到了 386 结构体对齐这个只在 32 位出现、不报错、又正好打在 Win7 PE 主战场上的 bug** —— 以及确认了 `CREATE_BREAKAWAY_FROM_JOB` 会被父 job 拒绝这个真实存在的运行环境约束。
> 随后的**三轮并行代码审计又清掉了 25 条问题**（1 条误报），其中 `wcs()` 的 GC 野指针和 `LockOSThread` 的调用位置是两个会直接破坏 PE 里 GUI 的静默故障 —— 这类 bug 只有在"照这个计划动手写会卡在哪"的视角下才看得见。

