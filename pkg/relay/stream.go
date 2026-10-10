package relay

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/convmeta"
	"github.com/QuantumNous/new-api/relaykit/types"
)

// ConvertStreamChunk 将上游流式响应分块 DTO 转换为下游目标格式的响应分块 DTO 列表
func ConvertStreamChunk(ctx context.Context, state *relayconvert.ResponseStreamState, meta convmeta.Meta, chunkDTO any) ([]any, error) {
	if state == nil {
		return nil, errors.New("response stream state is required")
	}
	results, err := relayconvert.ConvertStreamResponseChunk(ctx, meta, state, chunkDTO)
	if err != nil {
		return nil, err
	}
	out := make([]any, len(results))
	for i, r := range results {
		out[i] = r.Value
	}
	return out, nil
}

// FinalizeStream 在流式响应结束时调用，生成终结帧与最终 usage
func FinalizeStream(ctx context.Context, state *relayconvert.ResponseStreamState, meta convmeta.Meta) ([]any, error) {
	if state == nil {
		return nil, errors.New("response stream state is required")
	}
	results, err := relayconvert.FinalizeStreamResponse(ctx, meta, state)
	if err != nil {
		return nil, err
	}
	out := make([]any, len(results))
	for i, r := range results {
		out[i] = r.Value
	}
	return out, nil
}

// SSEEvent 表示解析后的单条 Server-Sent Event
type SSEEvent struct {
	Event string
	Data  []byte
	ID    string
}

// IsDone 判断当前 SSE 是否为流结束标识（如 data: [DONE]）
func (e *SSEEvent) IsDone() bool {
	return bytes.Equal(bytes.TrimSpace(e.Data), []byte("[DONE]"))
}

// SSEReader 是一个轻量级流式 SSE 行解析器
type SSEReader struct {
	reader *bufio.Reader
}

// NewSSEReader 创建新的 SSE 读取解析器
func NewSSEReader(r io.Reader) *SSEReader {
	return &SSEReader{reader: bufio.NewReader(r)}
}

// ReadEvent 从输入流中读取下一个完整的 SSE 事件
func (r *SSEReader) ReadEvent() (*SSEEvent, error) {
	var event SSEEvent
	var dataLines [][]byte
	hasFields := false

	for {
		line, err := r.reader.ReadBytes('\n')
		if err != nil && len(line) == 0 {
			if err == io.EOF && hasFields {
				event.Data = bytes.Join(dataLines, []byte("\n"))
				return &event, nil
			}
			return nil, err
		}

		line = bytes.TrimRight(line, "\r\n")

		// 空行分隔一个完整的事件块
		if len(line) == 0 {
			if hasFields {
				event.Data = bytes.Join(dataLines, []byte("\n"))
				return &event, nil
			}
			continue
		}

		// 忽略以 ':' 开头的注释行
		if line[0] == ':' {
			continue
		}

		hasFields = true
		if bytes.HasPrefix(line, []byte("data:")) {
			val := bytes.TrimPrefix(line, []byte("data:"))
			val = bytes.TrimPrefix(val, []byte(" "))
			dataLines = append(dataLines, val)
		} else if bytes.HasPrefix(line, []byte("event:")) {
			val := bytes.TrimPrefix(line, []byte("event:"))
			val = bytes.TrimPrefix(val, []byte(" "))
			event.Event = string(val)
		} else if bytes.HasPrefix(line, []byte("id:")) {
			val := bytes.TrimPrefix(line, []byte("id:"))
			val = bytes.TrimPrefix(val, []byte(" "))
			event.ID = string(val)
		}
	}
}

// ParseStreamChunk 将 SSE data 字节反序列化为源协议对应的 stream chunk DTO
func ParseStreamChunk(format types.RelayFormat, data []byte) (any, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("[DONE]")) {
		return nil, nil
	}

	switch format {
	case types.RelayFormatOpenAI:
		var chunk dto.ChatCompletionsStreamResponse
		if err := json.Unmarshal(trimmed, &chunk); err != nil {
			return nil, err
		}
		return &chunk, nil

	case types.RelayFormatOpenAIResponses:
		var chunk dto.ResponsesStreamResponse
		if err := json.Unmarshal(trimmed, &chunk); err != nil {
			return nil, err
		}
		return &chunk, nil

	case types.RelayFormatClaude:
		var chunk dto.ClaudeResponse
		if err := json.Unmarshal(trimmed, &chunk); err != nil {
			return nil, err
		}
		return &chunk, nil

	case types.RelayFormatGemini:
		var env struct {
			Response *dto.GeminiChatResponse `json:"response"`
		}
		if err := json.Unmarshal(trimmed, &env); err == nil && env.Response != nil {
			return env.Response, nil
		}
		var chunk dto.GeminiChatResponse
		if err := json.Unmarshal(trimmed, &chunk); err != nil {
			return nil, err
		}
		return &chunk, nil

	default:
		var chunk dto.ChatCompletionsStreamResponse
		if err := json.Unmarshal(trimmed, &chunk); err != nil {
			return nil, err
		}
		return &chunk, nil
	}
}

