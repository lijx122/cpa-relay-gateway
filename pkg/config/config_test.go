package config

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const sampleValidYAML = `
server:
  port: 8080
  host: "0.0.0.0"

channels:
  - name: "openai-main"
    base_url: "https://api.openai.com"
    api_key: "sk-openai-key"
    protocol: "openai_chat"
    models:
      - "gpt-4o"
      - "gpt-4o-mini"
  - name: "claude-direct"
    base_url: "https://api.anthropic.com"
    api_key: "sk-claude-key"
    protocol: "claude_messages"
    models:
      - "claude-3-5-sonnet-20241022"

model_mappings:
  "gpt-4":
    channel: "openai-main"
    target_model: "gpt-4o"
  "claude-latest":
    channel: "claude-direct"
    target_model: "claude-3-5-sonnet-20241022"
`

func TestConfigLoadAndParse(t *testing.T) {
	tmpDir := t.TempDir()
	cfgFile := filepath.Join(tmpDir, "config.yaml")

	if err := os.WriteFile(cfgFile, []byte(sampleValidYAML), 0644); err != nil {
		t.Fatalf("failed to write temp config: %v", err)
	}

	mgr := NewManager()
	if err := mgr.Load(cfgFile); err != nil {
		t.Fatalf("mgr.Load failed: %v", err)
	}

	cfg := mgr.Get()
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}

	if cfg.Server.Port != 8080 || cfg.Server.Host != "0.0.0.0" {
		t.Errorf("unexpected server config: %+v", cfg.Server)
	}

	if len(cfg.Channels) != 2 {
		t.Fatalf("expected 2 channels, got %d", len(cfg.Channels))
	}

	if cfg.Channels[0].Name != "openai-main" || cfg.Channels[0].Protocol != "openai_chat" {
		t.Errorf("unexpected channel 0: %+v", cfg.Channels[0])
	}

	if len(cfg.ModelMappings) != 2 {
		t.Fatalf("expected 2 mappings, got %d", len(cfg.ModelMappings))
	}

	mapping, ok := cfg.ModelMappings["gpt-4"]
	if !ok || mapping.TargetModel != "gpt-4o" || mapping.Channel != "openai-main" {
		t.Errorf("unexpected model mapping: %+v", mapping)
	}
}

func TestRouteModel(t *testing.T) {
	tmpDir := t.TempDir()
	cfgFile := filepath.Join(tmpDir, "config.yaml")

	if err := os.WriteFile(cfgFile, []byte(sampleValidYAML), 0644); err != nil {
		t.Fatalf("failed to write temp config: %v", err)
	}

	mgr := NewManager()
	if err := mgr.Load(cfgFile); err != nil {
		t.Fatalf("mgr.Load failed: %v", err)
	}

	t.Run("Route via ModelMappings", func(t *testing.T) {
		ch, targetModel, err := mgr.RouteModel("gpt-4")
		if err != nil {
			t.Fatalf("expected route success, got err: %v", err)
		}
		if ch.Name != "openai-main" {
			t.Errorf("expected channel openai-main, got %s", ch.Name)
		}
		if targetModel != "gpt-4o" {
			t.Errorf("expected targetModel gpt-4o, got %s", targetModel)
		}
	})

	t.Run("Route via Channel.Models directly", func(t *testing.T) {
		ch, targetModel, err := mgr.RouteModel("gpt-4o-mini")
		if err != nil {
			t.Fatalf("expected route success, got err: %v", err)
		}
		if ch.Name != "openai-main" {
			t.Errorf("expected channel openai-main, got %s", ch.Name)
		}
		if targetModel != "gpt-4o-mini" {
			t.Errorf("expected targetModel gpt-4o-mini, got %s", targetModel)
		}

		chClaude, targetClaude, err := mgr.RouteModel("claude-3-5-sonnet-20241022")
		if err != nil {
			t.Fatalf("expected claude route success, got err: %v", err)
		}
		if chClaude.Name != "claude-direct" || targetClaude != "claude-3-5-sonnet-20241022" {
			t.Errorf("unexpected claude route: ch=%s, target=%s", chClaude.Name, targetClaude)
		}
	})

	t.Run("Route Non-existent model", func(t *testing.T) {
		_, _, err := mgr.RouteModel("non-existent-model")
		if err == nil {
			t.Fatal("expected error for non-existent model, got nil")
		}
		if !errors.Is(err, ErrModelNotFound) {
			t.Errorf("expected ErrModelNotFound, got %v", err)
		}
	})
}

