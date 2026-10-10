package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/convmeta"
	"github.com/QuantumNous/new-api/relaykit/types"
)

func TestProtocolMapping(t *testing.T) {
	tests := []struct {
		proto  string
		format types.RelayFormat
	}{
		{ProtocolOpenAIChat, types.RelayFormatOpenAI},
		{ProtocolOpenAIResponses, types.RelayFormatOpenAIResponses},
		{ProtocolClaudeMessages, types.RelayFormatClaude},
		{ProtocolGemini, types.RelayFormatGemini},
	}

	for _, tt := range tests {
		gotFormat, err := ProtocolToRelayFormat(tt.proto)
		if err != nil {
			t.Fatalf("ProtocolToRelayFormat(%q) error: %v", tt.proto, err)
		}
		if gotFormat != tt.format {
			t.Errorf("ProtocolToRelayFormat(%q) = %v, want %v", tt.proto, gotFormat, tt.format)
		}

		gotProto, err := RelayFormatToProtocol(tt.format)
		if err != nil {
			t.Fatalf("RelayFormatToProtocol(%v) error: %v", tt.format, err)
		}
		if gotProto != tt.proto {
			t.Errorf("RelayFormatToProtocol(%v) = %q, want %q", tt.format, gotProto, tt.proto)
		}

		if StringToRelayFormat(tt.proto) != tt.format {
			t.Errorf("StringToRelayFormat(%q) != %v", tt.proto, tt.format)
		}
		if RelayFormatToString(tt.format) != tt.proto {
			t.Errorf("RelayFormatToString(%v) != %q", tt.format, tt.proto)
		}
	}

	// Unknown protocol error handling
	if _, err := ProtocolToRelayFormat("unknown_proto"); err == nil {
		t.Errorf("expected error for unknown protocol")
	}
	if _, err := RelayFormatToProtocol(types.RelayFormat("unknown_fmt")); err == nil {
		t.Errorf("expected error for unknown relay format")
	}
}

