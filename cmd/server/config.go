// config.go 加载 JSON 配置 + 环境变量覆盖。
package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/prompt"
)

// Config 顶层配置。
type Config struct {
	Listen    string `json:"listen"`
	APIKey    string `json:"api_key"`
	AuthDir   string `json:"auth_dir"`
	StateFile string `json:"state_file"`

	Server struct { MaxBodyMB int `json:"max_body_mb"` } `json:"server"`
	Cooldown struct {
		SoftRate string `json:"soft_rate"`
		SoftRateMax string `json:"soft_rate_max"`
	} `json:"cooldown"`
	Schedule struct {
		CheckinHours []int `json:"checkin_hours"`
		TravelHours []int `json:"travel_hours"`
		ActivityHours []int `json:"activity_hours"`
		KeepaliveHours []int `json:"keepalive_hours"`
		BlackcatHours []int `json:"blackcat_hours"`
		CheckinEnabled bool `json:"checkin_enabled"`
		TravelEnabled bool `json:"travel_enabled"`
		ActivityEnabled bool `json:"activity_enabled"`
		KeepaliveEnabled bool `json:"keepalive_enabled"`
		BlackcatEnabled bool `json:"blackcat_enabled"`
		BalanceRefreshEnabled bool `json:"balance_refresh_enabled"`
		BalanceRefreshMinutes int `json:"balance_refresh_minutes"`
	} `json:"schedule"`
	Global struct { Enabled bool `json:"enabled"`; ChatBase string `json:"chat_base"`; BillingBase string `json:"billing_base"` } `json:"global"`
	Upstream struct {
		TimeoutSeconds int `json:"timeout_seconds"`
		HeaderTimeoutSeconds int `json:"header_timeout_seconds"`
		IdleTimeoutSeconds int `json:"idle_timeout_seconds"`
		UserAgent string `json:"user_agent"`
		ClientVersion string `json:"client_version"`
		CliVersion string `json:"cli_version"`
		ClientName string `json:"client_name"`
		DeviceToken string `json:"device_token"`
		DeviceTokenFile string `json:"device_token_file"`
		PassthroughIP bool `json:"passthrough_ip"`
	} `json:"upstream"`
	Features struct { SanitizeBlacklistFingerprints bool `json:"sanitize_blacklist_fingerprints"` } `json:"features"`
	Prompt struct { Mode string `json:"mode"`; File string `json:"file"` } `json:"prompt"`
	PromptText string `json:"-"`
	Upstash struct { URL string `json:"url"`; Token string `json:"token"` } `json:"upstash"`
	Pool struct {
		MaxInFlight int `json:"max_in_flight"`
		MaxInFlightGlobal int `json:"max_in_flight_global"`
		BreakerThreshold int `json:"breaker_threshold"`
		BreakerCooldown string `json:"breaker_cooldown"`
		BreakerCooldownMax string `json:"breaker_cooldown_max"`
		DegradeThreshold int `json:"degrade_threshold"`
		DegradeCooldown string `json:"degrade_cooldown"`
		DegradeCooldownMax string `json:"degrade_cooldown_max"`
		IdleWeightPerHour float64 `json:"idle_weight_per_hour"`
		IdleWeightMax float64 `json:"idle_weight_max"`
		ExpiringSoon string `json:"expiring_soon"`
	} `json:"pool"`
	SessionSticky struct { Enabled bool `json:"enabled"`; TTL string `json:"ttl"`; GCInterval string `json:"gc_interval"` } `json:"session_sticky"`
	SoftRateDur time.Duration `json:"-"`
	SoftRateMaxDur time.Duration `json:"-"`
	BreakerCooldownDur time.Duration `json:"-"`
	BreakerCooldownMaxD time.Duration `json:"-"`
	DegradeCooldownDur time.Duration `json:"-"`
	DegradeCooldownMaxD time.Duration `json:"-"`
	SessionTTL time.Duration `json:"-"`
	SessionGCInterval time.Duration `json:"-"`
	BalanceRefreshInterval time.Duration `json:"-"`
	ExpiringSoonDur time.Duration `json:"-"`
}