// FormatStreamChunk 将目标格式的 chunk 格式化为下游所需的 SSE 字节
func FormatStreamChunk(dstFormat types.RelayFormat, val any) ([]byte, error) {
	b, err := json.Marshal(val)
	if err != nil {
		return nil, err
	}

	switch dstFormat {
	case types.RelayFormatClaude:
		if cr, ok := val.(*dto.ClaudeResponse); ok && cr.Type != "" {
			return []byte(fmt.Sprintf("event: %s\ndata: %s\n\n", cr.Type, string(b))), nil
		}
		return []byte(fmt.Sprintf("data: %s\n\n", string(b))), nil

	case types.RelayFormatOpenAIResponses:
		if rr, ok := val.(*dto.ResponsesStreamResponse); ok && rr.Type != "" {
			return []byte(fmt.Sprintf("event: %s\ndata: %s\n\n", rr.Type, string(b))), nil
		}
		return []byte(fmt.Sprintf("data: %s\n\n", string(b))), nil

	default:
		return []byte(fmt.Sprintf("data: %s\n\n", string(b))), nil
	}
}

// EmitStreamResult 将转换后的单条结果序列化并通过 writer 写入
func EmitStreamResult(w io.Writer, dstFormat types.RelayFormat, val any) error {
	formatted, err := FormatStreamChunk(dstFormat, val)
	if err != nil {
		return err
	}
	_, err = w.Write(formatted)
	return err
}

// StreamRelayPump 负责上游流式 SSE 的读取、跨协议转码并实时刷写到客户端
func StreamRelayPump(
	ctx context.Context,
	srcFormat, dstFormat types.RelayFormat,
	upstreamBody io.Reader,
	clientWriter io.Writer,
	flusher http.Flusher,
	reqModel string,
) error {
	// 如果协议相同，执行高性能透明管道转发
	if srcFormat == dstFormat {
		scanner := bufio.NewScanner(upstreamBody)
		for scanner.Scan() {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
			line := scanner.Text()
			_, _ = io.WriteString(clientWriter, line+"\n")
			if flusher != nil {
				flusher.Flush()
			}
		}
		return scanner.Err()
	}

	meta := &convmeta.Values{
		ChannelMetaAttached: true,
		OriginModelName:     reqModel,
		UpstreamModelName:   reqModel,
	}

	state, err := relayconvert.NewResponseStreamState(
		srcFormat,
		dstFormat,
		relayconvert.ResponseStreamOptions{
			ID:           "chatcmpl_relay",
			Model:        reqModel,
			IncludeUsage: true,
		},
	)
	if err != nil {
		return fmt.Errorf("init response stream state: %w", err)
	}

	reader := NewSSEReader(upstreamBody)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		event, err := reader.ReadEvent()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return err
		}

		if event.IsDone() {
			break
		}

		if len(event.Data) == 0 {
			continue
		}

		chunkDTO, err := ParseStreamChunk(srcFormat, event.Data)
		if err != nil || chunkDTO == nil {
			continue
		}

		results, err := ConvertStreamChunk(ctx, state, meta, chunkDTO)
		if err != nil {
			continue
		}

		for _, res := range results {
			if err := EmitStreamResult(clientWriter, dstFormat, res); err != nil {
				return err
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
	}

	// 流结束，补发终结帧与最终 usage
	finalResults, err := FinalizeStream(ctx, state, meta)
	if err == nil {
		for _, res := range finalResults {
			_ = EmitStreamResult(clientWriter, dstFormat, res)
			if flusher != nil {
				flusher.Flush()
			}
		}
	}

	// 若下游是 OpenAI，发送标准终止符
	if dstFormat == types.RelayFormatOpenAI {
		_, _ = io.WriteString(clientWriter, "data: [DONE]\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	}

	return nil
}
