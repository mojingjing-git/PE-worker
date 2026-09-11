// spike/hta/main.go
//
// HTA 前端路线可行性 spike —— 独立程序，不 import src/。
// 纯标准库，Go 1.20 兼容（禁用 tls.VersionName / os/user / GetTickCount64 /
// GetVersionExA / RegGetValueA，遵守 L1 返回 (T,error)、L5 不吞错）。
//
// 行为：
//   - 绑定 127.0.0.1 随机空闲端口
//   - GET  /                返回渲染后的 app.hta（把 __ORIGIN__ 替换为真实 origin）
//   - GET  /app.hta         同上（供方案 B 下载到 %TEMP% 后用 file:// 打开）
//   - POST /api/message     收 JSON {text} 回 JSON {ok,echo,ts}
//   - GET  /api/events      长轮询：server hold 最多 8s，有事件推事件，无则推 {"type":"noop"}
//   - POST /api/result      收 HTA 回传的结果（FSO 被区域策略拦截时的回退通道）
//   - 启动 2s 后主动塞一条 {"type":"test"} 进事件队列
//   - 启动后把完整 URL 写到 %TEMP%/hta_spike_url.txt
//   - 日志写到 hta_spike_server.log（与 exe 同目录）
//
// 编译（用户真机，本沙箱无 Go 工具链）：
//   SET CGO_ENABLED=0
//   SET GOOS=windows
//   SET GOARCH=386
//   go build -o spike/hta/hta386.exe ./spike/hta
//   SET GOARCH=amd64
//   go build -o spike/hta/hta64.exe ./spike/hta
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// 配置
// ---------------------------------------------------------------------------

const (
	holdMaxSec  = 8 // 长轮询最长 hold 时间
	testEventIn = 2 * time.Second
	resultFile  = "hta_spike_result.txt"
	urlFile     = "hta_spike_url.txt"
)

// eventBus 简单的事件队列 + 信号 channel，用于长轮询（避免 sync.Cond 死锁）。
type eventBus struct {
	mu sync.Mutex
	q  []map[string]interface{}
	ch chan struct{} // 有新事件时非阻塞通知
}

func newEventBus() *eventBus {
	return &eventBus{ch: make(chan struct{}, 1)}
}

// push 入队并唤醒一个等待中的长轮询。
func (b *eventBus) push(ev map[string]interface{}) {
	b.mu.Lock()
	b.q = append(b.q, ev)
	b.mu.Unlock()
	select {
	case b.ch <- struct{}{}:
	default:
	}
}

// wait 阻塞至多 timeout，返回最先可用的事件；超时为 nil。
func (b *eventBus) wait(timeout time.Duration) map[string]interface{} {
	deadline := time.After(timeout)
	for {
		b.mu.Lock()
		if len(b.q) > 0 {
			ev := b.q[0]
			b.q = b.q[1:]
			b.mu.Unlock()
			return ev
		}
		b.mu.Unlock()
		select {
		case <-b.ch:
			continue // 被唤醒，重新检查队列
		case <-deadline:
			return nil
		}
	}
}

// ---------------------------------------------------------------------------
// 主逻辑
// ---------------------------------------------------------------------------

