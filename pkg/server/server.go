package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"cpa-relay-gateway/pkg/authpool"
	"cpa-relay-gateway/pkg/config"
	"cpa-relay-gateway/pkg/relay"
	"github.com/QuantumNous/new-api/relaykit/types"
)

var geminiModelRegex = regexp.MustCompile(`/v1beta/models/([^/:]+)`)

type Server struct {
	cfgManager *config.Manager
	httpClient *http.Client
	dispatcher *authpool.Dispatcher
	mux        *http.ServeMux

	poolMu sync.RWMutex
	pools  map[string]*authpool.Pool
}

func NewServer(cfgManager *config.Manager) *Server {
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   32,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	s := &Server{
		cfgManager: cfgManager,
		httpClient: &http.Client{
			Transport: transport,
			Timeout:   120 * time.Second,
		},
		dispatcher: authpool.NewDispatcher(),
		mux:        http.NewServeMux(),
		pools:      make(map[string]*authpool.Pool),
	}
	s.routes()
	return s
}

func (s *Server) Handler() http.Handler {
	return s.mux
}

func (s *Server) routes() {
	s.mux.HandleFunc("/health", s.handleHealth)
	s.mux.HandleFunc("/v1/models", s.handleModels)
	s.mux.HandleFunc("/v1/chat/completions", s.handleRelay)
	s.mux.HandleFunc("/v1/responses", s.handleRelay)
	s.mux.HandleFunc("/v1/messages", s.handleRelay)
	s.mux.HandleFunc("/v1beta/models/", s.handleRelay)
}

