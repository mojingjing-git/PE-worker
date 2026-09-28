# 09 · HTA 前端方案（IE 引擎 + 本地 HTTP）

> 状态：**已通过本机 spike 验证，待真 PE 定论** · 调研日期 2026-09-11
> 实测报告：`spike/hta/SPIKE_REPORT.md` · 演示页：`spike/hta/demo.hta` · 截图：`spike/hta/demo_shot.png`
> 对照方案：`docs/10-Sciter前端方案.md`
> **2026-09-12 审阅修订**：经 review-hta 审阅（总评：需修订），已按意见修正证据链、进程生命周期、
> logx 改造量、产品化必改项与验收清单。审阅全文：`.workbuddy/audit/2026-09-12-hta-plan-review.md`

---

## 一、一句话

smith.exe 内起一个只监听 `127.0.0.1` 的 HTTP server，**页面本身由 server 直接 serve**（同源，绕开 IE 跨域安全区），`mshta.exe` 打开它，前端用 HTML/CSS 画 UI，靠 **XHR 长轮询**跟 agent 通信。PE 镜像侧必须加 `WinPE-HTA` 可选组件。

## 二、架构

```
   ┌────────────── mshta.exe（独立进程，IE 引擎）──────────────┐
   │  app.hta                                                  │
   │   HTML/CSS（IE8 级：无 flex、attachEvent）                  │
   │   ES3 JS：XHR 长轮询 / FSO 写文件 / window.close()          │
   └───────────────▲──────────────────────┬────────────────────┘
                   │ GET /  (页面)         │ POST /api/message
                   │ GET /api/events       │ POST /api/result（FSO 被拦时的回退）
                   │                       ▼
        ┌──────────────────── smith.exe（Go 单 exe）────────────────────┐
        │ src/htasrv/  net/http server（纯 Go，无 cgo）                  │
        │   eventBus（channel + 8s hold）—— 长轮询                       │
        │ //go:embed app.hta（注入 origin）                              │
        │ agent loop / 14 工具 / LLM 适配层 —— **全部不动**               │
        │ mshta 启动失败或退出 → 自动降级到现有 Win32 GUI                  │
        └───────────────────────────────────────────────────────────────┘
```

**关键点**：页面由 server 同源提供，是规避 IE 安全区/跨域问题的最强手段（实测 http 与 file 两种方案都通过，但 PE 的 IE8 只能靠同域方案兜底）。

**进程生命周期（审阅补充，原先只写了单向）**：

| 事件 | 必须的行为 | 实现要点 |
|---|---|---|
| mshta 被用户关闭 / 崩溃 | smith 降级到 Win32 GUI（或退出） | 监控 mshta 进程句柄（WaitForSingleObject，非轮询）；注意 mshta 正常自关与被杀要区分 |
| **smith 崩溃 / 被杀** | **mshta 必须被回收**，否则 PE 上留孤儿窗口 + 下次启动端口错乱 | **双向心跳（主路径）**：smith 每 2s 往 `GET /api/heartbeat` 写一行；mshta 前端长轮询该端点，连续 3 次拿不到（6s）即 `window.close()` 自关。**启动期清扫（兜底）**：smith 启动时按固定基端口（写入 ini 的 `hta_base_port`，默认 8200）扫描并 `taskkill` 残留 mshta；再用 `hta_base_port + N`（N=0..9）找第一个可用端口。**Job Object 不作主路径**：项目 P0 结论（`docs/07-Phase0-验证报告.md`、`PLAN.md` §0.9）已证明 Win7 PE 上 AssignProcessToJobObject 被拒、无嵌套 job，仅 Win11 靠嵌套 job 才救回 → 在 PE 上不可靠，只能作 Win10/11 桌面调试期的附加保险 |
| 端口冲突 / 多实例 | 见上一行（启动期端口扫描 + 固定基端口）；htasrv 起失败即降级 | ini 写 `hta_base_port`（默认 8200）；多实例用 `SINGLEINSTANCE="yes"`（HTA 侧）+ smith 侧单实例锁 |

