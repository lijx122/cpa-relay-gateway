package relay

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/convmeta"
	"github.com/QuantumNous/new-api/relaykit/types"
)

// ParseRequest 解析入站请求字节为对应协议格式的 DTO，并提取请求模型和 stream 标记
func ParseRequest(format types.RelayFormat, body []byte) (dtoObj any, model string, stream bool, err error) {
	switch format {
	case types.RelayFormatOpenAI:
		var req dto.GeneralOpenAIRequest
		if err := json.Unmarshal(body, &req); err != nil {
			return nil, "", false, fmt.Errorf("unmarshal openai chat request: %w", err)
		}
		isStream := req.Stream != nil && *req.Stream
		return &req, req.Model, isStream, nil

	case types.RelayFormatOpenAIResponses:
		var req dto.OpenAIResponsesRequest
		if err := json.Unmarshal(body, &req); err != nil {
			return nil, "", false, fmt.Errorf("unmarshal openai responses request: %w", err)
		}
		isStream := req.Stream != nil && *req.Stream
		return &req, req.Model, isStream, nil

	case types.RelayFormatClaude:
		var req dto.ClaudeRequest
		if err := json.Unmarshal(body, &req); err != nil {
			return nil, "", false, fmt.Errorf("unmarshal claude request: %w", err)
		}
		isStream := req.Stream != nil && *req.Stream
		return &req, req.Model, isStream, nil

	case types.RelayFormatGemini:
		var req dto.GeminiChatRequest
		if err := json.Unmarshal(body, &req); err != nil {
			return nil, "", false, fmt.Errorf("unmarshal gemini request: %w", err)
		}
		// Gemini 请求体通常不包含 model，尝试提取顶层 JSON 中的 model 字段兜底
		var raw struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(body, &raw)
		return &req, raw.Model, false, nil

	default:
		var req dto.GeneralOpenAIRequest
		if err := json.Unmarshal(body, &req); err != nil {
			return nil, "", false, fmt.Errorf("unmarshal default openai request: %w", err)
		}
		isStream := req.Stream != nil && *req.Stream
		return &req, req.Model, isStream, nil
	}
}

// ConvertRequest 将源 DTO 转换为目标协议的 JSON 字节负载
func ConvertRequest(ctx context.Context, srcFormat, dstFormat types.RelayFormat, reqDTO any, targetModel string) (outBody []byte, err error) {
	if srcFormat == dstFormat {
		if targetModel != "" {
			setModelName(reqDTO, targetModel)
		}
		return json.Marshal(reqDTO)
	}

	meta := &convmeta.Values{
		ChannelMetaAttached: true,
		OriginModelName:   targetModel,
		UpstreamModelName: targetModel,
		Options: &convmeta.Options{
			Claude: convmeta.ClaudeOptions{
				DefaultMaxTokens: func(model string) int {
					return 4096
				},
			},
		},
	}

	result, err := relayconvert.ConvertRequest(ctx, meta, dstFormat, reqDTO)
	if err != nil {
		return nil, fmt.Errorf("convert request from %s to %s: %w", srcFormat, dstFormat, err)
	}

	if targetModel != "" {
		setModelName(result.Value, targetModel)
	}

	return json.Marshal(result.Value)
}

// setModelName 为各种请求或响应 DTO 设置模型名
func setModelName(dtoObj any, model string) {
	if model == "" || dtoObj == nil {
		return
	}
	switch r := dtoObj.(type) {
	case *dto.GeneralOpenAIRequest:
		r.Model = model
	case *dto.OpenAIResponsesRequest:
		r.Model = model
	case *dto.ClaudeRequest:
		r.Model = model
	case *dto.GeminiChatRequest:
		r.SetModelName(model)
	case *dto.OpenAITextResponse:
		r.Model = model
	case *dto.OpenAIResponsesResponse:
		r.Model = model
	case *dto.ClaudeResponse:
		r.Model = model
	}
}

// ConvertResponse 将上游返回的响应转换为下游客户端期望的响应格式
func ConvertResponse(ctx context.Context, srcFormat, dstFormat types.RelayFormat, upstreamBody []byte, model string) (outBody []byte, err error) {
	if srcFormat == dstFormat {
		return upstreamBody, nil
	}

	respDTO, err := parseResponse(srcFormat, upstreamBody)
	if err != nil {
		return nil, fmt.Errorf("parse upstream response for %s: %w", srcFormat, err)
	}

	meta := &convmeta.Values{
		ChannelMetaAttached: true,
		OriginModelName:     model,
		UpstreamModelName:   model,
	}

	result, err := relayconvert.ConvertResponse(ctx, meta, dstFormat, respDTO)
	if err != nil {
		return nil, fmt.Errorf("convert response from %s to %s: %w", srcFormat, dstFormat, err)
	}

	if model != "" {
		setModelName(result.Value, model)
	}

	return json.Marshal(result.Value)
}

func parseResponse(format types.RelayFormat, body []byte) (any, error) {
	switch format {
	case types.RelayFormatOpenAI:
		var resp dto.OpenAITextResponse
		if err := json.Unmarshal(body, &resp); err != nil {
			return nil, err
		}
		return &resp, nil

	case types.RelayFormatOpenAIResponses:
		var resp dto.OpenAIResponsesResponse
		if err := json.Unmarshal(body, &resp); err != nil {
			return nil, err
		}
		return &resp, nil

	case types.RelayFormatClaude:
		var resp dto.ClaudeResponse
		if err := json.Unmarshal(body, &resp); err != nil {
			return nil, err
		}
		return &resp, nil

	case types.RelayFormatGemini:
		var env struct {
			Response *dto.GeminiChatResponse `json:"response"`
		}
		if err := json.Unmarshal(body, &env); err == nil && env.Response != nil && len(env.Response.Candidates) > 0 {
			return env.Response, nil
		}
		var resp dto.GeminiChatResponse
		if err := json.Unmarshal(body, &resp); err != nil {
			return nil, err
		}
		return &resp, nil

	default:
		var resp dto.OpenAITextResponse
		if err := json.Unmarshal(body, &resp); err != nil {
			return nil, err
		}
		return &resp, nil
	}
}