func main() {
	bus := newEventBus()

	// 1) 监听随机端口
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatalf("listen failed: %v", err) // L5：不吞错
	}
	port := ln.Addr().(*net.TCPAddr).Port
	origin := fmt.Sprintf("http://127.0.0.1:%d", port)

	// 2) 读 app.hta 模板并渲染 origin + temp 目录
	tmplPath := filepath.Join(exeDir(), "app.hta")
	tmpl, err := os.ReadFile(tmplPath)
	if err != nil {
		log.Fatalf("read app.hta failed: %v", err) // L5
	}
	// 运行时临时目录：PE 上 = X:\Windows\Temp，开发机 = %TEMP%。
	// 注入 __TEMP__ 让 app.hta 的 RESULT_PATH 不再硬编码开发机用户目录。
	// 注意：tempDir() 含反斜杠（Windows 路径），直接塞进 JS 字符串字面量
	// 会被当成 JS 转义（\U \r \0 ...），路径被吃坏 → FSO 报"路径未找到"。
	// 必须先转义反斜杠/双引号，再注入。
	rendered := renderOrigin(string(tmpl), origin, escapeJSStr(tempDir()))

	// 3) 写 URL 文件
	if err := writeURLFile(origin); err != nil {
		log.Printf("warn: write url file failed: %v", err)
	}

	log.Printf("HTA spike server on %s", origin)
	log.Printf("open: mshta %s/  (or download %s/app.hta and run mshta file://...)", origin, origin)

	// 4) 启动 2s 后塞测试事件
	go func() {
		time.Sleep(testEventIn)
		bus.push(map[string]interface{}{"type": "test", "ts": time.Now().Unix()})
		log.Printf("pushed test event")
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" && r.URL.Path != "/app.hta" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/hta")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = io.WriteString(w, rendered)
	})
	mux.HandleFunc("/api/message", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read body failed", http.StatusBadRequest)
			return
		}
		var req struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		log.Printf("message: %q", req.Text)
		resp := map[string]interface{}{"ok": true, "echo": req.Text, "ts": time.Now().Unix()}
		writeJSON(w, resp)
	})
	mux.HandleFunc("/api/events", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		ev := bus.wait(holdMaxSec * time.Second)
		if ev == nil {
			ev = map[string]interface{}{"type": "noop"}
		}
		log.Printf("events long-poll returned: %v", ev["type"])
		writeJSON(w, ev)
	})
	mux.HandleFunc("/api/result", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read body failed", http.StatusBadRequest)
			return
		}
		// 追加来源标记后落盘（FSO 被拦时的回退）
		merged := map[string]interface{}{"source": "server-fallback"}
		var client map[string]interface{}
		if err := json.Unmarshal(body, &client); err == nil {
			for k, v := range client {
				merged[k] = v
			}
		}
		merged["serverReceivedAt"] = time.Now().Unix()
		if err := writeResultFile(merged); err != nil {
			http.Error(w, "write result failed", http.StatusInternalServerError)
			return
		}
		log.Printf("result received via fallback (FSO blocked?)")
		writeJSON(w, map[string]interface{}{"ok": true})
	})

	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	if err := srv.Serve(ln); err != nil {
		log.Fatalf("serve failed: %v", err) // L5
	}
}

// ---------------------------------------------------------------------------
// 辅助
// ---------------------------------------------------------------------------

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

func renderOrigin(tmpl, origin, tempDir string) string {
	// 支持 __ORIGIN__ 与 {{.Origin}} 两种占位（前端地址）
	// 支持 __TEMP__ 占位（运行时临时目录，供 RESULT_PATH 拼结果文件名）
	out := tmpl
	out = replaceAll(out, "__ORIGIN__", origin)
	out = replaceAll(out, "{{.Origin}}", origin)
	out = replaceAll(out, "__TEMP__", tempDir)
	return out
}

// escapeJSStr 把要注入 JS 字符串字面量的值转义：反斜杠与双引号必须转义，
// 否则 Windows 路径 C:\Users\... 里的 \U \r \0 等会被当 JS 转义吃掉，
// 路径错乱 → FSO CreateTextFile "路径未找到"（PE 上 X:\Windows\Temp 同理）。
func escapeJSStr(s string) string {
	s = replaceAll(s, "\\", "\\\\")
	s = replaceAll(s, "\"", "\\\"")
	return s
}

// 极简替换，避免引入 strings 之外的依赖（strings 也是标准库，这里手写以显式可控）
func replaceAll(s, old, new string) string {
	if old == "" {
		return s
	}
	out := ""
	for {
		i := indexOf(s, old)
		if i < 0 {
			out += s
			break
		}
		out += s[:i] + new
		s = s[i+len(old):]
	}
	return out
}

func indexOf(s, sub string) int {
	n := len(sub)
	for i := 0; i+n <= len(s); i++ {
		if s[i:i+n] == sub {
			return i
		}
	}
	return -1
}

func writeURLFile(origin string) error {
	dir := tempDir()
	p := filepath.Join(dir, urlFile)
	return os.WriteFile(p, []byte(origin+"/"), 0o644)
}

func writeResultFile(v map[string]interface{}) error {
	dir := tempDir()
	p := filepath.Join(dir, resultFile)
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, b, 0o644)
}

func tempDir() string {
	if d := os.Getenv("TEMP"); d != "" {
		return d
	}
	if d := os.Getenv("TMP"); d != "" {
		return d
	}
	return os.TempDir()
}

func exeDir() string {
	exe, err := os.Executable()
	if err != nil {
		return "."
	}
	return filepath.Dir(exe)
}
