package service

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
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

func TestBuildRequestContentLogDoesNotCaptureHeadersByDefault(t *testing.T) {
	log := buildHeaderAuditLogForTest(t)

	if log.RequestHeaders != "" {
		t.Fatalf("request headers should be empty by default, got %q", log.RequestHeaders)
	}
}

func TestBuildRequestContentLogCapturesSanitizedHeadersWhenEnabled(t *testing.T) {
	t.Setenv(requestContentAuditIncludeHeadersEnv, "true")
	log := buildHeaderAuditLogForTest(t)

	if log.RequestHeaders == "" {
		t.Fatal("request headers should be captured")
	}
	if strings.Contains(log.RequestHeaders, "secret-token") ||
		strings.Contains(log.RequestHeaders, "session=secret") ||
		strings.Contains(log.RequestHeaders, "upstream-secret") {
		t.Fatalf("request headers leaked sensitive value: %s", log.RequestHeaders)
	}

	var headers map[string][]string
	if err := common.UnmarshalJsonStr(log.RequestHeaders, &headers); err != nil {
		t.Fatal(err)
	}
	assertHeaderValueForTest(t, headers, "X-Codex-Beta-Features", "fast-mode")
	assertHeaderValueForTest(t, headers, "Openai-Beta", "responses=v1")
	assertHeaderValueForTest(t, headers, "User-Agent", "Codex CLI")
	assertHeaderValueForTest(t, headers, "Authorization", omittedAuditValue)
	assertHeaderValueForTest(t, headers, "Cookie", omittedAuditValue)
	assertHeaderValueForTest(t, headers, "X-Trace-Auth", omittedAuditValue)
}

func buildAuditJSONForTest(value any) ([]byte, error) {
	return common.Marshal(value)
}

func buildHeaderAuditLogForTest(t *testing.T) *model.RequestContentLog {
	t.Helper()
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest("POST", "/v1/responses", nil)
	req.Host = "new-api.example.test"
	req.Header.Set("Authorization", "Bearer secret-token")
	req.Header.Set("Cookie", "session=secret")
	req.Header.Set("X-Trace-Auth", "Bearer upstream-secret")
	req.Header.Set("X-Codex-Beta-Features", "fast-mode")
	req.Header.Set("OpenAI-Beta", "responses=v1")
	req.Header.Set("User-Agent", "Codex CLI")
	c.Request = req
	c.Set(common.RequestIdKey, "req-test")

	info := &relaycommon.RelayInfo{
		UserId:          7,
		TokenId:         11,
		OriginModelName: "gpt-test",
		UsingGroup:      "default",
		RelayMode:       37,
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelId: 13,
		},
	}
	request := &dto.OpenAIResponsesRequest{
		Model: "gpt-test",
		Input: []byte(`"hello"`),
	}
	log, err := buildRequestContentLog(c, info, request, 0)
	if err != nil {
		t.Fatal(err)
	}
	if log == nil {
		t.Fatal("request content log is nil")
	}
	return log
}

func assertHeaderValueForTest(t *testing.T, headers map[string][]string, key string, want string) {
	t.Helper()
	values, ok := headers[key]
	if !ok || len(values) == 0 {
		t.Fatalf("header %s missing in %#v", key, headers)
	}
	if values[0] != want {
		t.Fatalf("header %s = %q, want %q", key, values[0], want)
	}
}
