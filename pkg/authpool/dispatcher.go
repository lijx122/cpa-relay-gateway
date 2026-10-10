package authpool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/relaykit/types"
)

const (
	XAIClientVersion = "1.0.13" // 修复 426 的最新 Grok 客户端指纹
)

// Dispatcher 负责将请求直接打到第三方官方端点，不依赖本地任何外部服务
type Dispatcher struct {
	httpClient *http.Client
}

func NewDispatcher() *Dispatcher {
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   32,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	return &Dispatcher{
		httpClient: &http.Client{
			Transport: transport,
			Timeout:   120 * time.Second,
		},
	}
}

// DirectExecute 直接向上游官方服务器发起调用
func (d *Dispatcher) DirectExecute(
	ctx context.Context,
	auth *AuthRecord,
	targetModel string,
	reqBody []byte,
	isStream bool,
) (*http.Response, types.RelayFormat, error) {
	switch strings.ToLower(auth.Type) {
	case "xai":
		return d.executeXAI(ctx, auth, targetModel, reqBody, isStream)
	case "codex":
		return d.executeCodex(ctx, auth, targetModel, reqBody, isStream)
	case "antigravity":
		return d.executeAntigravity(ctx, auth, targetModel, reqBody, isStream)
	default:
		return nil, "", fmt.Errorf("unsupported direct auth type: %q", auth.Type)
	}
}

func (d *Dispatcher) executeXAI(
	ctx context.Context,
	auth *AuthRecord,
	targetModel string,
	reqBody []byte,
	isStream bool,
) (*http.Response, types.RelayFormat, error) {
	baseURL := "https://cli-chat-proxy.grok.com/v1"
	if auth.BaseURL != "" {
		baseURL = strings.TrimRight(auth.BaseURL, "/")
	}
	url := baseURL + "/chat/completions"

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reqBody))
	if err != nil {
		return nil, "", err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+auth.AccessToken)
	req.Header.Set("X-XAI-Token-Auth", "xai-grok-cli")
	req.Header.Set("x-grok-client-version", XAIClientVersion)
	req.Header.Set("User-Agent", "xai-grok-workspace/"+XAIClientVersion)
	req.Header.Set("x-grok-client-identifier", "grok-shell")

	resp, err := d.httpClient.Do(req)
	return resp, types.RelayFormatOpenAI, err
}

func (d *Dispatcher) executeCodex(
	ctx context.Context,
	auth *AuthRecord,
	targetModel string,
	reqBody []byte,
	isStream bool,
) (*http.Response, types.RelayFormat, error) {
	baseURL := "https://chatgpt.com/backend-api/codex"
	if auth.BaseURL != "" {
		baseURL = strings.TrimRight(auth.BaseURL, "/")
	}
	url := baseURL + "/responses"

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reqBody))
	if err != nil {
		return nil, "", err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+auth.AccessToken)
	if auth.AccountID != "" {
		req.Header.Set("chatgpt-account-id", auth.AccountID)
	}

	resp, err := d.httpClient.Do(req)
	return resp, types.RelayFormatOpenAIResponses, err
}

func (d *Dispatcher) executeAntigravity(
	ctx context.Context,
	auth *AuthRecord,
	targetModel string,
	reqBody []byte,
	isStream bool,
) (*http.Response, types.RelayFormat, error) {
	baseURL := "https://daily-cloudcode-pa.googleapis.com"
	if auth.BaseURL != "" {
		baseURL = strings.TrimRight(auth.BaseURL, "/")
	}
	path := "/v1internal:generateContent"
	if isStream {
		path = "/v1internal:streamGenerateContent?alt=sse"
	}
	url := baseURL + path

	// Antigravity 上游接口要求将标准 Gemini 请求包装在外层 envelope 中
	projectID := auth.ProjectID
	if projectID == "" {
		projectID = "aicode-consumers"
	}

	// 智能补全并注入 Antigravity 官方要求的 thinkingConfig（对齐 CPA 核心逻辑）
	finalReqBody := patchAntigravityThinking(reqBody, targetModel)

	envelope := map[string]any{
		"project":     projectID,
		"model":       targetModel,
		"userAgent":   "antigravity",
		"requestType": "agent",
		"requestId":   fmt.Sprintf("agent-%d", time.Now().UnixNano()),
		"request":     json.RawMessage(finalReqBody),
	}
	wrappedBody, err := json.Marshal(envelope)
	if err != nil {
		return nil, "", fmt.Errorf("marshal antigravity envelope: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(wrappedBody))
	if err != nil {
		return nil, "", err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+auth.AccessToken)
	// 使用 Antigravity 官方客户端指纹，防止被判定为已废弃的旧客户端
	req.Header.Set("User-Agent", "antigravity/hub/2.9.1 darwin/arm64 google-api-nodejs-client/10.3.0")

	resp, err := d.httpClient.Do(req)
	return resp, types.RelayFormatGemini, err
}

func patchAntigravityThinking(reqBody []byte, targetModel string) []byte {
	var raw map[string]any
	if err := json.Unmarshal(reqBody, &raw); err != nil {
		return reqBody
	}

	genCfg, ok := raw["generationConfig"].(map[string]any)
	if !ok || genCfg == nil {
		genCfg = make(map[string]any)
	}

	thinkingCfg, ok := genCfg["thinkingConfig"].(map[string]any)
	if !ok || thinkingCfg == nil {
		thinkingCfg = make(map[string]any)
	}

	// 始终开启思考流实时可见性（解决后台静默思考导致肉眼严重卡顿的关键）
	thinkingCfg["includeThoughts"] = true

	lowerModel := strings.ToLower(targetModel)
	if strings.Contains(lowerModel, "claude") {
		// 对齐 CPA 生产级约束：thinkingBudget 必须小于 maxOutputTokens
		maxTokens := 4096
		if mOut, ok := genCfg["maxOutputTokens"].(float64); ok && mOut > 0 {
			maxTokens = int(mOut)
		}
		defaultBudget := 2048
		if defaultBudget >= maxTokens {
			defaultBudget = maxTokens - 1
		}
		if defaultBudget < 1 {
			defaultBudget = 1
		}

		if _, exists := thinkingCfg["thinkingBudget"]; !exists {
			thinkingCfg["thinkingBudget"] = defaultBudget
		} else if curBudget, ok := thinkingCfg["thinkingBudget"].(float64); ok && int(curBudget) >= maxTokens {
			thinkingCfg["thinkingBudget"] = maxTokens - 1
		}
	} else {
		// Gemini 3 系列采用 thinkingLevel
		if _, exists := thinkingCfg["thinkingLevel"]; !exists {
			if strings.Contains(lowerModel, "low") {
				thinkingCfg["thinkingLevel"] = "low"
			} else if strings.Contains(lowerModel, "minimal") {
				thinkingCfg["thinkingLevel"] = "minimal"
			} else {
				thinkingCfg["thinkingLevel"] = "high"
			}
		}
	}

	genCfg["thinkingConfig"] = thinkingCfg
	raw["generationConfig"] = genCfg

	modified, err := json.Marshal(raw)
	if err != nil {
		return reqBody
	}
	return modified
}
