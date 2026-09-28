# 10 · Sciter 前端方案（自带 HTML/CSS 引擎 + 单 DLL）

> 状态：**纸面调研完成，未实测（含 PE 与本机）** · 调研日期 2026-09-12
> 对照方案：`docs/09-HTA前端方案.md`（已过 spike，但依赖 PE 镜像加 HTA 组件 + IE8 引擎）
> **2026-09-12 审阅修订**：经 review-sciter 审阅（总评：需修订，无单一判死项），已按意见更正
> **CSS 方言（`flow:` 而非 flex）**、**gdiplus/ole32 硬依赖**、purego 版本锁定、386 合规与验收门禁。
> 审阅全文：`.workbuddy/audit/2026-09-12-sciter-plan-review.md`

---

## 一、一句话

把 **sciter.dll（4–9MB，单 DLL 但**运行期硬依赖系统 `gdiplus.dll` + `ole32`/`oleaut32`，见下）** 和 smith.exe 放一起，Go 侧用 **无 cgo 的 jc-lab/go-sciter 绑定**（purego）直接创建窗口、加载 `//go:embed` 的 HTML/CSS 资源、操作 DOM —— **单进程，不需要 HTTP server，不需要 PE 加任何可选组件，不依赖 IE**。

## 二、架构

```
   ┌──────────────────────── smith.exe（Go 单 exe，单进程）───────────────────────┐
   │ src/scui/（新增）                                                            │
   │   window.New() → sciter.dll 创建原生窗口                                      │
   │   //go:embed ui/*.html|*.css   →  SciterSetHomeURL / load(this://app/...)     │
   │   Go ↔ DOM：SetElementValue / Eval / callFunction / 事件回调                   │
   │ agent loop / 14 工具 / LLM 适配层 —— **全部不动**                              │
   └──────────────────────────────┬──────────────────────────────────────────────┘
                                  │ purego（LoadLibrary + GetProcAddress，无 cgo）
                                  ▼
                    sciter.dll（**x32skia**，随 exe 放同目录 —— PE 无 GPU 必须用 skia 版，见下）
                      渲染后端：Direct2D（Vista+）/ Skia（CPU 或 OpenGL）/ GDI+（不推荐，见下）
                      ⚠️ PE 无 GPU 必须强制 GFX_LAYER_SKIA（x32skia DLL 纯 CPU 光栅），
                      **不能用 GDI+** —— GDI 不支持 OpenType 字体，雅黑会变豆腐块
                      （作者原话："GDI+ is old and buggy… use SKIA"）；D2D 仅在 PE 确认带 d2d1/d3d11 时考虑
```

**vs HTA 的三点本质差异**：① 单进程，没有 mshta/HTTP 那条链路；② 引擎自己带，PE 不用加 OC（**但 DLL 本身有系统依赖，见下**）；③ 不用跟 IE8 搏斗。

> ⚠️ **审阅更正 1（CSS）**：Sciter 4 的布局是**自己的 `flow:` 方言**，并以**翻译子集支持** `display:flex` / `display:grid`（常见用例可用，非全量 W3C flexbox；桌面媒体模型与浏览器不同，不能假定网页 CSS 原样可用）。好处是 flex/grid 级别的排版能力仍然有，但**所有 CSS 都得按 Sciter 方言重写或验证翻译是否生效**。P0 必须实测一个 `flow:` 与 `display:flex` 各写一份等价的"日志流 + 输入区 + 状态栏"布局对比渲染。
>
> ⚠️ **审阅更正 2（依赖）**：sciter.dll **硬依赖 `gdiplus.dll`（约 119 个 `Gdip*` 导入）+ `oleaut32` / `ole32`**。
> 官方说的"without external dependencies"指的是**不额外打包第三方运行库**，不是"不依赖系统 DLL"。
> 因此 §九 对比表里"PE 不用改镜像"应表述为：**不用加 WinPE 可选组件，但需确认 PE 有 gdiplus.dll；缺失则随附（gdiplus 依赖链较长，需体检）**。

## 三、事实核准（来源：sciter.com 官方 / GitHub / HN 作者原话）

