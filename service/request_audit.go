package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"

	"github.com/bytedance/gopkg/util/gopool"
	"github.com/gin-gonic/gin"
)

const (
	requestContentAuditEnabledEnv       = "REQUEST_CONTENT_AUDIT_ENABLED"
	requestContentAuditMaxBytesEnv      = "REQUEST_CONTENT_AUDIT_MAX_BYTES"
	requestContentAuditIncludeSystemEnv = "REQUEST_CONTENT_AUDIT_INCLUDE_SYSTEM"
	defaultRequestContentAuditMaxBytes  = 65535
	omittedAuditValue                   = "[omitted]"
)

func RecordRequestContentAuditAsync(c *gin.Context, relayInfo *relaycommon.RelayInfo, request dto.Request, channelId int) {
	if !requestContentAuditEnabled() || c == nil || relayInfo == nil || request == nil {
		return
	}

	entry, err := buildRequestContentLog(c, relayInfo, request, channelId)
	if err != nil {
		logger.LogError(c, "failed to build request content audit log: "+err.Error())
		return
	}
	if entry == nil {
		return
	}

	gopool.Go(func() {
		if err := model.RecordRequestContentLog(entry); err != nil {
			logger.LogError(c, "failed to record request content audit log: "+err.Error())
		}
	})
}

func requestContentAuditEnabled() bool {
	return common.GetEnvOrDefaultBool(requestContentAuditEnabledEnv, false)
}

func buildRequestContentLog(c *gin.Context, relayInfo *relaycommon.RelayInfo, request dto.Request, channelId int) (*model.RequestContentLog, error) {
	content := buildAuditContent(relayInfo, request)
	if len(content) == 0 {
		return nil, nil
	}

	sanitized := sanitizeAuditValue(content)
	contentText := strings.Join(collectAuditText("", sanitized), "\n")
	if strings.TrimSpace(contentText) == "" {
		contentText = strings.Join(collectAuditText("", content), "\n")
	}

	contentBytes, err := common.Marshal(sanitized)
	if err != nil {
		return nil, err
	}

	maxBytes := common.GetEnvOrDefault(requestContentAuditMaxBytesEnv, defaultRequestContentAuditMaxBytes)
	if maxBytes <= 0 {
		maxBytes = defaultRequestContentAuditMaxBytes
	}

	truncated := false
	if len(contentBytes) > maxBytes {
		truncated = true
		contentText = truncateAuditString(contentText, maxBytes)
		contentBytes, err = common.Marshal(map[string]any{
			"truncated":    true,
			"content_text": contentText,
		})
		if err != nil {
			return nil, err
		}
	}
	contentText = truncateAuditString(contentText, maxBytes)

	sum := sha256.Sum256(contentBytes)
	username := c.GetString(string(constant.ContextKeyUserName))
	tokenName := c.GetString("token_name")
	if channelId == 0 {
		channelId = relayInfo.ChannelId
	}

	return &model.RequestContentLog{
		RequestId:   c.GetString(common.RequestIdKey),
		UserId:      relayInfo.UserId,
		Username:    username,
		TokenId:     relayInfo.TokenId,
		TokenName:   tokenName,
		ChannelId:   channelId,
		ModelName:   relayInfo.OriginModelName,
		Group:       relayInfo.UsingGroup,
		RelayMode:   relayInfo.RelayMode,
		Path:        c.Request.URL.Path,
		Content:     string(contentBytes),
		ContentText: contentText,
		ContentHash: hex.EncodeToString(sum[:]),
		Truncated:   truncated,
		CreatedAt:   common.GetTimestamp(),
	}, nil
}

func buildAuditContent(relayInfo *relaycommon.RelayInfo, request dto.Request) map[string]any {
	content := map[string]any{
		"model":      relayInfo.OriginModelName,
		"relay_mode": relayInfo.RelayMode,
	}
	includeSystem := common.GetEnvOrDefaultBool(requestContentAuditIncludeSystemEnv, true)

	switch req := request.(type) {
	case *dto.GeneralOpenAIRequest:
		addIfNotEmpty(content, "messages", req.Messages)
		addIfNotEmpty(content, "prompt", req.Prompt)
		addIfNotEmpty(content, "input", req.Input)
		addIfNotEmpty(content, "instruction", req.Instruction)
		addIfNotEmpty(content, "prefix", req.Prefix)
		addIfNotEmpty(content, "suffix", req.Suffix)
	case *dto.OpenAIResponsesRequest:
		if includeSystem {
			addRawIfNotEmpty(content, "instructions", req.Instructions)
		}
		addRawIfNotEmpty(content, "input", req.Input)
		addRawIfNotEmpty(content, "prompt", req.Prompt)
	case *dto.ClaudeRequest:
		if includeSystem {
			addIfNotEmpty(content, "system", req.System)
		}
		addIfNotEmpty(content, "prompt", req.Prompt)
		addIfNotEmpty(content, "messages", req.Messages)
	case *dto.GeminiChatRequest:
		if includeSystem {
			addIfNotEmpty(content, "system_instruction", req.SystemInstructions)
		}
		addIfNotEmpty(content, "contents", req.Contents)
		addIfNotEmpty(content, "requests", req.Requests)
	case *dto.ImageRequest:
		addIfNotEmpty(content, "prompt", req.Prompt)
	case *dto.EmbeddingRequest:
		addIfNotEmpty(content, "input", req.Input)
	case *dto.RerankRequest:
		addIfNotEmpty(content, "query", req.Query)
		addIfNotEmpty(content, "documents", req.Documents)
	default:
		addIfNotEmpty(content, "request", request)
	}

	if len(content) <= 2 {
		return nil
	}
	return content
}