// Default 默认配置。Magisk runtime 会显式设置 WB2A_ANDROID_MODULE、WB2A_AUTH_DIR
// 与 WB2A_STATE_FILE；首次生成的配置因而安全地绑定 loopback 且数据不在模块目录内。
func Default() *Config {
	c := &Config{Listen: ":7863", APIKey: "", AuthDir: "./auths", StateFile: "./data/state.json"}
	if os.Getenv("WB2A_ANDROID_MODULE") == "1" {
		c.Listen = "127.0.0.1:7863"
		if v := os.Getenv("WB2A_AUTH_DIR"); v != "" { c.AuthDir = v }
		if v := os.Getenv("WB2A_STATE_FILE"); v != "" { c.StateFile = v }
	}
	c.Cooldown.SoftRate, c.Cooldown.SoftRateMax = "600s", "2h"
	c.Server.MaxBodyMB = 8
	c.Schedule.CheckinHours, c.Schedule.TravelHours = []int{9, 21}, []int{9, 21}
	c.Schedule.ActivityHours, c.Schedule.KeepaliveHours, c.Schedule.BlackcatHours = []int{10}, []int{22}, []int{23}
	c.Schedule.CheckinEnabled, c.Schedule.TravelEnabled, c.Schedule.ActivityEnabled = true, true, true
	c.Schedule.KeepaliveEnabled, c.Schedule.BlackcatEnabled, c.Schedule.BalanceRefreshEnabled = true, true, true
	c.Schedule.BalanceRefreshMinutes = 5
	c.Upstream.TimeoutSeconds = 120
	c.Global.Enabled = true
	c.Features.SanitizeBlacklistFingerprints = true
	c.Prompt.Mode = "passthrough"
	c.Pool.MaxInFlight, c.Pool.MaxInFlightGlobal = 3, 2
	c.Pool.BreakerThreshold, c.Pool.BreakerCooldown, c.Pool.BreakerCooldownMax = 3, "30m", "6h"
	c.Pool.DegradeThreshold, c.Pool.DegradeCooldown, c.Pool.DegradeCooldownMax = 5, "10m", "2h"
	c.Pool.IdleWeightPerHour, c.Pool.IdleWeightMax, c.Pool.ExpiringSoon = 0.5, 5.0, "168h"
	c.SessionSticky.Enabled, c.SessionSticky.TTL, c.SessionSticky.GCInterval = true, "30m", "5m"
	return c
}