| 事实 | 内容 | 可信度 |
|---|---|---|
| 依赖 | 作者原话："fully contained in sciter.dll (4.9Mb) without external dependencies" —— **但审阅核实其导入表硬依赖 `gdiplus.dll`（~119 个 `Gdip*`）+ `oleaut32` / `ole32`**；"无外部依赖"的准确含义是"不额外带第三方运行库"，**不等于不依赖系统 DLL** | ⚠️ 已被审阅修正 |
| 系统支持 | Windows XP 起（GDI+ 后端）；Vista–Win10 用 Direct2D；**skia 版 DLL 在没有 D2D 的系统自动走 Skia 后端** | 高（官方 crossplatform 页） |
| 32 位 | `bin/32/sciter.dll`（D2D+GDI+）与 `bin.win/x32skia/sciter.dll`（含 Skia）均有 x32 构建 | 高（官方路径） |
| Go 绑定 | **jc-lab/go-sciter** = "go-sciter without cgo (windows only)"。审阅核实其 Windows 路径走 `x/sys/windows` + `lxn/win` + **purego.SyscallN**；**锁 purego v0.7.1**（该版本支持 386 的 SyscallN/NewCallback，go.mod 要求 go 1.18）；jc-lab go.mod 要求 go 1.20；支持 Sciter 4.0.0.0+ | 中高（README 陈旧仍写 cgo 步骤，以 `feat: no-cgo` 提交为准） |
| ⚠️ purego 锁定 | **purego 当前 head 要求 Go 1.25**，因此 jc-lab/go-sciter **不能升级 purego**，否则破坏本项目的 Go 1.20 铁律 → 依赖树被钉死在 v0.7.1；**386 下 SyscallN 不支持整数+浮点混合参数**（386 无 RegisterFunc 兜底），含浮点参数的 Sciter API（如 device-resolution）需 P0 单独验证 | 高（审阅新发现） |
| 不支持的 API | Sciter Node API、TIScript 引擎 API（不影响：我们可以完全不用脚本，纯 Go 驱动 DOM） | 官方 |
| 许可 | Sciter 4.x 二进制库"对商业与非商业均免费"（旧 EULA）；**新版本（5.x / Sciter.N）许可有变** | ⚠️ 需自行确认后再发版 |

## 四、实施阶段（每阶段都有可证伪的验收）

| 阶段 | 内容 | 产出 | 验收标准（不满足即止损） |
|---|---|---|---|
| **P0 可行性 spike** 半天 | ①下载 sciter-sdk，**取 x32skia（含 Skia 后端）**；②`spike/sciter/main.go` 用 jc-lab/go-sciter + Go **1.20** 编 **386** 跑 hello window；③本机通过后再拷 PE 跑 | `dist/spike386/sciter.exe` + x32skia DLL | **门禁**：386 exe 能建窗 **且能写一次 DOM**；`flow:` 与 `display:flex` 各写一份等价布局对比渲染；**强制 `GFX_LAYER_SKIA`（x32skia DLL，CPU 光栅）能渲染 + 中文（OpenType 字体）不豆腐块**；**含浮点参数的 API（如 device-resolution）真跑通**（386 SyscallN 混合类型限制）；列出 sciter.dll 实际导入依赖表 |
| **P0.5 依赖体检**（量化门禁） | 解析两个 DLL 的导入表，逐个对照 PE 是否有 | 依赖清单 + 判定 | **量化门禁**：缺失的 DLL 必须满足"总数 ≤ 3 且每个可随附（随附后 LoadLibrary 通过）"；**gdiplus/ole32/oleaut32 任一缺失且随附后仍加载失败 → 退出** |
| **P1 资源与骨架** ~150 行 | `//go:embed ui/` + Sciter home URL + 建窗/大小/DPI + 关闭退出清理 | `src/scui/shell.go` | 窗口能开、CSS 生效、退出无残留进程；**SciterAPI 结构体加双架构 `unsafe.Sizeof` 断言**（386 对齐硬规则） |
| **P2 前端页面** ~400 行 HTML/CSS | 会话流、think 块、工具调用块、输入区、状态栏；**用 Sciter `flow:` 方言排版（不是 flex）** | `ui/*.html|css` | 800×600 不溢出；中文正常（字体另需 PE 的中文字体） |
| **P3 事件桥** ~250 行 | Go→DOM（追加消息、流式更新）、DOM→Go（发送/停止按钮、输入回车）、`attach_image` 图片回显 | `src/scui/bridge.go` | 一条完整会话走通；Stop 能取消；**图片回显需先确认 Sciter 侧方案（无原生封装，可能要用 SciterDataReady 塞字节流）—— 未证实项，P0 一并验证**；`purego.SyscallN` 返回值按 L5/M1 透传为 error |
| **P4 集成与降级** ~120 行 | `frontend=win32\|sciter`（默认 win32）；**sciter.dll 缺失/加载失败 → 自动回落现有 Win32 GUI** | main.go + ini | 删掉 DLL 后程序仍能用（GUI 模式） |
| **P5 真 PE 验收** | 见第七节；**中文 IME 输入列为门禁**（相对手搓 Win32 的真实优势） | 验收记录 | 全绿 |