**输出路由（审阅指出原先的"全部不动"不成立）**：现有日志走 `logx → PostMessage(hwnd)`，HTA 模式没有 hwnd。
P3 必须给 logx 加**第二种 sink**：`logx` 输出同时投递到 `eventBus`（长轮询消费），Win32 模式下仍走 hwnd。
这是 P3 的主要工作量来源，原先估 120 行偏低。

## 三、已验证的事实（Win11 真 mshta）

> ⚠️ **数据来源有两批，不要混用**（审阅指出此前证据链断裂，已在 `SPIKE_REPORT.md §6` 补齐）：
> **① eval-hta 用 Python 替身 server**（22ms / 长轮询 ~1.5s / 80MB）→ 见 `SPIKE_REPORT.md §1`；
> **② team-lead 用真 Go 386 编译产物复测**（15ms / 2ms）→ 见 `SPIKE_REPORT.md §6`，含 curl 三接口原文与
> mshta 回传的 result.json 原文。下表混合了两批数据，已在"来源"列标注。

| 项 | 结果 | 证据 | 来源 |
|---|---|---|---|
| Go 386 server 起 loopback + 三接口 | PASS | POST 回显 `{"echo":"ping","ok":true}`；长轮询取到 queued `test` 事件；`GET /` 返回渲染页面 | ② 真 Go |
| mshta 打开同域页面 | PASS | UI 出现、无安全弹窗、跑完自关（exit 0） | ①+② |
| XHR POST round-trip | PASS | 15ms（① 用替身 server 为 22ms） | ② 真 Go |
| 长轮询 hold | PASS | 2ms 收事件（① 为 ~1.5s）；无事件满 8s 收 `noop`，不提前断连 | ①+② |
| FSO 写文件（区域策略） | PASS | `fsoOk=true`，http/file 两种模式都不弹窗 | ①+② |
| mshta 工作集内存 | ~80 MB | `GetProcessMemoryInfo` | ① |
| **引擎版本** | ⚠️ **Win11 的 mshta 是 IE11（Trident/7.0）** | UA 实测（`Trident/7.0`） | ①+② |

**结论**：happy path 在 IE11 全绿；**PE 上是 IE8（Trident/4）引擎，不能等价迁移**，必须真 PE 验证。

## 四、实施阶段

| 阶段 | 内容 | 产出 | 验收标准 |
|---|---|---|---|
| **P0 真 PE 可行性 spike** | 拷 `dist/spike386/hta.exe` + `spike/hta/app.hta` 进 PE，跑一次 | result 文件 | PE 里 mshta 存在、能开同域页面、XHR 通、记下真实 UA/引擎版本。**不通过则整条路线终止** |
| **P1 `src/htasrv/`** ~300 行 | net/http server + eventBus 长轮询 + `//go:embed app.hta` 注入 origin | 新包 + 单测 | `httptest` 覆盖 message/events/result 三接口；events 满 hold 返 noop |
| **P2 `app.hta` 前端** ~500 行 | IE8 兼容 UI：会话流、think 块、工具调用块、输入框、状态栏 | 前端页面 | 见下方 IE8 约束清单 |
| **P3 main.go 集成** ~250 行（审阅上调） | `frontend=gui\|hta`；起 server → 写 url → CreateProcess mshta（**不依赖 Job**，靠双向心跳回收）→ 失败/退出降级 GUI；**logx 加 eventBus sink**（双 sink：hwnd 与 hta channel）；mshta 侧生命周期监控 | 主流程改造 + ini + logx 改造 | ini 切换生效；kill mshta 后 GUI 自动接管不崩；**杀 smith 后 mshta 靠心跳超时自关** |
| **P4 真 PE 验收** | 见第七节清单 | 验收记录 | 清单全绿 |
| **P5 体验收尾** | server 端 markdown→HTML（**带白名单转义**）；think 折叠；Copy/Save；`attach_image` 图片回显通路 | 收尾提交 | 中文正常、长行不溢、滚动流畅、**图片能显示**、**模型输出无 XSS** |

