package relay

import (
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/relaykit/types"
)

// Protocol 字符串定义
const (
	ProtocolOpenAIChat      = "openai_chat"
	ProtocolOpenAIResponses = "openai_responses"
	ProtocolClaudeMessages  = "claude_messages"
	ProtocolGemini          = "gemini"
)

// ProtocolToRelayFormat 将协议字符串精确转换为 RelayKit 的 types.RelayFormat
func ProtocolToRelayFormat(protocol string) (types.RelayFormat, error) {
	switch strings.ToLower(protocol) {
	case ProtocolOpenAIChat, "openai":
		return types.RelayFormatOpenAI, nil
	case ProtocolOpenAIResponses, "responses":
		return types.RelayFormatOpenAIResponses, nil
	case ProtocolClaudeMessages, "claude":
		return types.RelayFormatClaude, nil
	case ProtocolGemini:
		return types.RelayFormatGemini, nil
	default:
		return "", fmt.Errorf("unknown protocol: %q", protocol)
	}
}

// RelayFormatToProtocol 将 RelayKit 的 types.RelayFormat 转换为协议字符串
func RelayFormatToProtocol(format types.RelayFormat) (string, error) {
	switch format {
	case types.RelayFormatOpenAI:
		return ProtocolOpenAIChat, nil
	case types.RelayFormatOpenAIResponses:
		return ProtocolOpenAIResponses, nil
	case types.RelayFormatClaude:
		return ProtocolClaudeMessages, nil
	case types.RelayFormatGemini:
		return ProtocolGemini, nil
	default:
		return "", fmt.Errorf("unknown relay format: %q", format)
	}
}

// StringToRelayFormat 辅助转换函数，未知协议返回 RelayFormatOpenAI
func StringToRelayFormat(protocol string) types.RelayFormat {
	format, err := ProtocolToRelayFormat(protocol)
	if err != nil {
		return types.RelayFormatOpenAI
	}
	return format
}

// RelayFormatToString 辅助转换函数
func RelayFormatToString(format types.RelayFormat) string {
	proto, err := RelayFormatToProtocol(format)
	if err != nil {
		return string(format)
	}
	return proto
}

// DetectClientProtocol 根据请求 URL 路径识别下游客户端的协议格式
func DetectClientProtocol(path string) types.RelayFormat {
	cleanPath := path
	if idx := strings.Index(cleanPath, "?"); idx != -1 {
		cleanPath = cleanPath[:idx]
	}
	cleanPath = strings.TrimRight(cleanPath, "/")

	lowerPath := strings.ToLower(cleanPath)
	switch {
	case strings.Contains(cleanPath, "generateContent") || strings.Contains(lowerPath, "generatecontent"):
		return types.RelayFormatGemini
	case cleanPath == "/v1/chat/completions" || strings.HasSuffix(cleanPath, "/v1/chat/completions") || strings.Contains(cleanPath, "/chat/completions"):
		return types.RelayFormatOpenAI
	case cleanPath == "/v1/responses" || strings.HasSuffix(cleanPath, "/v1/responses") || strings.Contains(cleanPath, "/responses"):
		return types.RelayFormatOpenAIResponses
	case cleanPath == "/v1/messages" || strings.HasSuffix(cleanPath, "/v1/messages") || strings.Contains(cleanPath, "/messages"):
		return types.RelayFormatClaude
	default:
		return types.RelayFormatOpenAI
	}
}

// FormatToEndpoint 根据协议和目标模型名生成上游相对路径
func FormatToEndpoint(format types.RelayFormat, targetModel string, isStream bool) string {
	switch format {
	case types.RelayFormatOpenAI:
		return "/v1/chat/completions"
	case types.RelayFormatOpenAIResponses:
		return "/v1/responses"
	case types.RelayFormatClaude:
		return "/v1/messages"
	case types.RelayFormatGemini:
		if isStream {
			return "/v1beta/models/" + targetModel + ":streamGenerateContent?alt=sse"
		}
		return "/v1beta/models/" + targetModel + ":generateContent"
	default:
		return "/v1/chat/completions"
	}
}