func TestDetectClientProtocol(t *testing.T) {
	tests := []struct {
		path string
		want types.RelayFormat
	}{
		{"/v1/chat/completions", types.RelayFormatOpenAI},
		{"/chat/completions", types.RelayFormatOpenAI},
		{"/v1/chat/completions?stream=true", types.RelayFormatOpenAI},
		{"/v1/responses", types.RelayFormatOpenAIResponses},
		{"/responses", types.RelayFormatOpenAIResponses},
		{"/v1/messages", types.RelayFormatClaude},
		{"/messages", types.RelayFormatClaude},
		{"/v1beta/models/gemini-1.5-pro:generateContent", types.RelayFormatGemini},
		{"/v1beta/models/gemini-1.5-pro:streamGenerateContent?alt=sse", types.RelayFormatGemini},
		{"/unknown/path", types.RelayFormatOpenAI}, // default fallback
	}

	for _, tt := range tests {
		got := DetectClientProtocol(tt.path)
		if got != tt.want {
			t.Errorf("DetectClientProtocol(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}

func TestParseRequestAllFormats(t *testing.T) {
	// 1. OpenAI Chat
	oaiJSON := []byte(`{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	dtoObj, model, stream, err := ParseRequest(types.RelayFormatOpenAI, oaiJSON)
	if err != nil {
		t.Fatalf("ParseRequest OpenAI failed: %v", err)
	}
	if model != "gpt-4o" || !stream {
		t.Errorf("OpenAI request: model=%s, stream=%v", model, stream)
	}
	if _, ok := dtoObj.(*dto.GeneralOpenAIRequest); !ok {
		t.Errorf("OpenAI request type expected *dto.GeneralOpenAIRequest, got %T", dtoObj)
	}

	// 2. OpenAI Responses
	respJSON := []byte(`{"model":"gpt-4o","stream":true,"input":"hello"}`)
	dtoObj, model, stream, err = ParseRequest(types.RelayFormatOpenAIResponses, respJSON)
	if err != nil {
		t.Fatalf("ParseRequest Responses failed: %v", err)
	}
	if model != "gpt-4o" || !stream {
		t.Errorf("Responses request: model=%s, stream=%v", model, stream)
	}
	if _, ok := dtoObj.(*dto.OpenAIResponsesRequest); !ok {
		t.Errorf("Responses request type expected *dto.OpenAIResponsesRequest, got %T", dtoObj)
	}

	// 3. Claude
	claudeJSON := []byte(`{"model":"claude-3-5-sonnet-20241022","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	dtoObj, model, stream, err = ParseRequest(types.RelayFormatClaude, claudeJSON)
	if err != nil {
		t.Fatalf("ParseRequest Claude failed: %v", err)
	}
	if model != "claude-3-5-sonnet-20241022" || !stream {
		t.Errorf("Claude request: model=%s, stream=%v", model, stream)
	}
	if _, ok := dtoObj.(*dto.ClaudeRequest); !ok {
		t.Errorf("Claude request type expected *dto.ClaudeRequest, got %T", dtoObj)
	}

	// 4. Gemini
	geminiJSON := []byte(`{"contents":[{"parts":[{"text":"hi"}]}]}`)
	dtoObj, model, stream, err = ParseRequest(types.RelayFormatGemini, geminiJSON)
	if err != nil {
		t.Fatalf("ParseRequest Gemini failed: %v", err)
	}
	if stream {
		t.Errorf("Gemini stream expected false, got %v", stream)
	}
	if _, ok := dtoObj.(*dto.GeminiChatRequest); !ok {
		t.Errorf("Gemini request type expected *dto.GeminiChatRequest, got %T", dtoObj)
	}
}

func TestOpenAIToClaudeRequest(t *testing.T) {
	ctx := context.Background()
	openaiBody := []byte(`{
		"model": "gpt-4o",
		"messages": [
			{"role": "user", "content": "Hello Claude!"}
		],
		"stream": false
	}`)

	reqDTO, model, stream, err := ParseRequest(types.RelayFormatOpenAI, openaiBody)
	if err != nil {
		t.Fatalf("ParseRequest failed: %v", err)
	}
	if model != "gpt-4o" || stream {
		t.Fatalf("unexpected model=%s or stream=%v", model, stream)
	}

	convertedBytes, err := ConvertRequest(ctx, types.RelayFormatOpenAI, types.RelayFormatClaude, reqDTO, "claude-3-7-sonnet")
	if err != nil {
		t.Fatalf("ConvertRequest to Claude failed: %v", err)
	}

	var claudeReq dto.ClaudeRequest
	if err := json.Unmarshal(convertedBytes, &claudeReq); err != nil {
		t.Fatalf("unmarshal claude request failed: %v", err)
	}

	if claudeReq.Model != "claude-3-7-sonnet" {
		t.Errorf("expected model claude-3-7-sonnet, got %s", claudeReq.Model)
	}
	if len(claudeReq.Messages) == 0 {
		t.Errorf("expected messages non-empty")
	}
}

func TestOpenAIToGeminiRequest(t *testing.T) {
	ctx := context.Background()
	openaiBody := []byte(`{
		"model": "gpt-4o",
		"messages": [
			{"role": "user", "content": "Hello Gemini!"}
		]
	}`)

	reqDTO, _, _, err := ParseRequest(types.RelayFormatOpenAI, openaiBody)
	if err != nil {
		t.Fatalf("ParseRequest failed: %v", err)
	}

	convertedBytes, err := ConvertRequest(ctx, types.RelayFormatOpenAI, types.RelayFormatGemini, reqDTO, "gemini-2.5-pro")
	if err != nil {
		t.Fatalf("ConvertRequest to Gemini failed: %v", err)
	}

	var geminiReq dto.GeminiChatRequest
	if err := json.Unmarshal(convertedBytes, &geminiReq); err != nil {
		t.Fatalf("unmarshal gemini request failed: %v", err)
	}

	if len(geminiReq.Contents) == 0 {
		t.Errorf("expected gemini contents non-empty")
	}
}

func TestClaudeToOpenAIResponse(t *testing.T) {
	ctx := context.Background()
	claudeResp := []byte(`{
		"id": "msg_01",
		"type": "message",
		"role": "assistant",
		"model": "claude-3-7-sonnet",
		"content": [
			{"type": "text", "text": "Hello from Claude!"}
		],
		"stop_reason": "end_turn",
		"usage": {"input_tokens": 10, "output_tokens": 20}
	}`)

	convertedBytes, err := ConvertResponse(ctx, types.RelayFormatClaude, types.RelayFormatOpenAI, claudeResp, "my-claude")
	if err != nil {
		t.Fatalf("ConvertResponse failed: %v", err)
	}

	var openaiResp dto.OpenAITextResponse
	if err := json.Unmarshal(convertedBytes, &openaiResp); err != nil {
		t.Fatalf("unmarshal openai response failed: %v", err)
	}

	if len(openaiResp.Choices) == 0 {
		t.Fatalf("expected choices non-empty")
	}
	content, ok := openaiResp.Choices[0].Message.Content.(string)
	if !ok || !strings.Contains(content, "Hello from Claude!") {
		t.Errorf("unexpected content: %v", openaiResp.Choices[0].Message.Content)
	}
}

func TestGeminiToOpenAIResponse(t *testing.T) {
	ctx := context.Background()
	geminiResp := []byte(`{
		"candidates": [
			{
				"content": {
					"parts": [
						{"text": "Hello from Gemini!"}
					],
					"role": "model"
				},
				"finishReason": "STOP"
			}
		],
		"usageMetadata": {
			"promptTokenCount": 5,
			"candidatesTokenCount": 10,
			"totalTokenCount": 15
		}
	}`)

	convertedBytes, err := ConvertResponse(ctx, types.RelayFormatGemini, types.RelayFormatOpenAI, geminiResp, "my-gemini")
	if err != nil {
		t.Fatalf("ConvertResponse failed: %v", err)
	}

	var openaiResp dto.OpenAITextResponse
	if err := json.Unmarshal(convertedBytes, &openaiResp); err != nil {
		t.Fatalf("unmarshal openai response failed: %v", err)
	}

	if len(openaiResp.Choices) == 0 {
		t.Fatalf("expected choices non-empty")
	}
	content, ok := openaiResp.Choices[0].Message.Content.(string)
	if !ok || !strings.Contains(content, "Hello from Gemini!") {
		t.Errorf("unexpected content: %v", openaiResp.Choices[0].Message.Content)
	}
}

func TestSameFormatPassThrough(t *testing.T) {
	ctx := context.Background()

	// 1. Same format request conversion
	req := &dto.GeneralOpenAIRequest{
		Model: "gpt-4o-mini",
	}
	outReq, err := ConvertRequest(ctx, types.RelayFormatOpenAI, types.RelayFormatOpenAI, req, "gpt-4o")
	if err != nil {
		t.Fatalf("ConvertRequest same format failed: %v", err)
	}
	var parsedReq dto.GeneralOpenAIRequest
	_ = json.Unmarshal(outReq, &parsedReq)
	if parsedReq.Model != "gpt-4o" {
		t.Errorf("expected updated model gpt-4o, got %s", parsedReq.Model)
	}

	// 2. Same format response conversion
	respRaw := []byte(`{"id":"chatcmpl-test","choices":[{"index":0}]}`)
	outResp, err := ConvertResponse(ctx, types.RelayFormatOpenAI, types.RelayFormatOpenAI, respRaw, "gpt-4o")
	if err != nil {
		t.Fatalf("ConvertResponse same format failed: %v", err)
	}
	if !bytes.Equal(outResp, respRaw) {
		t.Errorf("expected exact raw bytes pass-through")
	}
}

func TestSSEReaderAndParsing(t *testing.T) {
	rawSSE := ": this is a comment\r\n" +
		"event: message_start\r\n" +
		"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"model\":\"claude-test\"}}\r\n\r\n" +
		"data: [DONE]\r\n\r\n"

	reader := NewSSEReader(strings.NewReader(rawSSE))

	ev1, err := reader.ReadEvent()
	if err != nil {
		t.Fatalf("ev1 ReadEvent error: %v", err)
	}
	if ev1.Event != "message_start" {
		t.Errorf("ev1 event expected 'message_start', got %q", ev1.Event)
	}
	if !strings.Contains(string(ev1.Data), "message_start") {
		t.Errorf("ev1 data mismatch: %s", string(ev1.Data))
	}
	if ev1.IsDone() {
		t.Errorf("ev1 should not be done")
	}

	ev2, err := reader.ReadEvent()
	if err != nil {
		t.Fatalf("ev2 ReadEvent error: %v", err)
	}
	if !ev2.IsDone() {
		t.Errorf("ev2 expected to be [DONE]")
	}

	_, err = reader.ReadEvent()
	if err != io.EOF {
		t.Errorf("expected io.EOF at end of stream, got %v", err)
	}
}

func TestConvertStreamChunk(t *testing.T) {
	ctx := context.Background()
	state, err := relayconvert.NewResponseStreamState(
		types.RelayFormatOpenAI,
		types.RelayFormatOpenAIResponses,
		relayconvert.ResponseStreamOptions{
			ID:    "resp_stream_test",
			Model: "gpt-4o",
		},
	)
	if err != nil {
		t.Fatalf("NewResponseStreamState error: %v", err)
	}

	text := "Hello world stream"
	chunk := &dto.ChatCompletionsStreamResponse{
		Id:    "chatcmpl-stream-1",
		Model: "gpt-4o",
		Choices: []dto.ChatCompletionsStreamResponseChoice{
			{
				Index: 0,
				Delta: dto.ChatCompletionsStreamResponseChoiceDelta{
					Content: &text,
				},
			},
		},
	}

	meta := &convmeta.Values{
		ChannelMetaAttached: true,
		UpstreamModelName:   "gpt-4o",
	}

	results, err := ConvertStreamChunk(ctx, state, meta, chunk)
	if err != nil {
		t.Fatalf("ConvertStreamChunk failed: %v", err)
	}
	if len(results) == 0 {
		t.Fatalf("expected non-empty converted stream chunks")
	}

	finals, err := FinalizeStream(ctx, state, meta)
	if err != nil {
		t.Fatalf("FinalizeStream failed: %v", err)
	}
	_ = finals
}

func TestStreamRelayPumpSameFormat(t *testing.T) {
	ctx := context.Background()
	upstreamStream := "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\ndata: [DONE]\n\n"
	inBuf := strings.NewReader(upstreamStream)
	var outBuf bytes.Buffer

	err := StreamRelayPump(ctx, types.RelayFormatOpenAI, types.RelayFormatOpenAI, inBuf, &outBuf, nil, "gpt-4o")
	if err != nil {
		t.Fatalf("StreamRelayPump failed: %v", err)
	}

	out := outBuf.String()
	if !strings.Contains(out, "hello") {
		t.Errorf("expected out to contain hello, got: %s", out)
	}
}

func TestFormatToEndpoint(t *testing.T) {
	if got := FormatToEndpoint(types.RelayFormatOpenAI, "gpt-4o", false); got != "/v1/chat/completions" {
		t.Errorf("unexpected endpoint: %s", got)
	}
	if got := FormatToEndpoint(types.RelayFormatClaude, "claude-3", false); got != "/v1/messages" {
		t.Errorf("unexpected endpoint: %s", got)
	}
	if got := FormatToEndpoint(types.RelayFormatGemini, "gemini-1.5", true); !strings.Contains(got, "streamGenerateContent") {
		t.Errorf("unexpected gemini stream endpoint: %s", got)
	}
	if got := FormatToEndpoint(types.RelayFormatGemini, "gemini-1.5", false); !strings.Contains(got, "generateContent") {
		t.Errorf("unexpected gemini endpoint: %s", got)
	}
}
