// Package cfg: INI 解析。
//
// 极简 INI 解析器，只支持：
//   - [section] 头
//   - key = value（空格 trim）
//   - ; 或 # 开头 = 注释
//   - 空行
//   - value 可用 "..." 或 '...' 引号包裹（首尾匹配才剥）
//
// 不支持：嵌套 section、include、escape 字符、多行 value。
// 这对 smith.ini 这种 12 字段的小配置文件够用。
//
// v1-L1 契约：所有 API 返 (T, error)。缺字段用零值（ini 文件没该 section 不报错）。

package cfg

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// 默认值（PLAN §0.6 B6 字段表 + B7 字段约定）
const (
	DefaultKeyFile   = "smith.key"
	DefaultVision    = false
	DefaultTimeout   = 120
	DefaultConfirm   = true
	DefaultMaxTurns  = 10
	DefaultImgHistory = 2
	DefaultFontSize  = 12
)

// Config 是 smith.ini 解析后的根结构。
type Config struct {
	LLM   LLMConfig
	Agent AgentConfig
	UI    UIConfig
	path  string
}

// LLMConfig 是 [llm] 段。
type LLMConfig struct {
	Base     string
	Model    string
	KeyFile  string
	Key      string
	Provider string // "openai" | "anthropic" | "deepseek"；空 = openai
	Vision   bool
	Timeout  int
}

// AgentConfig 是 [agent] 段。
type AgentConfig struct {
	Confirm    bool
	Whitelist  []string
	MaxTurns   int
	ImgHistory int
}

// UIConfig 是 [ui] 段。
type UIConfig struct {
	Font     string
	FontSize int
}

// 错误集
var (
	ErrFileNotFound = errors.New("cfg: file not found")
	ErrNoEquals     = errors.New("cfg: key=value expected")
	ErrInvalidBool  = errors.New("cfg: invalid bool")
	ErrInvalidInt   = errors.New("cfg: invalid int")
)

// Default 返一个默认 cfg（无 ini 路径）。主程序在找不到 ini 时用它启动。
func Default() *Config {
	return defaultConfig()
}

// Load 从 path 加载 INI 文件并解析。文件不存在返 ErrFileNotFound。
// 任何 [section] 缺失 = 用该段默认值。
func Load(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrFileNotFound, err)
	}
	defer f.Close()
	c := defaultConfig()
	c.path = path
	sc := bufio.NewScanner(f)
	section := ""
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, ";") || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.ToLower(strings.TrimSpace(line[1 : len(line)-1]))
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			return nil, fmt.Errorf("%w at line %d: %q", ErrNoEquals, lineNo, line)
		}
		key := strings.TrimSpace(line[:eq])
		val := unquote(strings.TrimSpace(line[eq+1:]))
		if err := setField(c, section, key, val); err != nil {
			return nil, fmt.Errorf("line %d [%s] %s: %w", lineNo, section, key, err)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("cfg: scan: %w", err)
	}
	return c, nil
}

// Path 返回配置文件路径（Load 时设）。
func (c *Config) Path() string {
	return c.path
}

func defaultConfig() *Config {
	return &Config{
		LLM: LLMConfig{
			KeyFile: DefaultKeyFile,
			Vision:  DefaultVision,
			Timeout: DefaultTimeout,
		},
		Agent: AgentConfig{
			Confirm:    DefaultConfirm,
			MaxTurns:   DefaultMaxTurns,
			ImgHistory: DefaultImgHistory,
		},
		UI: UIConfig{
			FontSize: DefaultFontSize,
		},
	}
}

func setField(c *Config, section, key, val string) error {
	switch section {
	case "llm":
		return setLLM(&c.LLM, key, val)
	case "agent":
		return setAgent(&c.Agent, key, val)
	case "ui":
		return setUI(&c.UI, key, val)
	case "":
		// 顶层 key 暂忽略（未来可作 [default] 段）
		return nil
	default:
		// 未知 section 静默忽略（避免 1 个拼写错误毁整个启动）
		return nil
	}
}

func setLLM(c *LLMConfig, key, val string) error {
	switch strings.ToLower(key) {
	case "base":
		c.Base = val
	case "model":
		c.Model = val
	case "keyfile":
		c.KeyFile = val
	case "key":
		c.Key = val
	case "provider":
		c.Provider = strings.ToLower(val)
	case "vision":
		b, err := parseBool(val)
		if err != nil {
			return err
		}
		c.Vision = b
	case "timeout":
		n, err := parseInt(val)
		if err != nil {
			return err
		}
		c.Timeout = n
	default:
		return nil
	}
	return nil
}

func setAgent(c *AgentConfig, key, val string) error {
	switch strings.ToLower(key) {
	case "confirm":
		b, err := parseBool(val)
		if err != nil {
			return err
		}
		c.Confirm = b
	case "whitelist":
		c.Whitelist = splitList(val)
	case "maxturns":
		n, err := parseInt(val)
		if err != nil {
			return err
		}
		c.MaxTurns = n
	case "imghistory":
		n, err := parseInt(val)
		if err != nil {
			return err
		}
		c.ImgHistory = n
	default:
		return nil
	}
	return nil
}

func setUI(c *UIConfig, key, val string) error {
	switch strings.ToLower(key) {
	case "font":
		c.Font = val
	case "fontsize":
		n, err := parseInt(val)
		if err != nil {
			return err
		}
		c.FontSize = n
	default:
		return nil
	}
	return nil
}

func parseBool(s string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off", "":
		return false, nil
	default:
		return false, fmt.Errorf("%w: %q", ErrInvalidBool, s)
	}
}

func parseInt(s string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0, fmt.Errorf("%w: %q (%v)", ErrInvalidInt, s, err)
	}
	return n, nil
}

func splitList(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func unquote(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}