**IE8 端约束清单**（P2 必须遵守）：
- `attachEvent` 而非 `addEventListener`；XHR 用 `new ActiveXObject("Msxml2.XMLHTTP.6.0")`
- 无 fetch / SSE / WebSocket → 只能 XHR 长轮询；`readyState===4` 才处理
- CSS 无 flex/grid → **用绝对定位 top/bottom 锚定**（本次实测：table `height:100%` 在 IE8 standards 模式失效）
- 无 ES5 数组方法 → 手写 for 循环；markdown 一律 server 端转 HTML 下发
- **markdown→HTML 必须 HTML 转义 + 白名单标签**：模型输出按不可信处理，`<script>`/`<iframe>`/`on*` 属性一律剥离，否则 IE8 的 XSS 攻击面 = 全机器沦陷（HTA 是特权上下文，FSO/ActiveX 全开）
- 单主机仅 2 并发连接 → 长轮询常驻占 1 条，**前端必须做串行队列**：发送时暂不发起长轮询，收到响应后再恢复轮询；否则 8s hold 期间用户发消息会排队卡住（spike 是串行跑的，未暴露此问题）
- **必须声明 `<meta charset=utf-8>`**，否则 IE 按 ANSI(GBK) 猜 → 中文乱码（本次实测踩中）
- **产品化必改（spike 遗留）**：`app.hta` 里 `RESULT_PATH` 硬编码 `C:\Users\wrz20\...`，PE 里不存在 → 一律改用 `%TEMP%`/`os.TempDir()` 运行时求值；spike 的所有开发机路径必须在 P1/P2 清零
- **`attach_image` 图片回显通路（复审指出原先完全没设计）**：server 把工具产生的图片存到 `%TEMP%/smith-img-<id>.png`，长轮询事件里带 `{"type":"image","path":"/img/<id>"}`，前端用 `<img src="<origin>/img/<id>">` 受控 GET。**计入 IE8 的 2 连接预算**：图片加载是浏览器并发，不占 XHR 通道；但仍要避免与长轮询同时抢连接（IE8 每主机硬上限 2）。产品级必验：中文路径图片、PE 上 %TEMP% 可写、大图不卡死 UI
- **PE 资源约束（复审指出未评估）**：①scratch space 默认 32MB —— 4 个 cab 安装后镜像增大，但运行期 mshta ~80MB + smith + server 全在内存，**32MB 够不够要 P0 验**（不够就 `dism /Set-ScratchSpace:128`）；②72h 强制重启打断长会话 —— UI 须在状态栏显示倒计时（demo.hta 已有），接近时限前提示用户保存

## 五、工作量

| 模块 | LOC | 难度 |
|---|---|---|
| `src/htasrv/` | ~300 | 低（net/http 已验证可用） |
| `app.hta` 前端 | ~500 | **高**（IE8 布局/事件/重连最耗时） |
| main.go 集成 + 降级 + **logx 双 sink** | ~250 | 中（审阅上调：logx 改造原先漏算） |
| ini/cfg | ~20 | 低 |
| DISM 加包脚本 | ~40（脚本） | 中 |
| 安全区注册（同域方案可省） | ~30 | 高（未知） |
| **合计** | **~1130 行 / 45–60 人时** | 中 |
| 长期成本 | 保留 Win32 降级 = **永久维护两套 GUI** | 别忽略 |

## 六、风险与退出判据

