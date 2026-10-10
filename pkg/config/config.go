package config

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"
	"gopkg.in/yaml.v3"
)

var (
	ErrModelNotFound   = errors.New("model not found in configuration")
	ErrConfigNotLoaded = errors.New("configuration not loaded")
)

type ServerConfig struct {
	Port int    `yaml:"port"`
	Host string `yaml:"host"`
}

type ChannelConfig struct {
	Name     string   `yaml:"name"`
	BaseURL  string   `yaml:"base_url"`
	APIKey   string   `yaml:"api_key"`
	Protocol string   `yaml:"protocol"` // "openai_chat", "openai_responses", "claude_messages", "gemini"
	Models   []string `yaml:"models"`
}

type AuthPoolConfig struct {
	Name    string   `yaml:"name"`
	Type    string   `yaml:"type"`     // "xai", "codex", "antigravity"
	AuthDir string   `yaml:"auth_dir"` // 本地凭据目录
	Models  []string `yaml:"models"`
}

type ModelMapping struct {
	TargetModel string `yaml:"target_model"`
	Channel     string `yaml:"channel"`
	AuthPool    string `yaml:"auth_pool"`
}

type Config struct {
	Server        ServerConfig            `yaml:"server"`
	Channels      []ChannelConfig         `yaml:"channels"`
	AuthPools     []AuthPoolConfig        `yaml:"auth_pools"`
	ModelMappings map[string]ModelMapping `yaml:"model_mappings"`
}

type RouteTarget struct {
	TargetModel string
	Channel     *ChannelConfig
	AuthPool    *AuthPoolConfig
}

type Manager struct {
	current atomic.Pointer[Config]
	mu      sync.Mutex
}

func NewManager() *Manager {
	return &Manager{}
}

// Load 读取并解析配置文件，并原子替换当前配置
func (m *Manager) Load(filePath string) error {
	cfg, err := parseConfigFile(filePath)
	if err != nil {
		return err
	}
	m.current.Store(cfg)
	return nil
}

// Get 返回当前生效的配置指针
func (m *Manager) Get() *Config {
	return m.current.Load()
}

// ResolveRoute 统一解析路由目标，支持普通 Channel 或原生 AuthPool
func (m *Manager) ResolveRoute(requestedModel string) (*RouteTarget, error) {
	cfg := m.Get()
	if cfg == nil {
		return nil, ErrConfigNotLoaded
	}

	// 1. 优先查 ModelMappings
	if mapping, ok := cfg.ModelMappings[requestedModel]; ok {
		targetModel := mapping.TargetModel
		if targetModel == "" {
			targetModel = requestedModel
		}

		if mapping.AuthPool != "" {
			for i := range cfg.AuthPools {
				if cfg.AuthPools[i].Name == mapping.AuthPool {
					poolCopy := cfg.AuthPools[i]
					return &RouteTarget{
						TargetModel: targetModel,
						AuthPool:    &poolCopy,
					}, nil
				}
			}
			return nil, fmt.Errorf("%w: mapped auth pool %q not found", ErrModelNotFound, mapping.AuthPool)
		}

		if mapping.Channel != "" {
			for i := range cfg.Channels {
				if cfg.Channels[i].Name == mapping.Channel {
					chCopy := cfg.Channels[i]
					return &RouteTarget{
						TargetModel: targetModel,
						Channel:     &chCopy,
					}, nil
				}
			}
			return nil, fmt.Errorf("%w: mapped channel %q not found", ErrModelNotFound, mapping.Channel)
		}
	}

	// 2. 遍历 AuthPools
	for i := range cfg.AuthPools {
		pool := &cfg.AuthPools[i]
		if slices.Contains(pool.Models, requestedModel) {
			poolCopy := *pool
			return &RouteTarget{
				TargetModel: requestedModel,
				AuthPool:    &poolCopy,
			}, nil
		}
	}

	// 3. 遍历 Channels 的 Models
	for i := range cfg.Channels {
		ch := &cfg.Channels[i]
		if slices.Contains(ch.Models, requestedModel) {
			chCopy := *ch
			return &RouteTarget{
				TargetModel: requestedModel,
				Channel:     &chCopy,
			}, nil
		}
	}

	return nil, fmt.Errorf("%w: %s", ErrModelNotFound, requestedModel)
}

// RouteModel 兼容旧接口
func (m *Manager) RouteModel(requestedModel string) (*ChannelConfig, string, error) {
	target, err := m.ResolveRoute(requestedModel)
	if err != nil {
		return nil, "", err
	}
	if target.Channel != nil {
		return target.Channel, target.TargetModel, nil
	}
	return nil, target.TargetModel, nil
}

// StartWatcher 启动配置文件监控，检测写操作与重命名等事件进行热重载
// 如果新配置语法错误或读取失败，打印告警日志并保留旧配置（不 panic，不替换为无效配置）
func (m *Manager) StartWatcher(ctx context.Context, filePath string) error {
	absPath, err := filepath.Abs(filePath)
	if err != nil {
		return fmt.Errorf("failed to get absolute path of config file: %w", err)
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("failed to create fsnotify watcher: %w", err)
	}

	// 监听文件所在目录，以便捕获原子写/替换/重命名等操作
	dir := filepath.Dir(absPath)
	if err := watcher.Add(dir); err != nil {
		_ = watcher.Close()
		return fmt.Errorf("failed to watch directory %s: %w", dir, err)
	}

	go func() {
		defer watcher.Close()

		var (
			debounceTimer *time.Timer
			timerCh       <-chan time.Time
		)

		reload := func() {
			newCfg, err := parseConfigFile(absPath)
			if err != nil {
				log.Printf("[WARN] config reload failed, keeping current configuration: %v", err)
				return
			}
			m.current.Store(newCfg)
			log.Printf("[INFO] config reloaded successfully from %s", absPath)
		}

		for {
			select {
			case <-ctx.Done():
				if debounceTimer != nil {
					debounceTimer.Stop()
				}
				return

			case event, ok := <-watcher.Events:
				if !ok {
					return
				}

				// 检查事件是否针对目标文件
				eventClean := filepath.Clean(event.Name)
				if eventClean != absPath {
					continue
				}

				// 关注 Write, Create, Rename 等操作
				if event.Has(fsnotify.Write) || event.Has(fsnotify.Create) || event.Has(fsnotify.Rename) {
					if debounceTimer != nil {
						debounceTimer.Stop()
					}
					// 50ms 防抖，应对多重快速写入或编辑器保存过程
					debounceTimer = time.NewTimer(50 * time.Millisecond)
					timerCh = debounceTimer.C
				}

			case <-timerCh:
				timerCh = nil
				reload()

			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				log.Printf("[WARN] watcher error: %v", err)
			}
		}
	}()

	return nil
}

func parseConfigFile(filePath string) (*Config, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file %s: %w", filePath, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal yaml config %s: %w", filePath, err)
	}

	return &cfg, nil
}
