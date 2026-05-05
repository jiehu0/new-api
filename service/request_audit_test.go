package service

import (
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
)

func TestBuildAuditContentResponses(t *testing.T) {
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