## 五、工作量

| 模块 | LOC | 难度 |
|---|---|---|
| P0 探针 | ~120 | 中（DLL 获取 + Go 1.20 386 编译是未知项） |
| 窗口/资源骨架 | ~150 | 中 |
| 前端 HTML/CSS | ~400 | **中**（比 IE8 版省：flex 可用、CSS3 基本可用） |
| 事件桥（Go↔DOM） | ~250 | **中高**（API 小众、文档少、示例需翻源码） |
| 集成 + 降级 + ini | ~120 | 低 |
| 构建/打包脚本 | ~60 | 低 |
| **合计** | **~1500 行 / 50–75 人时**（含 PE 渲染后端调试 +20 缓冲） | 中 |
| 长期成本 | 保留 Win32 降级 = **永久维护两套 GUI**；Sciter 4.x 是免费终点 + jc-lab 单人维护 → **技术债永久** | 别忽略 |

> 与 HTA 相比：前端不用跟 IE8 搏斗（`flow:` 方言仍比 IE8 舒服），主要成本转移到
> "Sciter Go API 文档少 + DLL 依赖/许可/获取 + 依赖树被 purego v0.7.1 钉死"。

## 六、风险与退出判据

| 风险 | 说明 | 退出判据（满足任一条则放弃） |
|---|---|---|
| **渲染后端在 PE 不可用** | Direct2D 在 PE 基本没有；**必须强制 `GFX_LAYER_SKIA`（x32skia DLL，CPU 光栅）**。**不能用 GDI+**——GDI 不支持 OpenType 字体，雅黑会变豆腐块（作者明确不推荐 GDI+） | P0：强制 SKIA 后端仍无法渲染，或中文 OpenType 字体变豆腐块 → 退出 |
| **x32 DLL 依赖的 PE 里没有** | 例如 gdiplus.dll 在某些精简镜像缺失（可随附；但 gdiplus 依赖链较长） | 依赖体检缺失关键项且无法随附补齐 |
| **许可** | Sciter 4 二进制免费的说法来自旧 EULA；新版本政策变动 | 法务/许可不可接受（尤其若要分发） |
| **库小众、维护不确定** | jc-lab fork 单人维护，README 陈旧 | P0 在本机 386 上就编不过/跑不起来 |
| 体积 | 单 exe + ~5MB DLL（UPX 后 ~2MB），对 U 盘无压力，但大于现状 | 非阻断 |
| **打破"单 exe"形态**（审阅指出） | 分发从 1 个文件变成 2–3 个（exe + sciter.dll [+ gdiplus]），与项目"丢进 PE 就能跑的单文件"定位冲突 | 若用户坚持单文件 → 本方案出局 |
| **双前端长期维护**（审阅指出） | 保留 Win32 作降级 = 永远维护两套 GUI，成本可能高于 Sciter 本身 | 若不愿维护两套 → 需二选一而非并存 |
| **技术债永久**（审阅指出） | Sciter 4.x 是免费终点（5.x/Sciter.N 转付费），jc-lab fork 单人维护且不能升级 purego | 若要求长期可升级 → 本方案出局 |

## 七、真 PE 验收清单