| 风险 | 缓解 | 退出判据（满足任一条则放弃本方案） |
|---|---|---|
| PE 的 IE8 引擎跨域/ActiveX 行为与 Win11 不同 | 同域 http 设计 + `/api/result` 回退通道 | P0 在 PE 上 XHR 被拦且无解 |
| mshta ~80MB 内存对小内存 PE 是负担 | 保留 Win32 GUI 降级 | PE 可用内存不足 |
| **smith 崩溃后 mshta 变孤儿**（审阅指出反向盲区） | 用 Job Object（M2）挂 mshta；job 不可用时启动清理残留 | PE 上 job 不可用且无法等价替代 |
| IE8 双连接下长轮询常驻 + POST 抢占 | 前端串行队列（发送时暂停轮询） | 真机上连续发消息卡死且无法缓解 |
| 镜像加不进 4 个 cab（HTA/HTA_zh-CN/Scripting/FONTSupport-zh-CN） | 换 Sciter 方案（自带引擎，不需要 OC） | 用户不能/不愿改 PE 镜像 |
| IE8 前端维护成本高 | — | P2 工期超出 1.5 倍 |

## 七、真 PE 验收清单（一条都不能跳）

```
镜像侧（用户做一次）
[ ] Dism /Add-Package WinPE-HTA.cab            → X:\Windows\System32\mshta.exe 存在
[ ] Dism /Add-Package WinPE-HTA_zh-CN.cab
[ ] Dism /Add-Package WinPE-Scripting.cab      → FSO 可用
[ ] Dism /Add-Package WinPE-FONTSupport-zh-CN.cab
[ ] 重新生成 boot.wim 并启动

运行侧
[ ] 拷 dist/spike386/hta.exe + app.hta 到同一目录
[ ] 启动 hta.exe，读 %TEMP%\hta_spike_url.txt 得到 URL
[ ] mshta 打开该 URL（若不便手动，脚本写个 .cmd 自启）
[ ] 读 %TEMP%\hta_spike_result.txt：success=true？mode=http？
[ ] UA 里 Trident/ 版本号 = ____（确认是 IE8 还是别的）
[ ] tasklist 记 mshta 工作集 = ____ MB
[ ] 中文是否乱码（确认字体 cab 生效）
[ ] 800x600 分辨率下布局是否正常

生命周期（审阅补充：原清单只验了"启动发一条"）
[ ] 手动关闭 mshta → smith 是否检测到并降级/退出，不留残窗
[ ] taskkill 强杀 mshta → 同上
[ ] taskkill 强杀 smith → **mshta 靠心跳超时（6s）自关**（无 Job，PE 不可靠）
[ ] 重启 smith → 端口不冲突（固定基端口 + 启动期扫描生效）
[ ] 连续发 3 条消息（长轮询 + POST 抢占 2 连接）→ 不卡死，证明串行队列生效
[ ] **`attach_image` 图片回显**：触发一个产生图片的工具调用 → HTA 能显示图片
[ ] **scratch 32MB 够不够**：mshta + smith + server 同跑，tasklist 记内存；不够则 dism /Set-ScratchSpace:128
[ ] **72h 倒计时**：状态栏显示，接近前提示保存
```

## 八、与现有代码的关系

- **不动**：`src/agent/`（loop/history/llm）、`src/tools/`（14 工具）、`src/cfg/`
- **改动（审阅修正：原先写成"全部不动"是错的）**：
  - `src/logx/`：加第二种 sink（eventBus），Win32 模式仍走 PostMessage(hwnd)
  - `src/main.go`：启动序列分支 + CreateProcess mshta + Job 挂接 + 降级
  - `smith.ini`：`frontend=gui|hta`，默认 gui
- **新增**：`src/htasrv/`（server + eventBus + embed）
- **保留**：`src/win/`（Win32 GUI 保留为降级通道）—— 注意这意味着**长期维护两套 GUI**，见 §五 成本备注
- 三件套修复（H-1 编码 / 字体 / RichEdit）**与本方案正交**，无论走哪条前端路线都要先做
