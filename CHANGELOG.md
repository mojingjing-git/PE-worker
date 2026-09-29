# Changelog

PE-agent (smith / 铁匠) 项目的变更日志。

格式参考 [Keep a Changelog](https://keepachangelog.com/)，按版本倒序排列（最新在上）。

---

## [Unreleased] - 2026-09-29

### 收尾与功能补齐（T0–T4 批 · docs/12）

批次定义与审阅过程见 [`docs/12-收尾与功能补齐计划.md`](./docs/12-收尾与功能补齐计划.md)。
本次是**五批独立提交**，每批单独复审、单独过 `build.cmd test`：

| 批次 | commit | 一句话 |
|---|---|---|
| T0 线程安全 | `637d0f8` + 复审 `338ae9a` | `wstrKeep` 加锁 |
| T1 可诊断性 | `c5d82da` | MessageBoxW + 错误出口收口 |
| T2 Job 杀树 | `40cfa2e` | `win/jobexec.go` 接入 `exec` |
| T3 功能补齐 | `c5d82da` | `diskinfo` / `sysinfo` / `kill` |
| T4 健壮性收尾 | `7fada9b` | 12 项中的 9 项 |
| T5 文档对齐 | （本批） | README / PLAN / CHANGELOG / AGENTS / docs/12 |

#### T0 · 线程安全（`win/wstr.go`，M1 + L5）

**Fixed**：
- **`wstrKeep` / `wstrKeepSlices` 是无锁包级全局 slice，而调用方横跨两个线程**
  （UI 线程 `main.go` 的 `OnStop` / `OnSend`，worker goroutine 的 `agent/loop.go` 5 处日志）。
  `logx.logf` 对**每条**日志行调 `win.Ptr()` → 两条并发 `append` 竞态。
  **`Ptr` / `Hold` / `FromCmdline` 三处 append 全部加同一把 `sync.Mutex`。**
  - **为什么必须三处都改**：v1 计划只写了 `Ptr` / `Hold`，**漏了 `FromCmdline`**（同样无锁，
    今天恰好是死代码）。而 `Hold` 今天**没有生产调用方** —— 锁的重点选反了一半。
- **危害不是"少一行日志"**：那个 `*uint16` 从此只被 `uintptr` 引用 → GC 立刻可回收 →
  UI 线程解引用野指针 → 随机花屏/崩溃。`win.KeepAlive(lp)` **救不了** ——
  它只保证本语句之前存活，而这里的解引用跨越线程边界。
- **实测丢失率**（386 多核，5 轮取区间）：2 线程 29.7%–34.4% / 4 线程 59.5%–64.3% /
  8 线程 69.7%–79.1%。
  > ⚠️ **教训**：v1 计划写"9.1%"是错的（那次可能用了 `GOMAXPROCS=1` 或串行对照）。
  > 真实场景只有 2 个调用线程，丢失率约 30% —— **非常容易复现，修复效果看得见**。

**不做**（原 v1 的"治本方案"）：改"所有权交接队列"要动 `logx` + `gui` 数据流，收益低于风险。

**Added**：`wstr_concurrency_test.go` —— 3 个并发**结构断言**（断言 `len(wstrKeep)` 增量，
不能断言 `== M`：`TestWstrKeepGrows` 已塞了 500 条抬高了 baseline，首次跑必红）。

**决策**：原"治本方案"（所有权交接队列）**明确不做** —— 要动 `logx` + `gui` 数据流，收益低于风险。

#### T1 · 可诊断性（`win/msgbox.go` + `main.go`，L1 + L5）

**问题**：PE 里出任何问题，用户拿到的信息是**零** —— 产物是 `-H windowsgui`（`os.Stderr` 全丢弃）、
`--console` 是空实现、三级日志路径都可能失败、全败时只 `return 1`。
**PE 现场：双击 smith.exe，闪一下就没了。**

**Added**：
- **`win/msgbox.go` · `MessageBoxW` 封装** —— PE 里唯一可靠的可读输出通道
  （日志文件可能写不了，stderr 被 `-H windowsgui` 丢弃，控制台不存在）。
  > ⚠️ **v1 写的是 `MessageBoxA`，会把中文显示成乱码。** A 版按系统 ANSI 代码页解释字节，
  > Go 的 string 是 UTF-8；本机 `kernel32!GetACP() = 936`（GBK）→ UTF-8 中文按 GBK 解释必然乱码，
  > **直接废掉 T1 的立身之本**。改用 W 版后**不新增任何 DLL 依赖**（产物导入表已含 `user32.dll`）。
  > ⚠️ `NewLazyProc` 在 DLL 缺失时是 **panic 不是 error** → 包装里 `recover` 成 error，
  > 否则 panic 会穿透 `boot` 的 defer 直接崩成 exit code 2（恰好是本节要消灭的行为）。
- **`main.go` 的 `fatalExit(code, format, ...)`** —— 所有非零退出码的统一收口：
  **先写日志，再弹窗**（万一弹窗失败日志还在），弹窗文本由纯函数 `buildFatalMessage` 拼
  （**抽成纯函数是为了能在无头 CI 里测** —— MessageBox 模态阻塞测不了，字符串能测），
  并在末尾附上**日志绝对路径**（PE 里用户照着它去 U 盘找）。
- **退出码 3 通路**（原来不可达）：`runWorker` 加 `defer recover()` → `atomic.StoreInt32(&workerPanicked, 1)` →
  `PostMessage(hwnd, WM_QUIT, 0, 0)` 唤醒消息循环 → `boot` 在 `win.Run()` 返回后读原子变量 → `return 3`。
  - ⚠️ **必须用原子变量不能用 channel**：`boot` 此刻**阻塞在 `win.Run()` 的消息循环里，
    根本不在 select 中**，没人读那个 channel。
  - ⚠️ **必须用 `PostMessage(hwnd, WM_QUIT)` 不能用 `PostQuitMessage`**：后者只投给**调用线程**
    的消息队列，而 worker 跑在另一个 goroutine / 另一个 OS 线程上 —— UI 线程根本收不到。
  - ⚠️ **不能用 `if r := recover(); r != nil` 判 panic**：Go 1.20 语义下 `panic(nil)` 时
    `recover()` **返回 nil**，但 panic 确实被停住了 → 用哨兵变量 `panicked := true`。
- **`win.Run()` 自己的非零返回码也走弹窗**（`RegisterClassEx` / `CreateWindowEx` / `GetMessage` 失败）。

**Removed**：
- **`--console` flag 已删除**（docs/12 §七 Q1）。产物是 `-H windowsgui`（**没有控制台**），
  `os.Stderr` 全部丢弃；而 `AttachConsole(ATTACH_PARENT_PROCESS)` **只在父进程有控制台时才成功** ——
  U 盘双击场景**必然失败**，cmd 启动场景 MessageBoxW 一样够用。
  **留一个"承诺弹控制台但什么也不做"的假开关比没有更坏**：用户以为有保护，实际没有。
  > ✅ 复审已核实**没有任何脚本传 `--console`**（`build.cmd` 不传），所以删 flag **零破坏**。

**⚠️ 两个必须加的守卫**：
1. **无头模式不能弹窗**。MessageBox 是**模态阻塞**调用，而 smoke 测试跑的是 `smith.exe --no-gui`
   → 无人点 OK → **进程永挂** → smoke 15s 超时失败。必须有 `if !noGUIMode` 守卫。
2. **只收非零退出码**。用户主动取消 key 对话框是**正常退出**（`return 0`），给它们弹
   「启动失败」是错误 UX。

#### T2 · Job 杀整棵进程树接入（`win/jobexec.go`，M2 + L1 + L5 · 风险最高）

**Added**：`src/win/jobexec.go`（~594 行）+ `jobexec_test.go`（~329 行）。
`tools/exec.go` 从 `os/exec.CommandContext` 改为 `win.StartJobCmd` —— **Stop / Esc 现在能杀整棵进程树**
（`diskpart` / `dism` / `ping` 等孙进程不再继续持裸盘句柄）。

**为什么不能直接用 `os/exec`**：已读 Go 1.20.14 `syscall/exec_windows.go`，`SysProcAttr` 共 9 个字段，
**无任何 job 能力**。必须自己调 `CreateProcess`。

**关键设计 —— 直接用 stdlib 的结构体**：
`syscall.StartupInfo` / `syscall.ProcessInformation` / `syscall.SecurityAttributes` / `syscall.CreateProcess`。
> ✅ **这是本次审阅最有价值的一条建议**：v1 计划写"手写 STARTUPINFOW（68 字节）+ PROCESS_INFORMATION
> + SECURITY_ATTRIBUTES"是最费事又最危险的路线。改用 stdlib 后，**§S1 那条"含 64 位成员的手写结构体"
> 铁律在本批根本不触发**，不需要新增结构体尺寸门禁 —— 风险降一个数量级。

**Job Object 装配顺序（不可换）**：
`CreateProcess(CREATE_SUSPENDED)` → `CreateJobObject` → `SetKillOnJobClose(KILL_ON_JOB_CLOSE)` →
`AssignProcessToJobObject` → `ResumeThread`
> **先 Resume 再 Assign 会留一个窗口期**：子进程已经跑起来了却还没进 job，这段时间里 Stop 杀不掉它，
> 而它可能已经 fork 出孙进程。`ResumeThread` 成功后**必须立刻关 `hThread`**。

**管道**（v1 写反了，两个错误叠加）：
> ⚠️ **① 前提错**：`CreatePipe` 的两个句柄在 `sa=NULL` 时**默认就不是可继承的**
> （MSDN：*"If NULL, the handle cannot be inherited"*）。
> **② 方向错**：写端（stdout/stderr）是**子进程要用的那个**，必须可继承 ——
> 把它设成不可继承，子进程拿到的是空 stdout/stderr。
> **正确写法**（照抄 Go 自己的实现）：`sa = SecurityAttributes{InheritHandle: 1}` →
> `CreatePipe(&rd, &wr, &sa, 0)` → 可选地把父进程自己那份读端 `SetHandleInformation(rd, HANDLE_FLAG_INHERIT, 0)`。
> ⚠️ 而且 **`CreateProcess` 的 `bInheritHandles` 必须传 `TRUE`**，不传则上面全白做。
> ⚠️ `bInheritHandles=TRUE` 会继承父进程**所有**可继承句柄。Go 的白名单解法
> （`PROC_THREAD_ATTRIBUTE_HANDLE_LIST`）**Win7 不可用**，PE 里不能依赖。

**排空 goroutine**（v1 漏了，会在项目已知故障上死锁）：
> ⚠️ 匿名管道缓冲约 64KB，一旦写满，子进程 `WriteFile` 阻塞 → 永不退出 →
> `WaitForSingleObject(hProc, INFINITE)` 永远等不到 → **整个 exec 工具挂死，连 60s 超时都救不了**。
> 而 `tools/exec.go` 注释里**明确记录了这个真实故障**：`for /L %i in (1,1,3000000) do @echo ...`
> 峰值 14.7MB + 43MB heap。**这个场景必中。** `os/exec` 没这问题是因为它内部为每个 pipe 起了 copy goroutine。
> 落地：两个 `io.Copy` drain goroutine + `sync.WaitGroup`（`tools/exec.go` 里
> `jc.Wait()` 之后 `drainWG.Wait()`，否则会丢掉尾部输出）。

**`CreateProcessW` 的坑**：传了 `lpApplicationName` 时**不做 PATH 搜索** ——
`ExePath` 必须给绝对路径，否则只在当前目录找。

**降级链（T2-4）**：`AssignProcessToJobObject` 失败时不能变成"没有中止机制"：
```
TerminateJobObject 成功       → 整棵树已死
       ↓ 失败
KillTreeSelfContained(pid, name)  ← M2 双层防护（照抄 spike，不要重写）
       ↓ 失败
OpenProcess + TerminateProcess    ← 只杀直接子进程
```
**日志里必须明确记录走了哪条**（PE 现场无法复现，日志是唯一线索）。

**⚠️ 仍需真机验证**：**"Win7 上 Assign 会不会失败"至今没有任何真机实测。**
`docs/07` P0-5 只在 **Win11** 上测过（Assign 成功，因 Win11 支持嵌套 job）；
那句 `Access is denied.` 来自 Win11 本机的 `CREATE_BREAKAWAY_FROM_JOB` 被拒，**不是** Win7 的 Assign 失败。
**所以 Win7 上的降级行为是"预期会降级"，不是"已验证会降级"。**

**未做**（本批遗留，不在已完成范围）：
- `win/proc.go` 的 `KillTreeSelfContained` 签名**仍是 `(int, []string)`**，没有改成 `(int, error)`
- kill 轮次之间**没有补 `sleep(300)`**（`spike/job` 有、产品代码没有）→ 三轮预算可能被同一批
  "正在终止"的进程吃光
- **`tools/run_script.go` 仍在用 `os/exec.CommandContext`**，没接 Job

#### T3 · 功能补齐：`diskinfo` + `sysinfo` + `kill`（`tools/sysinfo.go`，L1）

**背景**：`PLAN.md` 写的 Phase 2 验收标准是"模型自动调 `diskinfo`"，**而 `diskinfo` 从未被实现过**。
`win/sysinfo.go` 里 8 个底层函数全部写好、测过，**一个都没暴露成工具**。

**Added**：
- **`diskinfo`** —— 逻辑盘容量/剩余/类型。✅ **可用**。无参则列所有盘；有参接受 `C:` / `C:\` / `C:\Windows`。
  空光驱 / 断开的网络盘容量为 `?`（**不是错误**）。新 proc `GetDiskFreeSpaceExW`（1 个）。
- **`sysinfo`** —— OS 版本 / 机型 / 内存 / 计算机名 / 用户名 / 已运行秒数。✅ **可用**，0 个新 proc（全用现成的）。
- **`kill`** —— ⚠️ **已注册但未接入**。`killTool.Run` **无条件返回 `errKillNotWired`**。
  > **为什么不先接一个能跑的版本**：kill 的全部价值在于 M2 双层 PID 复用防护。少了它，
  > "杀 1234"可能杀掉 PID 已被复用的**无辜进程** —— 在 PE 现场那等于数据事故。
  > **宁可诚实报"未接入"，也不要一个看起来能用但会杀错进程的 kill。**
  > 它仍然注册，是为了让工具数稳定在 17，T5 文档对齐时不用再改数字。
  > **当前版本请用 `exec` 工具，或 `taskkill /T /F /PID <pid>`。**
- `win.TickCount()` 改签名 `(uint32, error)`（原来返裸 `uint32` 违反 L1，唯一调用方是 `sysinfo_test.go`）。
- 三处工具清单同步到 17：`tools/tools_test.go` 的 `allToolNames` / `tools/meta.go` 的
  `expectRegisteredTools` / `src/test/e2e_test.go` 的 `TestE2E_All17ToolsRegistered`（**一并重命名**）。

**⚠️ 撤销 v1 的 T3-2（它会把能用的函数改坏）**：
v1 写"`MemoryStatusEx` 的成员是 `SIZE_T`，改成 `uintptr` 后 x86=36 与 Win32 一致"——
**三个断言全错，照做会让 `MemoryStatus()` 必然失败**。
SDK `um/sysinfoapi.h` 里成员是 **`DWORDLONG`（`unsigned __int64`）不是 `SIZE_T`**；
MSVC x86 把 `__int64` 对齐到 8 → `4+4+7×8 = 64`，**x86 与 x64 完全相同**。

| | cbLength | 结果 |
|---|---|---|
| 现状（`uint64`）| 64 | ✅ `ret=1` TotalPhys=68.6GB |
| v1 处方（`uintptr`）| 36 | ❌ `ERROR_INVALID_PARAMETER` |

且 v1 **自相矛盾**：它自己引用了"64 成功、36 失败"的实测数据，处方却写"改成 36"。
**那份数据恰好证明 36 是错的。** → **结论：现状是对的，不改。** 并加了一条**架构无关**的断言卡住后人再犯。

**Win32 常量的位置**（C1 边界）：`DRIVE_*`（0–6，对照 `winbase.h`）与
`PROCESSOR_ARCHITECTURE_*`（0/5/6/9/12，对照 `winnt.h`）**故意放在 `tools/sysinfo.go`** ——
`win/` 的每个 Win32 常量都要在 `win/consts_test.go` 登记（`expectedCount` 门禁），
本批不改那个文件。它们由 `tools/sysinfo_test.go` 单独断言。

#### T4 · 健壮性收尾（`win/proc+job+gui+sysinfo` + `main.go`，L1 + M2 · 12 项中的 9 项）

**Fixed（已落地 9 项）**：
| # | 改动 | 为什么 |
|---|---|---|
| T4-1 | `CreateToolhelp32Snapshot` 失败判 `h == 0 \|\| h == ^uintptr(0)` | 失败返 `INVALID_HANDLE_VALUE`(0xFFFFFFFF) 不是 0，只判 0 会把真失败误报成"Process32FirstW 失败" |
| T4-2 | `proc_test.go` 填真实映像名（原来传 `expectName=""`） | `expectName != ""` 保护是 M2 第 (a) 层，**空串把它整层短路**，等于 M2 在测试里没被执行 |
| T4-3 | `TestJobSizeContract_PlatformDoc` 补 `t.Fatalf` 断言 112/144/16 | 原来纯 `t.Logf` **零断言** —— §S1 铁律正是被 108 vs 112 的错位坑出来的（KILL_ON_JOB_CLOSE 静默失效） |
| T4-4 | `--no-gui` 从固定 `time.Sleep(5s)` 改 done channel 驱动，5s 只作上限 | 实测 smoke 5s **全部耗在固定 sleep 上** |
| T4-5 | `smoke_bin_test.go` 断言换成 `boot start` + `agent loop ready` + `LLM 未配置` + `no-gui smoke done` | 原来断言 `strings.Contains(content,"llm")` —— **被 `!! llm: 缺配置` 这条错误日志满足**。<br/>⚠️ v1 想换的 `你 > ver` / `ver turn=` **在 smoke 环境里不会出现**：smoke 跑在空 temp 目录没有 `smith.ini` → `llmClient == nil` → `runWorker` 直接 `continue`，**`loop.Run` 根本不被调用**；全仓 grep `你 >` **0 命中** |
| T4-8 | 删掉 `TestE2E_WinKeepAliveNoCrash` | 分配约 1MB 垃圾（1KB×1000）但**从不调 `runtime.GC()`**、无内容断言。真正的 M1 门禁是 `wstr_test.go` 的 `TestPtrNotGCed` |
| T4-10 | `onSize` 加 `minWinW = 320` clamp | `uintptr(int32(w)-130)` 无 clamp，w<130 时 386 上变巨大正数、amd64 上是符号扩展的负数，**两架构行为不一致** |
| T4-11 | `TickCount()` 改 `(uint32, error)` | 返裸 `uint32` 违反 L1 |
| T4-12 | 日志区宿主控件从普通 `EDIT` 换成 **`RichEdit20W`**（`LoadLibraryW` 失败则**显式降级回 EDIT 并打日志**） | `EM_SETCHARFORMAT` 往普通 `EDIT` 发**返回 0**（RichEdit 专有消息）→ **think 染色是实测无效的 no-op**。常量 `0x0444` 本身是对的（richedit.h:115 `WM_USER+68`，已定点验证 0/100/200 三次读回一致），**只是宿主控件选错**。⚠️ **不要**为了字号把 `riched20.dll` 变成硬依赖（它依赖 `msls31.dll`，精简 PE 可能缺）—— 纯观感改进不值得牺牲 PE 兼容性 |

**未落地 3 项**（如实记录，别当成已做）：
- **T4-6**（`appendLog` 抢走用户选区 / 截断时在选区上 `EM_SETSEL + WM_CLEAR`）——
  **未做**。`win/gui.go` 里**只用了 `EM_SETSEL`，没有 `EM_GETSEL`**，即没有做选区记录/恢复。
  且**需真机 desktop 才能验**。
- **T4-7**（`win.Run()` 返裸 `int` 违反 L1）—— **未做**，`gui.go` 现在仍是 `func Run() int`。
- **T4-9**（`TestSizeProcessEntry32_PlatformDoc` 零断言）—— **未做**，仍是纯 `t.Logf`，
  注释里自己写着"**没有**断言 amd64 尺寸"。而 `processEntry32` 恰好是 M2 依赖的结构体。

#### 踩过的坑（留给后来人）

1. **`build.cmd` 必须是 CRLF。** 写成 LF 时 cmd.exe 会吞掉每行前几个字节，在解析器里空转 ——
   **表现是构建永久挂死**（不报错、不退出）。核对：
   ```powershell
   $b=[System.IO.File]::ReadAllBytes('build.cmd'); $crlf=0;$lf=0
   for($i=0;$i -lt $b.Length;$i++){ if($b[$i] -eq 10){ if($i -gt 0 -and $b[$i-1] -eq 13){$crlf++}else{$lf++} } }
   "$crlf CRLF / $lf LF-only"   # LF-only 必须是 0
   ```
2. **提交时用 `git add --update` 会漏掉新增文件。** 上一轮（`4ecd766`）宣称"S3 capWriter 限流已落地"，
   但定义 `capWriter` 的 `src/tools/limited_writer.go` **从未入库** → **`HEAD 不可编译`**
   （`git archive HEAD` 干净检出后 `go build ./src/...` 报 `undefined: newCapWriter`）。
   任何 clone 都过不了，bisect / revert 全失效。
   > **教训**：S6 建立"每批都过 `build.cmd test`"的纪律时，那条纪律验的是**工作区**不是**版本库**。
   > **工作区绿 ≠ HEAD 绿。** 新增文件必须显式 `git add <file>`，收尾用 `git archive HEAD` 复验。
3. **`CreateProcessW` 传了 `lpApplicationName` 时不做 PATH 搜索。** `ExePath` 必须给绝对路径。
4. **管道读端/写端哪个该设不可继承，设反了子进程拿不到 stdout。**
   写端是子进程要用的那个，必须可继承；设反的表现是"命令跑了但输出全空"。
   前提还有一条：`sa=NULL` 时 `CreatePipe` 的两端**默认就不是可继承的**。
5. **`MEMORYSTATUSEX` 的成员是 `DWORDLONG` 不是 `SIZE_T`，不要"修"成 `uintptr`。**
   x86/x64 都是 64 字节；"修"成 `uintptr` 会让 `MemoryStatus()` **必然失败**
   （`cbLength=36` → `ERROR_INVALID_PARAMETER`）。
6. **`kernel32!IsProcessInJob` 会猝死进程（`0xc0000005`），Go 层没有 error / panic / 日志。**
   探针实测（go1.20.14 / CGO_ENABLED=0 / 非管理员 / Win11，386 + amd64 双架构）：
   `hJob=NULL` 崩（`hProcess` 是真句柄 / pseudo-handle 都崩），**`hJob` 是有效 job 句柄也一样崩**；
   只有 `hJob` 是个它能立刻判定为无效的句柄（假句柄 / Event 句柄）才干净返回
   "The handle is invalid."。崩点在"真正去比对进程/job 归属"的代码路径上。
   → **`win.IsProcessInJob` 现在的实现是诚实降级，恒返 `(false, nil)`，从不探测。**
   调用方若需要"是否已在 job 里"，**不要把 `false` 当成"已验证不在"**，也别试图"修"这个 wrapper。
7. **`PostQuitMessage` 只能投给调用线程。** worker 跑在另一个 goroutine / 另一个 OS 线程上，
   必须用 `PostMessage(hwnd, WM_QUIT, ...)` 唤醒 UI 线程。

---


### 文档对齐（S8 批 · docs/11 §S8-1..S8-11）

本次整改**只改文档，不改代码**。逐条都对照 `src/` 实测核实，不采信旧文档的数字。

**Fixed（文档与代码的严重脱节）**：

| 项 | 问题 | 实测结论 |
|---|---|---|
| S8-1 | README 特性表两个 ✅ 是假的：「Job Object 杀整棵进程树」「Esc 中止 —— 杀当前工具调用 + 取消 LLM 请求」 | 全仓 `CreateJobObject` / `SetKillOnJobClose` / `AssignProcessToJobObject` / `KillTreeSelfContained` **生产零调用点**，唯一调用方是各自 `_test.go`。已改为「取消信号已贯通到工具层；Job 杀树代码就绪但尚未接入 exec/run_script（见 docs/11 §S1-2）」 |
| S8-2 | **隐私**：`PLAN.md` + `smith.ini.example` 仍是 `owl.key` / `owl.ini` / `owl.exe` | `cfg.DefaultKeyFile = "smith.key"`，旧名 key 文件不被 `.gitignore` 覆盖 → **`git add .` 会把 API key 提交进仓库**。已全部改为 `smith.*`（`owl` 仅保留在 docs/03 命名讨论与本条历史记录中） |
| S8-3 | README 产物体积 / 测试数、CHANGELOG 仓库统计全错且互相矛盾 | 已改为**量级描述 + 实测命令**，不再手写数字（见下「仓库统计」） |
| S8-4 | `.workbuddy/audit/*.md` 被当权威来源引用 | `.gitignore:2` 排除整个 `.workbuddy/`，clone 下来全是死链。已改为指向 `docs/11`，并显式标注"早期版本引用过该路径，不随仓库分发" |
| S8-6 | PLAN §2 目录结构列的 5 个文件全不存在 | `win/api.go` / `win/dpi.go` / `tools/file.go` / `tools/sys.go` / `tools/vision.go` 从未存在。已按 `src/` 实际文件重写，并加「与 §3 阶段计划的对应」表标注哪些**未实现** |
| S8-7 | PLAN B6 权威字段表缺 `provider` | `cfg/ini.go` 已实现 `[llm] provider`（openai/anthropic/deepseek），已补入字段表 |
| S8-8 | AGENTS 硬规则表符号名偏差 | M1 的 `strKeep` → 实际 `wstrKeep` / `wstrKeepSlices`；M2 的 `job.go + proc.go` → **两层防护全在 `proc.go`**，job.go 只是 Job API 封装 |
| S8-9 | README/AGENTS 目录结构过时 | docs 说 7 篇（实际 10 篇）、spike 说 5 个（实际 9 个） |
| S8-11 | commit 格式被 `[shared]` / `[hta]` 前缀破坏 | 已在 AGENTS §5/提交约定标注"不要加前缀" |

**Added（AGENTS 硬规则表从 5 条扩到 9 条）**：

- **B2** `runtime.LockOSThread()` 必须是线程入口第一行（含 keydialog 子消息循环）
- **V1** `win/` 的 `go vet` 必须用 `-unsafeptr=false`（uintptr ↔ unsafe.Pointer 互转是 Win32 互操作必需的）
- **S1**（本次新学到的坑）**含 64 位成员的手写 Win32 结构体有 386 对齐风险 → 手工构造字节缓冲 + 显式偏移**。来源：386 上写 108 字节时 `SetInformationJobObject` 返 `ERROR_BAD_LENGTH` 但不抛错，`KILL_ON_JOB_CLOSE` 静默失效。现有实现见 `win/job.go` `buildJobExtLimitInfo`
- **C1**（本次新学到的坑）**Win32 常量必须对照 SDK 头文件 + 实测，禁止凭记忆写**。真实踩坑：`WM_TIMER` 曾写成 `0x0118`（那是 `WM_SWITCHWINDOW`）。门禁见 `win/consts_test.go`（68 条断言 + `expectedCount` 覆盖度自检）

**Changed**：

- `smith.ini.example`：`owl.*` → `smith.*`；`vision` / `imghistory` 明确标注 **NOT IMPLEMENTED**（能解析但无消费方，配了不生效）
- README / CHANGELOG / PLAN / AGENTS 的产物体积改为「约 5~6 MB」+ 注明由 `build.cmd` 实测，**不再手写字节数**
- README / CHANGELOG 的测试数改为「以 `go test -list` / `go test -v` 输出为准」+ 给出核对命令
- README 特性表新增 ⛔ 条目显式列出**未实现**的能力（`screenshot` 视觉通道、`sysinfo/diskinfo/netinfo/kill`）
- README 阶段进度：Batch 1 第 1 条（H-1 OEM→UTF8，commit `4f7ced6`，`win/oem.go`）从 `[ ]` 改为 `[~] 部分完成`，并逐条列已落地/未落地；补 S6 产物门禁、S8 文档对齐两条 `[x]`

---

## [Unreleased · 上一批]

### 下一批
- **Batch 1**（LLM 适配层 9 条 ~80 行）：H-1 OEM→UTF8 + M-9 ErrMaxTurns + L-5 空响应 + H-2 OpenAI image wire + H-3 重试空体 + H-4 CheckRedirect + M-6 attach_image + M-7 think 剥离 + M-8 空 tool 占位
  - **H-1 OEM→UTF8 已落地**（commit `4f7ced6`）。其余状态见 docs/11 §S4。
- **Batch 2**（Win 互操作 + Job 接入 ~150 行）：H-6 wstrKeep 加锁 + L-1 泄漏 + H-8 IsProcessInJob 签名 + H-7 Job 杀树接入 exec/run_script（大改 + spike 回归）
  - **ctx 贯通已落地**（docs/11 §S1-1）；**Job 接入未做**（§S1-2）。
- **Batch 3**（GUI 交互 5 条 ~40 行 / **同 PR atomic**）：M-2/M-3/M-4/M-5 + H-5
- **Batch 4**（杂项 / 安全 / 健壮 ~120 行）：M-1 + M-10/M-11 + M-12/M-13/M-14 + L-2 kill 工具 + L-3/L-4 + L-7/L-8

**整改依据与实施记录**：[`docs/11-审计整改计划.md`](./docs/11-审计整改计划.md)。
> ⚠️ 本节此前引用 `.workbuddy/audit/2026-09-11-P1-audit.md` 与 `.workbuddy/audit/2026-09-11-P1-verify.md` —— **这两个文件已被 `.gitignore` 排除（`.gitignore:2`），不随仓库分发**，clone 下来是死链。整改依据已整理进 `docs/11`。

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
| P1-9b | `tools/` 其余 12 工具（`read`/`write`/`net`/`ps`/`meta` 共 12；**`screenshot` 未做**，至今未实现） |
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
| 2026-09-11 | **rename owl → smith**（P1-23~28）。⚠️ 当时漏了 `PLAN.md` + `smith.ini.example`（2026-09-28 补） |
| 2026-09-11 | **P2-0 5 CRITICAL 修复**（verifier 复核全 CONFIRMED） |
| 2026-09-11 | **P2-1 GUI 日志优化**（word-wrap + 60000 截断 + Copy/Clear/Save + 时间戳） |
| 2026-09-11 | **P2-2 think 块单独缩字号**（EM_SETCHARFORMAT CHARFORMATW 92 字节） |
| 2026-09-11 | **P2-4 排版修复**（双重 [I] 去除 + 防御性 \r\n + 5pt 字号） |
| 2026-09-28 | **H-1 OEM→UTF8 落地**（commit `4f7ced6`，`win/oem.go`） |
| 2026-09-28 | **4 片并行审计 → docs/11 整改计划**（S0~S8 八批） |
| 2026-09-28 | **S6 产物门禁**（commit `4581a9d`：`verify-pe.ps1` + 68 条 Win32 常量门禁 + gofmt 门禁） |
| 2026-09-28 | **S8 文档对齐**（README / PLAN / CHANGELOG / AGENTS / smith.ini.example） |
| 2026-09-28 | **T-minus 基础设施**（commit `642a06f`：补交漏入库的 `limited_writer.go`，恢复 HEAD 可编译） |
| 2026-09-29 | **T0 线程安全**（`637d0f8` + `338ae9a`：`wstrKeep` 加锁） |
| 2026-09-29 | **T1 可诊断性**（`c5d82da`：`win/msgbox.go` + `fatalExit` + 退出码 3 通路 + 删 `--console`） |
| 2026-09-29 | **T2 Job 杀树**（`40cfa2e`：`win/jobexec.go` 接入 `exec`） |
| 2026-09-29 | **T3 功能补齐**（`c5d82da`：`diskinfo` / `sysinfo` / `kill`，工具数 14 → 17） |
| 2026-09-29 | **T4 健壮性收尾**（`7fada9b`：12 项中的 9 项） |
| 2026-09-29 | **T5 文档对齐**（本批） |

## 仓库统计

> **2026-09-29 重新实测口径**。下面刻意不写死用例总数与产物体积 ——
> 精确数字随每次提交漂移，写进文档必然腐烂。核对命令见各条目。

- **commit 数**：`git rev-list --count HEAD`（本次核实 **55**）
- **测试包**：6 个 —— `agent` / `cfg` / `logx` / `test` / `tools` / `win`
  - **用例数以 `go test -list '.*' ./src/... | Select-String -Pattern '^Test'` 的输出为准**
  - 本次核实（顶层 `Test*` 函数数，**不含 `t.Run` 子测试**，共 **209**）：
    `agent` 50 / `cfg` 17 / `logx` 5 / `test` 10 / `tools` 68 / `win` 59
- **主产物**：`dist/smith.exe`（386）+ `dist/smith64.exe`（amd64），各**约 5.5~6 MB**
  - 精确字节数由 `build.cmd` 收尾的 `verify-pe.ps1` 实测输出（docs/11 §S6-1）
- **spike 探针**：**9 个**，产物 `spike386`(9) + `spike64`(9) —— Phase 0 五项（job/gui/hello/https/dlls）+ 后续四项（hta/oem2utf8/richedit/screenshot）
  - ⚠️ **spike 产物不在 `build.cmd` 门禁范围内**（详见 AGENTS.md「已知陷阱」5）
- **工具**：**17 个已注册**（`exec` / `run_script` / `help` / `selftest` / `ls` / `cat` / `grep` / `find` / `write` / `edit` / `append` / `http_get` / `https_get` / `ps` / `diskinfo` / `sysinfo` / `kill`）
  - **其中 16 个可跑**；`kill` **已注册未接入**（`Run` 恒返回 `errKillNotWired`）
- **未实现**：`screenshot` 等 Phase 4 视觉工具、`netinfo` / `download` / `hash`
- **0 依赖外部库**（无 CGO / 无 -race / 无 go.mod 依赖）
- **GUI 100% 原生 Win32**（user32 内建控件 + gdi32 字体，不引 comctl32 / 浏览器 / 任何库）

## 待办（按优先级）

**权威来源：[`docs/11-审计整改计划.md`](./docs/11-审计整改计划.md)（S0~S8）+
[`docs/12-收尾与功能补齐计划.md`](./docs/12-收尾与功能补齐计划.md)（T0~T5）。**

- [x] **S0 止血批**（6 条，含 S0-5 `WM_TIMER` 常量 —— 提前并入 S6）
- [~] **S1 接线批** —— ctx 贯通已落地（§S1-1）；**Job 杀树已接入 `exec`**（docs/12 T2 / commit `40cfa2e`）。**`run_script` 未接线**
- [x] **S2 会话记忆批**（S2-1 Loop 提到 `for` 外 / S2-2 tool 首条回退 / S2-3 空 history 保 system）—— 落地于 `4ecd766`
- [x] **S3 输出上限批**（512KB 硬上限）—— 落地于 `4ecd766`
- [~] **S4 LLM 适配层批** —— S4-1~S4-5 / S4-10 已落地；S4-6 tool_calls 兜底等未做
- [x] **S5 工具正确性批** —— 落地于 `4ecd766`
- [x] **S6 产物门禁批**（`verify-pe.ps1` + smoke 新鲜度 + 68 条常量门禁 + gofmt）
- [x] **S7 GUI 交互批** —— S7-1 `IsDialogMessage` 已接；**S7-6 `--console` 已删除**（docs/12 T1-4，commit `c5d82da`），改用 `MessageBoxW`
- [x] **S8 文档对齐批**
- [x] **T0 线程安全批**（`wstrKeep` 加锁，commit `637d0f8`）
- [x] **T1 可诊断性批**（`win/msgbox.go` + `fatalExit`，commit `c5d82da`）
- [x] **T2 Job 杀树批**（`win/jobexec.go` 接入 `exec`，commit `40cfa2e`）
- [~] **T3 功能补齐批** —— `diskinfo` / `sysinfo` ✅；**`kill` 已注册未接入**（commit `c5d82da`）
- [~] **T4 健壮性收尾批** —— 12 项中 9 项已落地；**T4-6 / T4-7 / T4-9 未做**（commit `7fada9b`）
- [x] **T5 文档对齐批**（本批）
- [ ] **`kill` 工具接入** —— 前置：`win.KillTreeSelfContained` 签名从 `(int, []string)` 改成 `(int, error)`
- [ ] **`run_script` 接 Job** —— 改走 `win.StartJobCmd`
- [ ] **T4 剩余 3 项**（T4-6 选区保护 / T4-7 `Run() (int, error)` / T4-9 `processEntry32` 尺寸断言）
- [ ] **真机 PE 测试**（spike/{job,gui,hello} 拷 U 盘进 Win7/10/11 PE 验）
  - 含 docs/12 §八列的 6 项：Win7 无嵌套 job 的降级行为 / `riched20.dll` 与 `GetDiskFreeSpaceExW` 在精简镜像的存在性 / MessageBoxW 在 PE 上是否可用 / T4-6 选区保护 / `MemoryStatusEx` 在真 32 位 Windows 上的行为 / keydialog 线程迁移

## 关键文档

- `PLAN.md` — 设计源头（v1 第四轮 + v2 复审合并的 9 条硬规则 + 字段表 §0.6 B6 + 阶段计划）
- `docs/11-审计整改计划.md` — S0~S8 批次定义 + 实施记录
- `docs/12-收尾与功能补齐计划.md` — T0~T5 批次定义 + 实施记录（T0–T4 已落地）
- `AGENTS.md` — 给 AI 编程 agent 的工作约定（**10 条硬规则** + 步间审核 + 386/amd64 + 中文 path）
- `docs/01-05, 07-10` — 专项设计（06 编号空缺）
- `spike/*` — **只读探针，9 个独立程序**（不可 import）