```
准备
[ ] 下载 sciter-sdk，取 **bin.win/x32skia/sciter.dll**（PE 无 GPU 必用 skia 版；bin/32 是 D2D+GDI 版，仅备查）
[ ] 记录 x32skia DLL 的版本号与导入依赖表（与 PE 的 System32 对照）
[ ] spike/sciter 用 Go 1.20 编 386：CGO_ENABLED=0 GOOS=windows GOARCH=386 go build

本机（Win11）先过一遍
[ ] 386 exe + x32skia DLL 能开窗口、渲染 HTML/CSS
[ ] 强制 `GFX_LAYER_SKIA`（SciterSetOption + x32skia DLL，CPU 光栅）能渲染，且中文（OpenType 字体）不豆腐块
[ ] **不要试 GDI+** —— 作者不推荐且 OpenType 中文会豆腐块
[ ] 中文正常；退出后进程干净
[ ] **含浮点参数的 API（如 SciterSetOption(SCITER_SET_DEVICE_RESOLUTION,...)）真跑通**（386 SyscallN 不支持混合整数+浮点）
[ ] `attach_image` 图片回显：用 SciterDataReady 喂 `<img>` 字节流真跑通（未证实项）
[ ] 高 DPI（125%/150%）下字号/布局不崩（SCITER_SET_DEVICE_RESOLUTION）
[ ] 复制模型输出到剪贴板可用（Sciter 原生 clipboard API）

真 PE
[ ] 拷 exe + DLL 到 U 盘同目录，启动
[ ] 窗口出现且 CSS 生效；若不出现：确认 x32skia DLL + `GFX_LAYER_SKIA`；**不要回退 GDI+**
[ ] 800x600 布局正常；中文非豆腐块（中文字体仍需 WinPE-FONTSupport-zh-CN 或镜像自带）
[ ] 跑一段真实会话（exec 输出 + 中文 + think 块）
[ ] tasklist 记 smith.exe 内存 = ____ MB（对比 mshta 方案的 80MB）；`tasklist /m` 记 GDI 对象数对比现状 Win32 GUI
[ ] 删掉 sciter.dll 重启 → 应自动降级到 Win32 GUI 且可用
[ ] **若 DLL 落 X: 内存盘**：每次启动从 U 盘读 5MB → 记启动耗时 ____s、scratch 32MB 占用 ____MB（建议 DLL 放只读 U 盘/固定盘而非 X:）
```

## 八、与现有代码的关系

- **不动**：`src/agent/`（loop/history/llm）、`src/tools/`（14 工具）、`src/cfg/`、`src/logx/`
- **改动**：`src/main.go`（frontend 分支 + 加载失败降级）、`smith.ini`（`frontend=win32|sciter`，默认 win32）
- **新增**：`src/scui/`（shell.go / bridge.go）、`ui/`（HTML+CSS 资源，`//go:embed`）、`spike/sciter/`（探针）
- **关键澄清（复审 C6）**：**Job/进程树管理不属 `src/win`（GUI 层）**，应是与渲染解耦的独立模块（现位于 `src/win/job.go` + `src/win/proc.go`，P1 评估是否抽到 `src/agent/` 或独立 `src/job/`）；`src/scui` 只替换渲染层，**不复制进程管理逻辑**。否则"不动 src/win"与"新增 src/scui"自相矛盾
- **保留**：`src/win/`（Win32 GUI 保留为降级通道）—— 注意这意味着**长期维护两套 GUI**，见 §五 成本备注
- **构建**：`build.cmd` 增加 `assets` 步骤（把 sciter.dll 复制到 dist/，并确保 .gitignore 放行该 DLL）
- 三件套修复（H-1 编码 / 字体 / RichEdit）**与本方案正交**，无论走哪条前端路线都要先做

---

## 九、两方案怎么选（速查）

| | HTA（09） | Sciter（10） |
|---|---|---|
| PE 镜像改动 | **需要 4 个 cab** | **不需要加 OC**（但 sciter.dll 依赖 gdiplus/ole32/oleaut32，PE 若缺需随附） |
| 分发形态 | 单 exe（HTA 由镜像 OC 提供） | **exe + DLL（2–3 个文件）** |
| 进程模型 | 双进程 + 本地 HTTP | **单进程** |
| 前端能力 | IE8 级（无 flex，诸多约束） | **`flow:` 方言 + 翻译子集支持 `display:flex`/`grid`**（常见用例可用，非全量 W3C） |
| 内存 | mshta ~80MB | 单进程，更小 |
| 已实测程度 | **本机全链路通过** | 仅纸面，未实测 |
| 最大不确定 | PE 的 IE8 引擎行为 | DLL 在 PE 的渲染后端/依赖 |
| 工作量 | 45–60 人时 | **50–75 人时**（含 PE 渲染后端调试缓冲；未含永久双前端维护） |
| 依赖外链 | 微软官方 OC | 第三方专有 DLL（需确认许可） |

**建议**：先做 HTA 的 P0（半天真 PE 验证），同时做 Sciter 的 P0（半天本机+PE）——两个 P0 都完成后再决定主线；三件套修复同步进行，不阻塞。