func (s *Server) getOrCreatePool(pCfg *config.AuthPoolConfig) (*authpool.Pool, error) {
	s.poolMu.RLock()
	p, ok := s.pools[pCfg.Name]
	s.poolMu.RUnlock()
	if ok {
		return p, nil
	}

	s.poolMu.Lock()
	defer s.poolMu.Unlock()
	if p, ok := s.pools[pCfg.Name]; ok {
		return p, nil
	}

	newP, err := authpool.NewPool(pCfg.AuthDir, pCfg.Type)
	if err != nil {
		return nil, err
	}
	_ = newP.StartWatcher(context.Background())
	s.pools[pCfg.Name] = newP
	return newP, nil
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfgManager.Get()
	if cfg == nil {
		http.Error(w, `{"error":{"message":"configuration not loaded"}}`, http.StatusInternalServerError)
		return
	}

	type ModelItem struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Created int64  `json:"created"`
		OwnedBy string `json:"owned_by"`
	}

	var list []ModelItem
	seen := make(map[string]bool)

	for modelName := range cfg.ModelMappings {
		if !seen[modelName] {
			seen[modelName] = true
			list = append(list, ModelItem{ID: modelName, Object: "model", Created: time.Now().Unix(), OwnedBy: "cpa-relay"})
		}
	}
	for _, pool := range cfg.AuthPools {
		for _, m := range pool.Models {
			if !seen[m] {
				seen[m] = true
				list = append(list, ModelItem{ID: m, Object: "model", Created: time.Now().Unix(), OwnedBy: "authpool:" + pool.Name})
			}
		}
	}
	for _, ch := range cfg.Channels {
		for _, m := range ch.Models {
			if !seen[m] {
				seen[m] = true
				list = append(list, ModelItem{ID: m, Object: "model", Created: time.Now().Unix(), OwnedBy: ch.Name})
			}
		}
	}

	resp := map[string]any{
		"object": "list",
		"data":   list,
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleRelay(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":{"message":"method not allowed"}}`, http.StatusMethodNotAllowed)
		return
	}

	clientFormat := relay.DetectClientProtocol(r.URL.Path)

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":{"message":"read request body: %s"}}`, err.Error()), http.StatusBadRequest)
		return
	}

	reqDTO, reqModel, isStream, err := relay.ParseRequest(clientFormat, body)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":{"message":"parse request: %s"}}`, err.Error()), http.StatusBadRequest)
		return
	}

	// 若 Gemini URL 中包含模型名且请求体未指定，从 URL 中提取
	if reqModel == "" && clientFormat == types.RelayFormatGemini {
		if m := geminiModelRegex.FindStringSubmatch(r.URL.Path); len(m) > 1 {
			reqModel = m[1]
		}
	}
	if strings.Contains(r.URL.Path, "streamGenerateContent") || r.URL.Query().Get("alt") == "sse" {
		isStream = true
	}

	// 统一路由解析
	routeTarget, err := s.cfgManager.ResolveRoute(reqModel)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(fmt.Sprintf(`{"error":{"message":"model %q not found or route error: %s"}}`, reqModel, err.Error())))
		return
	}

	var (
		targetProtocol types.RelayFormat
		upResp         *http.Response
	)

	// 分支 1：原生 AuthPool 凭据池直连官方（纯内部直接打上游，不经任何本地外部进程）
	if routeTarget.AuthPool != nil {
		pool, err := s.getOrCreatePool(routeTarget.AuthPool)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(fmt.Sprintf(`{"error":{"message":"init auth pool %s: %s"}}`, routeTarget.AuthPool.Name, err.Error())))
			return
		}

		authRec, err := pool.Next()
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(fmt.Sprintf(`{"error":{"message":"select credential: %s"}}`, err.Error())))
			return
		}

		switch strings.ToLower(authRec.Type) {
		case "xai":
			targetProtocol = types.RelayFormatOpenAI
		case "codex":
			targetProtocol = types.RelayFormatOpenAIResponses
		case "antigravity":
			targetProtocol = types.RelayFormatGemini
		default:
			targetProtocol = types.RelayFormatOpenAI
		}

		outBody, err := relay.ConvertRequest(r.Context(), clientFormat, targetProtocol, reqDTO, routeTarget.TargetModel)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(fmt.Sprintf(`{"error":{"message":"convert request: %s"}}`, err.Error())))
			return
		}

		upResp, _, err = s.dispatcher.DirectExecute(r.Context(), authRec, routeTarget.TargetModel, outBody, isStream)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(fmt.Sprintf(`{"error":{"message":"direct execute to upstream failed: %s"}}`, err.Error())))
			return
		}
	} else {
		// 分支 2：常规商业 API 渠道
		channel := routeTarget.Channel
		targetProtocol = relay.StringToRelayFormat(channel.Protocol)

		outBody, err := relay.ConvertRequest(r.Context(), clientFormat, targetProtocol, reqDTO, routeTarget.TargetModel)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(fmt.Sprintf(`{"error":{"message":"convert request: %s"}}`, err.Error())))
			return
		}

		endpoint := relay.FormatToEndpoint(targetProtocol, routeTarget.TargetModel, isStream)
		baseURL := strings.TrimRight(channel.BaseURL, "/")
		if strings.HasSuffix(baseURL, "/v1") && strings.HasPrefix(endpoint, "/v1/") {
			endpoint = strings.TrimPrefix(endpoint, "/v1")
		}
		upstreamURL := baseURL + endpoint

		upReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, upstreamURL, bytes.NewReader(outBody))
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":{"message":"create upstream request: %s"}}`, err.Error()), http.StatusInternalServerError)
			return
		}

		upReq.Header.Set("Content-Type", "application/json")
		injectAuthHeaders(upReq, targetProtocol, channel.APIKey, r.Header.Get("Authorization"))

		upResp, err = s.httpClient.Do(upReq)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(fmt.Sprintf(`{"error":{"message":"call upstream failed: %s"}}`, err.Error())))
			return
		}
	}
	defer upResp.Body.Close()

	// 上游异常直接透传错误状态码
	if upResp.StatusCode >= 400 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(upResp.StatusCode)
		_, _ = io.Copy(w, upResp.Body)
		return
	}

	// 流式处理
	if isStream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)

		flusher, _ := w.(http.Flusher)
		_ = relay.StreamRelayPump(r.Context(), targetProtocol, clientFormat, upResp.Body, w, flusher, reqModel)
		return
	}

	// 非流式响应转换
	upBody, err := io.ReadAll(upResp.Body)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":{"message":"read upstream body: %s"}}`, err.Error()), http.StatusInternalServerError)
		return
	}

	clientResp, err := relay.ConvertResponse(r.Context(), targetProtocol, clientFormat, upBody, reqModel)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(fmt.Sprintf(`{"error":{"message":"convert response: %s"}}`, err.Error())))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(clientResp)
}

func injectAuthHeaders(req *http.Request, protocol types.RelayFormat, apiKey, clientAuth string) {
	key := apiKey
	if key == "" && clientAuth != "" {
		key = strings.TrimPrefix(clientAuth, "Bearer ")
	}
	if key == "" {
		return
	}

	switch protocol {
	case types.RelayFormatClaude:
		req.Header.Set("x-api-key", key)
		req.Header.Set("anthropic-version", "2023-06-01")
	case types.RelayFormatGemini:
		req.Header.Set("x-goog-api-key", key)
	default:
		req.Header.Set("Authorization", "Bearer "+key)
	}
}