func TestHotReload(t *testing.T) {
	tmpDir := t.TempDir()
	cfgFile := filepath.Join(tmpDir, "config.yaml")

	initialYAML := `
server:
  port: 8080
  host: "127.0.0.1"
channels:
  - name: "chan-1"
    base_url: "https://chan1.example.com"
    api_key: "key-1"
    protocol: "openai_chat"
    models:
      - "model-v1"
model_mappings: {}
`
	if err := os.WriteFile(cfgFile, []byte(initialYAML), 0644); err != nil {
		t.Fatalf("failed to write initial config: %v", err)
	}

	mgr := NewManager()
	if err := mgr.Load(cfgFile); err != nil {
		t.Fatalf("initial load failed: %v", err)
	}

	// 确认初始状态能路由 model-v1，不能路由 model-v2
	if _, _, err := mgr.RouteModel("model-v1"); err != nil {
		t.Fatalf("expected model-v1 routed: %v", err)
	}
	if _, _, err := mgr.RouteModel("model-v2"); err == nil {
		t.Fatal("expected model-v2 NOT routed initially")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := mgr.StartWatcher(ctx, cfgFile); err != nil {
		t.Fatalf("StartWatcher failed: %v", err)
	}

	// 1. 测试更新非法 YAML（应记录警告并保留旧配置）
	invalidYAML := `
server:
  port: [invalid
`
	time.Sleep(100 * time.Millisecond) // 等待监听器挂载
	if err := os.WriteFile(cfgFile, []byte(invalidYAML), 0644); err != nil {
		t.Fatalf("failed to write invalid YAML: %v", err)
	}

	// 等待并验证旧配置仍然生效，且无 crash
	time.Sleep(200 * time.Millisecond)
	if _, _, err := mgr.RouteModel("model-v1"); err != nil {
		t.Fatalf("old config should be preserved on invalid yaml: %v", err)
	}

	// 2. 测试合法变更热重载（添加 chan-2 和 model-v2）
	updatedYAML := `
server:
  port: 9090
  host: "0.0.0.0"
channels:
  - name: "chan-1"
    base_url: "https://chan1.example.com"
    api_key: "key-1"
    protocol: "openai_chat"
    models:
      - "model-v1"
  - name: "chan-2"
    base_url: "https://chan2.example.com"
    api_key: "key-2"
    protocol: "gemini"
    models:
      - "gemini-1.5-pro"
model_mappings:
  "model-v2":
    channel: "chan-2"
    target_model: "gemini-1.5-pro"
`
	if err := os.WriteFile(cfgFile, []byte(updatedYAML), 0644); err != nil {
		t.Fatalf("failed to write updated YAML: %v", err)
	}

	// 轮询等待热重载完成（最多等待 3 秒）
	deadline := time.Now().Add(3 * time.Second)
	var reloaded bool
	for time.Now().Before(deadline) {
		ch, target, err := mgr.RouteModel("model-v2")
		if err == nil && ch.Name == "chan-2" && target == "gemini-1.5-pro" {
			reloaded = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if !reloaded {
		t.Fatal("hot reload failed: model-v2 was not routed within deadline")
	}

	// 验证配置也已更新
	cfg := mgr.Get()
	if cfg.Server.Port != 9090 {
		t.Errorf("expected updated port 9090, got %d", cfg.Server.Port)
	}
}
