package service

import (
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
)

func TestBuildAuditContentResponses(t *testing.T) {
	t.Setenv(requestContentAuditIncludeSystemEnv, "true")

	req := &dto.OpenAIResponsesRequest{
		Model:        "gpt-test",
		Instructions: []byte(`"answer briefly"`),
		Input:        []byte(`[{"role":"user","content":"今天宣城的天气是什么"}]`),
	}
	info := &relaycommon.RelayInfo{
		OriginModelName: "gpt-test",
		RelayMode:       37,
	}

	content := buildAuditContent(info, req)
	sanitized := sanitizeAuditValue(content)
	text := strings.Join(collectAuditText("", sanitized), "\n")

	if !strings.Contains(text, "今天宣城的天气是什么") {
		t.Fatalf("audit text does not contain user input: %q", text)
	}
	if !strings.Contains(text, "answer briefly") {
		t.Fatalf("audit text does not contain instructions: %q", text)
	}
}

func TestBuildAuditContentResponsesKeepsOnlyLatestUserInputByDefault(t *testing.T) {
	req := &dto.OpenAIResponsesRequest{
		Model: "gpt-test",
		Input: []byte(`[
			{"type":"message","role":"developer","content":[{"type":"input_text","text":"developer instructions"}]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"old user question"}]},
			{"type":"reasoning","summary":[{"text":"reasoning text"}]},
			{"type":"function_call","name":"lookup","arguments":"{\"query\":\"tool arguments\"}"},
			{"type":"function_call_output","output":"tool output text"},
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"assistant answer"}]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"latest user question"}]}
		]`),
	}
	info := &relaycommon.RelayInfo{
		OriginModelName: "gpt-test",
		RelayMode:       37,
	}

	content := buildAuditContent(info, req)
	sanitized := sanitizeAuditValue(content)
	text := strings.Join(collectAuditText("", sanitized), "\n")
	bytes, err := buildAuditJSONForTest(sanitized)
	if err != nil {
		t.Fatal(err)
	}
	output := string(bytes)

	if !strings.Contains(text, "latest user question") {
		t.Fatalf("audit text does not contain latest user input: %q", text)
	}
	for _, unexpected := range []string{
		"old user question",
		"developer instructions",
		"reasoning text",
		"tool arguments",
		"tool output text",
		"assistant answer",
	} {
		if strings.Contains(text, unexpected) || strings.Contains(output, unexpected) {
			t.Fatalf("audit content leaked %q\ntext=%q\njson=%s", unexpected, text, output)
		}
	}
}

func TestBuildAuditContentResponsesCanKeepAllUserInput(t *testing.T) {
	t.Setenv(requestContentAuditScopeEnv, auditScopeAllUser)

	req := &dto.OpenAIResponsesRequest{
		Model: "gpt-test",
		Input: []byte(`[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"first user question"}]},
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"assistant answer"}]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"second user question"}]}
		]`),
	}
	info := &relaycommon.RelayInfo{
		OriginModelName: "gpt-test",
		RelayMode:       37,
	}

	content := buildAuditContent(info, req)
	sanitized := sanitizeAuditValue(content)
	text := strings.Join(collectAuditText("", sanitized), "\n")

	if !strings.Contains(text, "first user question") || !strings.Contains(text, "second user question") {
		t.Fatalf("audit text does not contain all user input: %q", text)
	}
	if strings.Contains(text, "assistant answer") {
		t.Fatalf("audit text leaked assistant content: %q", text)
	}
}

func TestBuildAuditContentChatKeepsOnlyLatestUserMessageByDefault(t *testing.T) {
	req := &dto.GeneralOpenAIRequest{
		Model: "gpt-test",
		Messages: []dto.Message{
			{Role: "system", Content: "system instructions"},
			{Role: "user", Content: "old user message"},
			{Role: "assistant", Content: "assistant message"},
			{Role: "user", Content: "latest user message"},
		},
	}
	info := &relaycommon.RelayInfo{
		OriginModelName: "gpt-test",
		RelayMode:       1,
	}

	content := buildAuditContent(info, req)
	sanitized := sanitizeAuditValue(content)
	text := strings.Join(collectAuditText("", sanitized), "\n")

	if !strings.Contains(text, "latest user message") {
		t.Fatalf("audit text does not contain latest user message: %q", text)
	}
	for _, unexpected := range []string{"old user message", "assistant message", "system instructions"} {
		if strings.Contains(text, unexpected) {
			t.Fatalf("audit text leaked %q: %q", unexpected, text)
		}
	}
}

func TestSanitizeAuditValueOmitsMediaData(t *testing.T) {
	value := map[string]any{
		"content": []any{
			map[string]any{
				"type":      "input_image",
				"image_url": "data:image/png;base64," + strings.Repeat("a", 4096),
			},
			map[string]any{
				"type": "input_text",
				"text": "keep this text",
			},
		},
	}

	sanitized := sanitizeAuditValue(value).(map[string]any)
	bytes, err := buildAuditJSONForTest(sanitized)
	if err != nil {
		t.Fatal(err)
	}
	output := string(bytes)

	if strings.Contains(output, "data:image/png") {
		t.Fatalf("media data was not omitted: %s", output)
	}
	if !strings.Contains(output, omittedAuditValue) {
		t.Fatalf("omitted marker missing: %s", output)
	}
	if !strings.Contains(output, "keep this text") {
		t.Fatalf("text content missing: %s", output)
	}
}

func buildAuditJSONForTest(value any) ([]byte, error) {
	return common.Marshal(value)
}