// Load 从文件读，再用 WB2A_* env 覆盖。
func Load(path string) (*Config, error) {
	c := Default()
	if path != "" {
		if st, statErr := os.Stat(path); statErr == nil && st.IsDir() {
			return nil, fmt.Errorf("config %s 是目录而非文件；请创建配置文件，例如：cp config.example.json %s", path, path)
		}
		raw, err := os.ReadFile(path)
		if err != nil { return nil, fmt.Errorf("read config: %w", err) }
		if _, err := ParseConfigInto(raw, c); err != nil { return nil, err }
	}
	applyEnv(c)
	if err := c.normalize(); err != nil { return nil, err }
	return c, nil
}
func ParseConfigInto(raw []byte, c *Config) (*Config, error) {
	if err := json.Unmarshal(raw, c); err != nil { return nil, fmt.Errorf("parse config: %w", err) }
	if err := c.normalize(); err != nil { return nil, err }; return c, nil
}
func ParseConfig(raw []byte) (*Config, error) { return ParseConfigInto(raw, Default()) }
func WriteDefault(path string) (string, error) {
	raw := make([]byte, 18); if _, err := rand.Read(raw); err != nil { return "", fmt.Errorf("gen api_key: %w", err) }
	key := "sk-" + base64.RawURLEncoding.EncodeToString(raw)
	c := Default(); c.APIKey = key; _ = c.normalize()
	out, err := json.MarshalIndent(c, "", "  "); if err != nil { return "", fmt.Errorf("marshal config: %w", err) }
	if dir := filepath.Dir(path); dir != "" && dir != "." { if err := os.MkdirAll(dir, 0o700); err != nil { return "", fmt.Errorf("mkdir config dir: %w", err) } }
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600); if err != nil { return "", fmt.Errorf("write config: %w", err) }; defer f.Close()
	if _, err := f.Write(out); err != nil { return "", fmt.Errorf("write config: %w", err) }; return key, nil
}
func applyEnv(c *Config) {
	if v := os.Getenv("WB2A_LISTEN"); v != "" { c.Listen = v }; if v := os.Getenv("WB2A_API_KEY"); v != "" { c.APIKey = v }
	if v := os.Getenv("WB2A_AUTH_DIR"); v != "" { c.AuthDir = v }; if v := os.Getenv("WB2A_STATE_FILE"); v != "" { c.StateFile = v }
	if v := os.Getenv("WB2A_MAX_BODY_MB"); v != "" { if n, err := strconv.Atoi(v); err == nil { c.Server.MaxBodyMB = n } }
	if v := os.Getenv("WB2A_SOFT_RATE"); v != "" { c.Cooldown.SoftRate = v }; if v := os.Getenv("WB2A_SOFT_RATE_MAX"); v != "" { c.Cooldown.SoftRateMax = v }
	if v := os.Getenv("WB2A_TIMEOUT_SECONDS"); v != "" { if n, err := strconv.Atoi(v); err == nil { c.Upstream.TimeoutSeconds = n } }
	if v := os.Getenv("WB2A_HEADER_TIMEOUT_SECONDS"); v != "" { if n, err := strconv.Atoi(v); err == nil { c.Upstream.HeaderTimeoutSeconds = n } }
	if v := os.Getenv("WB2A_IDLE_TIMEOUT_SECONDS"); v != "" { if n, err := strconv.Atoi(v); err == nil { c.Upstream.IdleTimeoutSeconds = n } }
	if v := os.Getenv("WB2A_USER_AGENT"); v != "" { c.Upstream.UserAgent = v }; if v := os.Getenv("WB2A_CLIENT_VERSION"); v != "" { c.Upstream.ClientVersion = v }; if v := os.Getenv("WB2A_CLI_VERSION"); v != "" { c.Upstream.CliVersion = v }; if v := os.Getenv("WB2A_CLIENT_NAME"); v != "" { c.Upstream.ClientName = v }
	if v := os.Getenv("WB2A_DEVICE_TOKEN"); v != "" { c.Upstream.DeviceToken = v }; if v := os.Getenv("WB2A_DEVICE_TOKEN_FILE"); v != "" { c.Upstream.DeviceTokenFile = v }
	if v := os.Getenv("WB2A_PASSTHROUGH_IP"); v != "" { if b, err := strconv.ParseBool(v); err == nil { c.Upstream.PassthroughIP = b } }
	if v := os.Getenv("WB2A_SANITIZE_FINGERPRINTS"); v != "" { if b, err := strconv.ParseBool(v); err == nil { c.Features.SanitizeBlacklistFingerprints = b } }
	if v := os.Getenv("WB2A_PROMPT_MODE"); v != "" { c.Prompt.Mode = v }; if v := os.Getenv("WB2A_PROMPT_FILE"); v != "" { c.Prompt.File = v }; if v := os.Getenv("WB2A_EXPIRING_SOON"); v != "" { c.Pool.ExpiringSoon = v }
}
func (c *Config) normalize() error {
	var err error
	if c.Server.MaxBodyMB <= 0 { return fmt.Errorf("server.max_body_mb: %d 非法（需为正整数，单位 MB）", c.Server.MaxBodyMB) }
	if c.SoftRateDur, err = time.ParseDuration(c.Cooldown.SoftRate); err != nil { return fmt.Errorf("cooldown.soft_rate: %w", err) }
	if c.Cooldown.SoftRateMax == "" { c.Cooldown.SoftRateMax = "2h" }; if c.SoftRateMaxDur, err = time.ParseDuration(c.Cooldown.SoftRateMax); err != nil { return fmt.Errorf("cooldown.soft_rate_max: %w", err) }
	if c.Pool.BreakerCooldown == "" { c.Pool.BreakerCooldown = "30m" }; if c.Pool.BreakerCooldownMax == "" { c.Pool.BreakerCooldownMax = "6h" }
	if c.BreakerCooldownDur, err = time.ParseDuration(c.Pool.BreakerCooldown); err != nil { return fmt.Errorf("pool.breaker_cooldown: %w", err) }; if c.BreakerCooldownMaxD, err = time.ParseDuration(c.Pool.BreakerCooldownMax); err != nil { return fmt.Errorf("pool.breaker_cooldown_max: %w", err) }
	if c.Pool.DegradeCooldown == "" { c.Pool.DegradeCooldown = "10m" }; if c.Pool.DegradeCooldownMax == "" { c.Pool.DegradeCooldownMax = "2h" }
	if c.DegradeCooldownDur, err = time.ParseDuration(c.Pool.DegradeCooldown); err != nil { return fmt.Errorf("pool.degrade_cooldown: %w", err) }; if c.DegradeCooldownMaxD, err = time.ParseDuration(c.Pool.DegradeCooldownMax); err != nil { return fmt.Errorf("pool.degrade_cooldown_max: %w", err) }
	if c.SessionTTL, err = time.ParseDuration(c.SessionSticky.TTL); err != nil { return fmt.Errorf("session_sticky.ttl: %w", err) }; if c.SessionGCInterval, err = time.ParseDuration(c.SessionSticky.GCInterval); err != nil { return fmt.Errorf("session_sticky.gc_interval: %w", err) }
	if c.Pool.ExpiringSoon != "" { if c.ExpiringSoonDur, err = time.ParseDuration(c.Pool.ExpiringSoon); err != nil { return fmt.Errorf("pool.expiring_soon: %w", err) } }
	if c.Pool.BreakerThreshold <= 0 { c.Pool.BreakerThreshold = 3 }; if c.Pool.DegradeThreshold <= 0 { c.Pool.DegradeThreshold = 5 }; if c.Pool.MaxInFlightGlobal <= 0 { c.Pool.MaxInFlightGlobal = 2 }; if c.Pool.IdleWeightPerHour <= 0 { c.Pool.IdleWeightPerHour = 0.5 }; if c.Pool.IdleWeightMax <= 0 { c.Pool.IdleWeightMax = 5 }
	if c.Upstream.TimeoutSeconds <= 0 { c.Upstream.TimeoutSeconds = 120 }; if c.Upstream.HeaderTimeoutSeconds <= 0 { c.Upstream.HeaderTimeoutSeconds = c.Upstream.TimeoutSeconds }; if c.Upstream.IdleTimeoutSeconds <= 0 { c.Upstream.IdleTimeoutSeconds = 300 }
	if !strings.HasPrefix(c.Listen, ":") && !strings.Contains(c.Listen, ":") { c.Listen = ":" + c.Listen }
	if len(c.Schedule.CheckinHours) == 0 { c.Schedule.CheckinHours = []int{9,21} }; if len(c.Schedule.TravelHours) == 0 { c.Schedule.TravelHours = []int{9,21} }; if len(c.Schedule.ActivityHours) == 0 { c.Schedule.ActivityHours = []int{10} }; if len(c.Schedule.KeepaliveHours) == 0 { c.Schedule.KeepaliveHours = []int{22} }; if len(c.Schedule.BlackcatHours) == 0 { c.Schedule.BlackcatHours = []int{23} }
	if c.Schedule.BalanceRefreshEnabled { if c.Schedule.BalanceRefreshMinutes <= 0 { c.Schedule.BalanceRefreshMinutes = 5 }; c.BalanceRefreshInterval = time.Duration(c.Schedule.BalanceRefreshMinutes)*time.Minute }
	if err := c.validateScheduleHours(); err != nil { return err }; return c.normalizePrompt()
}
func (c *Config) normalizePrompt() error { switch m := strings.ToLower(strings.TrimSpace(c.Prompt.Mode)); m { case "", "passthrough": c.Prompt.Mode="passthrough"; case "custom": c.Prompt.Mode="custom"; default: return fmt.Errorf("prompt.mode: %q 不是合法值（passthrough / custom）", c.Prompt.Mode) }; if c.Prompt.Mode == "custom" { text, err := prompt.Load(c.Prompt.Mode,c.Prompt.File); if err != nil{return err}; c.PromptText=text }; return nil }
func (c *Config) validateScheduleHours() error { if err:=checkHourRange("schedule.checkin_hours","checkin_enabled",c.Schedule.CheckinHours);err!=nil{return err}; if err:=checkHourRange("schedule.travel_hours","travel_enabled",c.Schedule.TravelHours);err!=nil{return err}; if err:=checkHourRange("schedule.activity_hours","activity_enabled",c.Schedule.ActivityHours);err!=nil{return err}; if err:=checkHourRange("schedule.keepalive_hours","keepalive_enabled",c.Schedule.KeepaliveHours);err!=nil{return err}; return checkHourRange("schedule.blackcat_hours","blackcat_enabled",c.Schedule.BlackcatHours) }
func checkHourRange(field,switchKey string,hours []int) error { for _,h:=range hours { if h<0||h>23{return fmt.Errorf("%s: %d 不是合法小时（0-23）；如要关闭该任务请设 schedule.%s=false",field,h,switchKey)} }; return nil }
