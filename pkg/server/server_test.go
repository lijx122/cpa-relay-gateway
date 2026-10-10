package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cpa-relay-gateway/pkg/config"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"gopkg.in/yaml.v3"
)

func strPtr(s string) *string { return &s }

func TestEndToEndGatewayAuthPoolNative(t *testing.T) {
	// 1. 模拟 xAI 官方直连端点（验证网关直连与指纹，不依赖任何本地第三方中间件）
	var receivedAuthHeader string
	var receivedVersionHeader string
	mockXAI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuthHeader = r.Header.Get("Authorization")
		receivedVersionHeader = r.Header.Get("x-grok-client-version")

		w.Header().Set("Content-Type", "application/json")
		resp := dto.OpenAITextResponse{
			Id:     "chatcmpl_mock_xai",
			Object: "chat.completion",
			Model:  "grok-3-mini",
			Choices: []dto.OpenAITextResponseChoice{
				{
					Index: 0,
					Message: dto.Message{
						Role:    "assistant",
						Content: "Hello from Native Grok Upstream!",
					},
					FinishReason: "stop",
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer mockXAI.Close()

	// 2. 创建测试 auth 目录，写入原生 xai 凭据文件
	authDir := t.TempDir()
	authFile := filepath.Join(authDir, "xai-test-account.json")
	authJSON := []byte(`{
		"type": "xai",
		"email": "test@grok.com",
		"access_token": "sk-mock-xai-token-12345",
		"base_url": "` + mockXAI.URL + `",
		"disabled": false
	}`)
	if err := os.WriteFile(authFile, authJSON, 0644); err != nil {
		t.Fatalf("write auth file: %v", err)
	}

	// 3. 配置网关：原生挂载 auth_pools
	cfg := &config.Config{
		Server: config.ServerConfig{Port: 8788, Host: "127.0.0.1"},
		AuthPools: []config.AuthPoolConfig{
			{
				Name:    "xai-native-pool",
				Type:    "xai",
				AuthDir: authDir,
				Models:  []string{"grok-3-mini"},
			},
		},
		ModelMappings: map[string]config.ModelMapping{
			"claude-to-grok": {
				TargetModel: "grok-3-mini",
				AuthPool:    "xai-native-pool",
			},
		},
	}

	cfgMgr := config.NewManager()
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := writeTestYAML(cfgPath, cfg); err != nil {
		t.Fatalf("writeTestYAML failed: %v", err)
	}
	if err := cfgMgr.Load(cfgPath); err != nil {
		t.Fatalf("cfgMgr.Load failed: %v", err)
	}

	gateway := NewServer(cfgMgr)
	gwServer := httptest.NewServer(gateway.Handler())
	defer gwServer.Close()

	// 4. 客户端用 Claude Messages 格式请求网关 -> 网关原生打 mock xAI -> 返回 Claude 格式响应
	reqBody := []byte(`{
		"model": "claude-to-grok",
		"max_tokens": 100,
		"messages": [
			{"role": "user", "content": "Hi from Claude client to Native Grok"}
		]
	}`)
	resp, err := http.Post(gwServer.URL+"/v1/messages", "application/json", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("http post failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("unexpected status %d: %s", resp.StatusCode, string(body))
	}

	var claudeResp dto.ClaudeResponse
	if err := json.NewDecoder(resp.Body).Decode(&claudeResp); err != nil {
		t.Fatalf("decode claude response failed: %v", err)
	}

	// 验证上游实际收到的指纹与认证信息
	if receivedAuthHeader != "Bearer sk-mock-xai-token-12345" {
		t.Errorf("expected Bearer token, got %q", receivedAuthHeader)
	}
	if receivedVersionHeader != "1.0.13" {
		t.Errorf("expected Grok version 1.0.13, got %q", receivedVersionHeader)
	}

	// 验证客户端收到的 Claude 格式响应
	if len(claudeResp.Content) == 0 {
		t.Fatalf("expected claude response content > 0")
	}
	if !strings.Contains(claudeResp.Content[0].GetText(), "Hello from Native Grok Upstream!") {
		t.Errorf("unexpected content: %s", claudeResp.Content[0].GetText())
	}
}

func TestEndToEndGatewayCrossProtocol(t *testing.T) {
	// 1. 搭建模拟上游
	// 模拟 OpenAI 上游
	upstreamOpenAI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := dto.OpenAITextResponse{
			Id:     "chatcmpl_mock_openai",
			Object: "chat.completion",
			Model:  "mock-gpt4",
			Choices: []dto.OpenAITextResponseChoice{
				{
					Index: 0,
					Message: dto.Message{
						Role:    "assistant",
						Content: "Hello from Mock OpenAI Upstream!",
					},
					FinishReason: "stop",
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer upstreamOpenAI.Close()

	// 模拟 Claude 上游
	upstreamClaude := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := dto.ClaudeResponse{
			Id:         "msg_mock_claude",
			Type:       "message",
			Role:       "assistant",
			Model:      "mock-claude",
			StopReason: "end_turn",
			Content: []dto.ClaudeMediaMessage{
				{
					Type: "text",
					Text: strPtr("Hello from Mock Claude Upstream!"),
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer upstreamClaude.Close()

	// 模拟 Gemini 上游
	upstreamGemini := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := dto.GeminiChatResponse{
			Candidates: []dto.GeminiChatCandidate{
				{
					Content: dto.GeminiChatContent{
						Role: "model",
						Parts: []dto.GeminiPart{
							{Text: "Hello from Mock Gemini Upstream!"},
						},
					},
					FinishReason: strPtr("STOP"),
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer upstreamGemini.Close()

	// 2. 配置 Manager 与路由
	cfg := &config.Config{
		Server: config.ServerConfig{Port: 8787, Host: "127.0.0.1"},
		Channels: []config.ChannelConfig{
			{
				Name:     "ch-openai",
				BaseURL:  upstreamOpenAI.URL,
				APIKey:   "sk-test-openai",
				Protocol: "openai_chat",
				Models:   []string{"mock-gpt4"},
			},
			{
				Name:     "ch-claude",
				BaseURL:  upstreamClaude.URL,
				APIKey:   "sk-test-claude",
				Protocol: "claude_messages",
				Models:   []string{"mock-claude"},
			},
			{
				Name:     "ch-gemini",
				BaseURL:  upstreamGemini.URL,
				APIKey:   "AIza-mock-gemini",
				Protocol: "gemini",
				Models:   []string{"mock-gemini"},
			},
		},
		ModelMappings: map[string]config.ModelMapping{
			"alias-to-claude": {
				TargetModel: "mock-claude",
				Channel:     "ch-claude",
			},
			"alias-to-openai": {
				TargetModel: "mock-gpt4",
				Channel:     "ch-openai",
			},
			"alias-to-gemini": {
				TargetModel: "mock-gemini",
				Channel:     "ch-gemini",
			},
		},
	}

	cfgMgr := config.NewManager()
	cfgPath := t.TempDir() + "/config.yaml"
	if err := writeTestYAML(cfgPath, cfg); err != nil {
		t.Fatalf("writeTestYAML failed: %v", err)
	}
	if err := cfgMgr.Load(cfgPath); err != nil {
		t.Fatalf("cfgMgr.Load failed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := cfgMgr.StartWatcher(ctx, cfgPath); err != nil {
		t.Fatalf("StartWatcher failed: %v", err)
	}

	gateway := NewServer(cfgMgr)
	gwServer := httptest.NewServer(gateway.Handler())
	defer gwServer.Close()

	// 3. 测试 1：下游发 OpenAI Chat 格式 -> 网关路由至 Claude 上游 -> 下游拿到 OpenAI Chat 响应
	t.Run("OpenAIChat_To_ClaudeUpstream", func(t *testing.T) {
		reqBody := []byte(`{
			"model": "alias-to-claude",
			"messages": [
				{"role": "user", "content": "Hi from OpenAI client"}
			]
		}`)
		resp, err := http.Post(gwServer.URL+"/v1/chat/completions", "application/json", bytes.NewReader(reqBody))
		if err != nil {
			t.Fatalf("http post failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("unexpected status %d: %s", resp.StatusCode, string(body))
		}

		var openaiResp dto.OpenAITextResponse
		if err := json.NewDecoder(resp.Body).Decode(&openaiResp); err != nil {
			t.Fatalf("decode openai response failed: %v", err)
		}

		if len(openaiResp.Choices) == 0 {
			t.Fatalf("expected choices > 0")
		}
		content, _ := openaiResp.Choices[0].Message.Content.(string)
		if !strings.Contains(content, "Hello from Mock Claude Upstream!") {
			t.Errorf("unexpected content: %s", content)
		}
	})

	// 4. 测试 2：下游发 Claude Messages 格式 -> 网关路由至 OpenAI 上游 -> 下游拿到 Claude Messages 响应
	t.Run("ClaudeMessages_To_OpenAIUpstream", func(t *testing.T) {
		reqBody := []byte(`{
			"model": "alias-to-openai",
			"max_tokens": 1024,
			"messages": [
				{"role": "user", "content": "Hi from Claude client"}
			]
		}`)
		resp, err := http.Post(gwServer.URL+"/v1/messages", "application/json", bytes.NewReader(reqBody))
		if err != nil {
			t.Fatalf("http post failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("unexpected status %d: %s", resp.StatusCode, string(body))
		}

		var claudeResp dto.ClaudeResponse
		if err := json.NewDecoder(resp.Body).Decode(&claudeResp); err != nil {
			t.Fatalf("decode claude response failed: %v", err)
		}

		if len(claudeResp.Content) == 0 {
			t.Fatalf("expected claude content > 0")
		}
		if !strings.Contains(claudeResp.Content[0].GetText(), "Hello from Mock OpenAI Upstream!") {
			t.Errorf("unexpected text: %s", claudeResp.Content[0].GetText())
		}
	})

	// 5. 测试 3：下游发 OpenAI Chat 格式 -> 网关路由至 Gemini 上游 -> 下游拿到 OpenAI Chat 响应
	t.Run("OpenAIChat_To_GeminiUpstream", func(t *testing.T) {
		reqBody := []byte(`{
			"model": "alias-to-gemini",
			"messages": [
				{"role": "user", "content": "Hi from OpenAI client to Gemini"}
			]
		}`)
		resp, err := http.Post(gwServer.URL+"/v1/chat/completions", "application/json", bytes.NewReader(reqBody))
		if err != nil {
			t.Fatalf("http post failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("unexpected status %d: %s", resp.StatusCode, string(body))
		}

		var openaiResp dto.OpenAITextResponse
		if err := json.NewDecoder(resp.Body).Decode(&openaiResp); err != nil {
			t.Fatalf("decode openai response failed: %v", err)
		}

		if len(openaiResp.Choices) == 0 {
			t.Fatalf("expected choices > 0")
		}
		content, _ := openaiResp.Choices[0].Message.Content.(string)
		if !strings.Contains(content, "Hello from Mock Gemini Upstream!") {
			t.Errorf("unexpected content: %s", content)
		}
	})

	// 6. 测试 4：下游发 Gemini 格式 -> 网关路由至 OpenAI 上游 -> 下游拿到 Gemini 格式响应
	t.Run("Gemini_To_OpenAIUpstream", func(t *testing.T) {
		reqBody := []byte(`{
			"contents": [
				{
					"role": "user",
					"parts": [{"text": "Hi Gemini client to OpenAI upstream"}]
				}
			]
		}`)
		resp, err := http.Post(gwServer.URL+"/v1beta/models/alias-to-openai:generateContent", "application/json", bytes.NewReader(reqBody))
		if err != nil {
			t.Fatalf("http post failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("unexpected status %d: %s", resp.StatusCode, string(body))
		}

		var geminiResp dto.GeminiChatResponse
		if err := json.NewDecoder(resp.Body).Decode(&geminiResp); err != nil {
			t.Fatalf("decode gemini response failed: %v", err)
		}

		if len(geminiResp.Candidates) == 0 || len(geminiResp.Candidates[0].Content.Parts) == 0 {
			t.Fatalf("expected candidate parts > 0")
		}
		if !strings.Contains(geminiResp.Candidates[0].Content.Parts[0].Text, "Hello from Mock OpenAI Upstream!") {
			t.Errorf("unexpected gemini text: %s", geminiResp.Candidates[0].Content.Parts[0].Text)
		}
	})

	// 7. 测试 5：Health 与 Models 列表接口
	t.Run("Health_And_Models", func(t *testing.T) {
		resp, err := http.Get(gwServer.URL + "/health")
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("health check failed: %v, status: %d", err, resp.StatusCode)
		}

		modelsResp, err := http.Get(gwServer.URL + "/v1/models")
		if err != nil || modelsResp.StatusCode != http.StatusOK {
			t.Fatalf("models check failed: %v, status: %d", err, modelsResp.StatusCode)
		}

		var result map[string]any
		_ = json.NewDecoder(modelsResp.Body).Decode(&result)
		dataList, ok := result["data"].([]any)
		if !ok || len(dataList) == 0 {
			t.Errorf("expected models in data list")
		}
	})

	// 8. 测试 6：在线原子热重载验证（无需重启服务器添加新模型）
	t.Run("Online_HotReload_NewModel", func(t *testing.T) {
		reqBody := []byte(`{
			"model": "brand-new-model",
			"messages": [{"role": "user", "content": "hello"}]
		}`)
		resp404, _ := http.Post(gwServer.URL+"/v1/chat/completions", "application/json", bytes.NewReader(reqBody))
		if resp404.StatusCode != http.StatusNotFound {
			t.Fatalf("expected 404 for brand-new-model before hot reload, got %d", resp404.StatusCode)
		}
		resp404.Body.Close()

		cfg.ModelMappings["brand-new-model"] = config.ModelMapping{
			TargetModel: "mock-gpt4",
			Channel:     "ch-openai",
		}
		if err := writeTestYAML(cfgPath, cfg); err != nil {
			t.Fatalf("writeTestYAML update failed: %v", err)
		}

		time.Sleep(250 * time.Millisecond)

		resp200, err := http.Post(gwServer.URL+"/v1/chat/completions", "application/json", bytes.NewReader(reqBody))
		if err != nil {
			t.Fatalf("http post after hot reload failed: %v", err)
		}
		defer resp200.Body.Close()

		if resp200.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp200.Body)
			t.Fatalf("expected 200 OK after hot reload, got %d: %s", resp200.StatusCode, string(body))
		}
	})
}

func writeTestYAML(path string, cfg *config.Config) error {
	b, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0644)
}