func addIfNotEmpty(dst map[string]any, key string, value any) {
	if value == nil {
		return
	}
	switch v := value.(type) {
	case string:
		if strings.TrimSpace(v) == "" {
			return
		}
	case []dto.Message:
		if len(v) == 0 {
			return
		}
	case []dto.ClaudeMessage:
		if len(v) == 0 {
			return
		}
	case []dto.GeminiChatContent:
		if len(v) == 0 {
			return
		}
	case []dto.GeminiChatRequest:
		if len(v) == 0 {
			return
		}
	case []string:
		if len(v) == 0 {
			return
		}
	}
	dst[key] = value
}

func addRawIfNotEmpty(dst map[string]any, key string, value json.RawMessage) {
	if len(value) == 0 || string(value) == "null" {
		return
	}
	var decoded any
	if err := common.Unmarshal(value, &decoded); err == nil {
		dst[key] = decoded
		return
	}
	dst[key] = string(value)
}

func sanitizeAuditValue(value any) any {
	switch v := value.(type) {
	case nil:
		return nil
	case string:
		if shouldOmitString(v) {
			return omittedAuditValue
		}
		return v
	case bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
		return v
	case json.RawMessage:
		var decoded any
		if err := common.Unmarshal(v, &decoded); err != nil {
			return omittedAuditValue
		}
		return sanitizeAuditValue(decoded)
	case []any:
		out := make([]any, 0, len(v))
		for _, item := range v {
			out = append(out, sanitizeAuditValue(item))
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, item := range v {
			if shouldOmitAuditKey(key) {
				out[key] = omittedAuditValue
				continue
			}
			out[key] = sanitizeAuditValue(item)
		}
		return out
	default:
		bytes, err := common.Marshal(v)
		if err != nil {
			return omittedAuditValue
		}
		var decoded any
		if err = common.Unmarshal(bytes, &decoded); err != nil {
			return omittedAuditValue
		}
		return sanitizeAuditValue(decoded)
	}
}

func shouldOmitAuditKey(key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	key = strings.ReplaceAll(key, "-", "_")
	switch key {
	case "authorization", "api_key", "x_api_key", "x_goog_api_key", "mj_api_secret",
		"file_data", "b64_json", "image_url", "input_audio", "inline_data",
		"inlinedata", "data", "filedata", "fileuri", "inline_data_data",
		"file", "video_url", "audio", "image":
		return true
	default:
		return false
	}
}

func shouldOmitString(value string) bool {
	trimmed := strings.TrimSpace(value)
	if strings.HasPrefix(trimmed, "data:image/") ||
		strings.HasPrefix(trimmed, "data:audio/") ||
		strings.HasPrefix(trimmed, "data:video/") ||
		strings.HasPrefix(trimmed, "data:application/") {
		return true
	}
	if len(trimmed) > 4096 && looksLikeBase64(trimmed) {
		return true
	}
	return false
}

func looksLikeBase64(value string) bool {
	if len(value)%4 != 0 {
		return false
	}
	checkLen := len(value)
	if checkLen > 512 {
		checkLen = 512
	}
	for i := 0; i < checkLen; i++ {
		ch := value[i]
		if (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '+' || ch == '/' || ch == '=' || ch == '-' || ch == '_' {
			continue
		}
		return false
	}
	return true
}

func collectAuditText(key string, value any) []string {
	switch v := value.(type) {
	case nil:
		return nil
	case string:
		if shouldCollectAuditTextKey(key) && v != omittedAuditValue && strings.TrimSpace(v) != "" {
			return []string{v}
		}
		return nil
	case bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
		return nil
	case []any:
		texts := make([]string, 0)
		for _, item := range v {
			texts = append(texts, collectAuditText(key, item)...)
		}
		return texts
	case map[string]any:
		texts := make([]string, 0)
		for childKey, item := range v {
			texts = append(texts, collectAuditText(childKey, item)...)
		}
		return texts
	default:
		bytes, err := common.Marshal(v)
		if err != nil {
			return nil
		}
		var decoded any
		if err = common.Unmarshal(bytes, &decoded); err != nil {
			return nil
		}
		return collectAuditText(key, decoded)
	}
}

func shouldCollectAuditTextKey(key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	switch key {
	case "text", "content", "prompt", "input", "instructions", "instruction", "system", "query", "documents", "prefix", "suffix":
		return true
	default:
		return false
	}
}

func truncateAuditString(value string, maxBytes int) string {
	if maxBytes <= 0 || len(value) <= maxBytes {
		return value
	}
	var builder strings.Builder
	for _, r := range value {
		if builder.Len()+len(string(r)) > maxBytes {
			break
		}
		builder.WriteRune(r)
	}
	return builder.String()
}
