package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"

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
	requestContentAuditEnabledEnv             = "REQUEST_CONTENT_AUDIT_ENABLED"
	requestContentAuditMaxBytesEnv            = "REQUEST_CONTENT_AUDIT_MAX_BYTES"
	requestContentAuditIncludeSystemEnv       = "REQUEST_CONTENT_AUDIT_INCLUDE_SYSTEM"
	requestContentAuditIncludeHeadersEnv      = "REQUEST_CONTENT_AUDIT_INCLUDE_HEADERS"
	requestContentAuditMaxHeaderValueBytesEnv = "REQUEST_CONTENT_AUDIT_MAX_HEADER_VALUE_BYTES"
	requestContentAuditScopeEnv               = "REQUEST_CONTENT_AUDIT_SCOPE"
	requestContentAuditDedupeEnabledEnv       = "REQUEST_CONTENT_AUDIT_DEDUPE_ENABLED"
	requestContentAuditDedupeWindowEnv        = "REQUEST_CONTENT_AUDIT_DEDUPE_WINDOW_SECONDS"
	requestContentAuditMaxStorageEnv          = "REQUEST_CONTENT_AUDIT_MAX_STORAGE_BYTES"
	requestContentAuditCleanupRatioEnv        = "REQUEST_CONTENT_AUDIT_CLEANUP_TARGET_RATIO"
	requestContentAuditCleanupBatchEnv        = "REQUEST_CONTENT_AUDIT_CLEANUP_BATCH_SIZE"

	auditScopeCurrentUser = "current_user"
	auditScopeAllUser     = "all_user"
	auditScopeFull        = "full"

	defaultRequestContentAuditMaxBytes            = 65535
	defaultRequestContentAuditMaxHeaderValueBytes = 4096
	defaultRequestContentAuditScope               = auditScopeCurrentUser
	defaultRequestContentDedupeWindow             = 300
	defaultRequestContentMaxStorage               = int64(20 * 1024 * 1024 * 1024)
	defaultRequestContentCleanupRatio             = 0.9
	defaultRequestContentCleanupBatch             = 1000
	omittedAuditValue                             = "[omitted]"
)

var requestContentAuditCleanupRunning atomic.Bool

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
		var err error
		if requestContentAuditDedupeEnabled() {
			err = model.RecordRequestContentLogWithDedupe(entry, requestContentAuditDedupeWindowSeconds())
		} else {
			err = model.RecordRequestContentLog(entry)
		}
		if err != nil {
			logger.LogError(c, "failed to record request content audit log: "+err.Error())
			return
		}
		enforceRequestContentAuditStorageLimit()
	})
}

func requestContentAuditEnabled() bool {
	return common.GetEnvOrDefaultBool(requestContentAuditEnabledEnv, false)
}

func requestContentAuditIncludeSystem() bool {
	return common.GetEnvOrDefaultBool(requestContentAuditIncludeSystemEnv, false)
}

func requestContentAuditIncludeHeaders() bool {
	return common.GetEnvOrDefaultBool(requestContentAuditIncludeHeadersEnv, false)
}

func requestContentAuditMaxHeaderValueBytes() int {
	maxBytes := common.GetEnvOrDefault(requestContentAuditMaxHeaderValueBytesEnv, defaultRequestContentAuditMaxHeaderValueBytes)
	if maxBytes <= 0 {
		return defaultRequestContentAuditMaxHeaderValueBytes
	}
	return maxBytes
}

func requestContentAuditScope() string {
	scope := strings.ToLower(strings.TrimSpace(common.GetEnvOrDefaultString(requestContentAuditScopeEnv, defaultRequestContentAuditScope)))
	switch scope {
	case auditScopeCurrentUser, "current", "latest":
		return auditScopeCurrentUser
	case auditScopeAllUser, "all_user_messages", "all":
		return auditScopeAllUser
	case auditScopeFull:
		return auditScopeFull
	default:
		return defaultRequestContentAuditScope
	}
}

func requestContentAuditDedupeEnabled() bool {
	return common.GetEnvOrDefaultBool(requestContentAuditDedupeEnabledEnv, true)
}

func requestContentAuditDedupeWindowSeconds() int64 {
	window := common.GetEnvOrDefault(requestContentAuditDedupeWindowEnv, defaultRequestContentDedupeWindow)
	if window <= 0 {
		return int64(defaultRequestContentDedupeWindow)
	}
	return int64(window)
}

func requestContentAuditMaxStorageBytes() int64 {
	raw := strings.TrimSpace(common.GetEnvOrDefaultString(requestContentAuditMaxStorageEnv, strconv.FormatInt(defaultRequestContentMaxStorage, 10)))
	if raw == "" {
		return defaultRequestContentMaxStorage
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		common.SysError("failed to parse " + requestContentAuditMaxStorageEnv + ": " + err.Error())
		return defaultRequestContentMaxStorage
	}
	return value
}

func requestContentAuditCleanupTargetRatio() float64 {
	raw := strings.TrimSpace(common.GetEnvOrDefaultString(requestContentAuditCleanupRatioEnv, strconv.FormatFloat(defaultRequestContentCleanupRatio, 'f', -1, 64)))
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		common.SysError("failed to parse " + requestContentAuditCleanupRatioEnv + ": " + err.Error())
		return defaultRequestContentCleanupRatio
	}
	if value <= 0 || value >= 1 {
		return defaultRequestContentCleanupRatio
	}
	return value
}

func requestContentAuditCleanupBatchSize() int {
	batchSize := common.GetEnvOrDefault(requestContentAuditCleanupBatchEnv, defaultRequestContentCleanupBatch)
	if batchSize <= 0 {
		return defaultRequestContentCleanupBatch
	}
	return batchSize
}

func enforceRequestContentAuditStorageLimit() {
	maxBytes := requestContentAuditMaxStorageBytes()
	if maxBytes <= 0 {
		return
	}
	if !requestContentAuditCleanupRunning.CompareAndSwap(false, true) {
		return
	}
	defer requestContentAuditCleanupRunning.Store(false)

	targetBytes := int64(float64(maxBytes) * requestContentAuditCleanupTargetRatio())
	deleted, totalBytes, err := model.EnforceRequestContentLogStorageLimit(context.Background(), maxBytes, targetBytes, requestContentAuditCleanupBatchSize())
	if err != nil {
		logger.LogError(context.Background(), "failed to enforce request content audit storage limit: "+err.Error())
		return
	}
	if deleted > 0 {
		logger.LogInfo(context.Background(), "request content audit storage cleanup removed "+strconv.FormatInt(deleted, 10)+" rows, estimated remaining bytes "+strconv.FormatInt(totalBytes, 10))
	}
}

func buildRequestContentLog(c *gin.Context, relayInfo *relaycommon.RelayInfo, request dto.Request, channelId int) (*model.RequestContentLog, error) {
	content := buildAuditContent(relayInfo, request)
	if len(content) == 0 {
		return nil, nil
	}

	sanitized := sanitizeAuditValue(content)
	contentText := strings.Join(collectAuditText("", sanitized), "\n")

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

	requestHeaders, err := buildAuditRequestHeaders(c)
	if err != nil {
		return nil, err
	}

	sum := sha256.Sum256(contentBytes)
	username := c.GetString(string(constant.ContextKeyUserName))
	tokenName := c.GetString("token_name")
	if channelId == 0 {
		channelId = relayInfo.ChannelId
	}
	now := common.GetTimestamp()
	requestId := c.GetString(common.RequestIdKey)

	return &model.RequestContentLog{
		RequestId:      requestId,
		UserId:         relayInfo.UserId,
		Username:       username,
		TokenId:        relayInfo.TokenId,
		TokenName:      tokenName,
		ChannelId:      channelId,
		ModelName:      relayInfo.OriginModelName,
		Group:          relayInfo.UsingGroup,
		RelayMode:      relayInfo.RelayMode,
		Path:           c.Request.URL.Path,
		Content:        string(contentBytes),
		ContentText:    contentText,
		RequestHeaders: requestHeaders,
		ContentHash:    hex.EncodeToString(sum[:]),
		DuplicateCount: 1,
		LastRequestId:  requestId,
		LastSeenAt:     now,
		StorageBytes:   model.EstimateRequestContentLogStorageBytes(string(contentBytes), contentText, requestHeaders),
		Truncated:      truncated,
		CreatedAt:      now,
	}, nil
}

func buildAuditRequestHeaders(c *gin.Context) (string, error) {
	if !requestContentAuditIncludeHeaders() || c == nil || c.Request == nil {
		return "", nil
	}

	headers := make(map[string][]string, len(c.Request.Header)+1)
	if host := strings.TrimSpace(c.Request.Host); host != "" {
		headers["Host"] = []string{truncateAuditString(host, requestContentAuditMaxHeaderValueBytes())}
	}
	for key, values := range c.Request.Header {
		name := http.CanonicalHeaderKey(strings.TrimSpace(key))
		if name == "" {
			continue
		}
		sanitized := sanitizeAuditHeaderValues(name, values)
		if len(sanitized) == 0 {
			continue
		}
		headers[name] = sanitized
	}
	if len(headers) == 0 {
		return "", nil
	}

	data, err := common.Marshal(headers)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func sanitizeAuditHeaderValues(name string, values []string) []string {
	if shouldOmitAuditHeaderKey(name) {
		return []string{omittedAuditValue}
	}
	maxBytes := requestContentAuditMaxHeaderValueBytes()
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		switch {
		case shouldOmitAuditHeaderValue(value), shouldOmitString(value):
			out = append(out, omittedAuditValue)
		default:
			out = append(out, truncateAuditString(value, maxBytes))
		}
	}
	return out
}

func shouldOmitAuditHeaderKey(key string) bool {
	key = normalizeAuditHeaderKey(key)
	switch key {
	case "authorization", "proxy_authorization", "cookie", "set_cookie",
		"api_key", "x_api_key", "x_openai_api_key", "openai_api_key",
		"anthropic_api_key", "x_goog_api_key", "google_api_key",
		"x_auth_token", "x_csrf_token", "x_csrftoken",
		"cf_access_client_secret":
		return true
	default:
		return false
	}
}

func shouldOmitAuditHeaderValue(value string) bool {
	trimmed := strings.TrimSpace(value)
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "bearer ") ||
		strings.HasPrefix(lower, "basic ") ||
		strings.HasPrefix(lower, "digest ") ||
		strings.HasPrefix(lower, "token ") ||
		strings.HasPrefix(lower, "apikey ") ||
		strings.HasPrefix(lower, "api-key ") {
		return true
	}
	return strings.HasPrefix(trimmed, "sk-") && len(trimmed) > 20
}

func normalizeAuditHeaderKey(key string) string {
	key = strings.ToLower(strings.TrimSpace(key))
	return strings.ReplaceAll(key, "-", "_")
}

func buildAuditContent(relayInfo *relaycommon.RelayInfo, request dto.Request) map[string]any {
	content := map[string]any{
		"model":      relayInfo.OriginModelName,
		"relay_mode": relayInfo.RelayMode,
	}
	includeSystem := requestContentAuditIncludeSystem()
	scope := requestContentAuditScope()

	if scope == auditScopeFull {
		return buildFullAuditContent(content, request, includeSystem)
	}

	switch req := request.(type) {
	case *dto.GeneralOpenAIRequest:
		addIfNotEmpty(content, "messages", filterOpenAIMessages(req.Messages, scope, includeSystem))
		addIfNotEmpty(content, "prompt", req.Prompt)
		addIfNotEmpty(content, "input", filterGenericAuditInput(req.Input, scope, includeSystem))
		if includeSystem {
			addIfNotEmpty(content, "instruction", req.Instruction)
		}
		addIfNotEmpty(content, "prefix", req.Prefix)
		addIfNotEmpty(content, "suffix", req.Suffix)
	case *dto.OpenAIResponsesRequest:
		if includeSystem {
			addRawIfNotEmpty(content, "instructions", req.Instructions)
		}
		addIfNotEmpty(content, "input", filterOpenAIResponsesInput(req.Input, scope, includeSystem))
	case *dto.ClaudeRequest:
		if includeSystem {
			addIfNotEmpty(content, "system", req.System)
		}
		addIfNotEmpty(content, "prompt", req.Prompt)
		addIfNotEmpty(content, "messages", filterClaudeMessages(req.Messages, scope))
	case *dto.GeminiChatRequest:
		if includeSystem {
			addIfNotEmpty(content, "system_instruction", req.SystemInstructions)
		}
		addIfNotEmpty(content, "contents", filterGeminiContents(req.Contents, scope))
		addIfNotEmpty(content, "requests", filterGeminiRequests(req.Requests, scope, includeSystem))
	case *dto.ImageRequest:
		addIfNotEmpty(content, "prompt", req.Prompt)
	case *dto.EmbeddingRequest:
		addIfNotEmpty(content, "input", req.Input)
	case *dto.RerankRequest:
		addIfNotEmpty(content, "query", req.Query)
		addIfNotEmpty(content, "documents", req.Documents)
	}

	if len(content) <= 2 {
		return nil
	}
	return content
}

func buildFullAuditContent(content map[string]any, request dto.Request, includeSystem bool) map[string]any {
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
	case []any:
		if len(v) == 0 {
			return
		}
	case []map[string]any:
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

func filterOpenAIMessages(messages []dto.Message, scope string, includeSystem bool) []dto.Message {
	if len(messages) == 0 {
		return nil
	}
	filtered := make([]dto.Message, 0, len(messages))
	var latestUser *dto.Message
	for _, message := range messages {
		role := normalizeAuditRole(message.Role)
		switch {
		case role == "user":
			message.Content = filterOpenAIUserContent(message.Content)
			if isAuditContentEmpty(message.Content) {
				continue
			}
			if scope == auditScopeCurrentUser {
				latest := message
				latestUser = &latest
			} else {
				filtered = append(filtered, message)
			}
		case includeSystem && isAuditSystemRole(role):
			filtered = append(filtered, message)
		}
	}
	if scope == auditScopeCurrentUser && latestUser != nil {
		filtered = append(filtered, *latestUser)
	}
	return filtered
}

func filterClaudeMessages(messages []dto.ClaudeMessage, scope string) []dto.ClaudeMessage {
	if len(messages) == 0 {
		return nil
	}
	filtered := make([]dto.ClaudeMessage, 0, len(messages))
	var latestUser *dto.ClaudeMessage
	for _, message := range messages {
		if normalizeAuditRole(message.Role) != "user" {
			continue
		}
		message.Content = filterClaudeUserContent(message.Content)
		if isAuditContentEmpty(message.Content) {
			continue
		}
		if scope == auditScopeCurrentUser {
			latest := message
			latestUser = &latest
		} else {
			filtered = append(filtered, message)
		}
	}
	if scope == auditScopeCurrentUser && latestUser != nil {
		filtered = append(filtered, *latestUser)
	}
	return filtered
}

func filterGeminiContents(contents []dto.GeminiChatContent, scope string) []dto.GeminiChatContent {
	if len(contents) == 0 {
		return nil
	}
	filtered := make([]dto.GeminiChatContent, 0, len(contents))
	var latestUser *dto.GeminiChatContent
	for _, content := range contents {
		role := normalizeAuditRole(content.Role)
		if role != "" && role != "user" {
			continue
		}
		content.Parts = filterGeminiUserParts(content.Parts)
		if len(content.Parts) == 0 {
			continue
		}
		if scope == auditScopeCurrentUser {
			latest := content
			latestUser = &latest
		} else {
			filtered = append(filtered, content)
		}
	}
	if scope == auditScopeCurrentUser && latestUser != nil {
		filtered = append(filtered, *latestUser)
	}
	return filtered
}

func filterGeminiRequests(requests []dto.GeminiChatRequest, scope string, includeSystem bool) []map[string]any {
	if len(requests) == 0 {
		return nil
	}
	filtered := make([]map[string]any, 0, len(requests))
	for _, request := range requests {
		item := make(map[string]any)
		if includeSystem && request.SystemInstructions != nil {
			item["system_instruction"] = request.SystemInstructions
		}
		if contents := filterGeminiContents(request.Contents, scope); len(contents) > 0 {
			item["contents"] = contents
		}
		if nested := filterGeminiRequests(request.Requests, scope, includeSystem); len(nested) > 0 {
			item["requests"] = nested
		}
		if len(item) > 0 {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func filterOpenAIResponsesInput(value json.RawMessage, scope string, includeSystem bool) any {
	if len(value) == 0 || string(value) == "null" {
		return nil
	}
	var decoded any
	if err := common.Unmarshal(value, &decoded); err != nil {
		return nil
	}
	return filterOpenAIResponsesDecodedInput(decoded, scope, includeSystem)
}

func filterGenericAuditInput(value any, scope string, includeSystem bool) any {
	switch v := value.(type) {
	case []any:
		if hasAuditRoleItems(v) {
			return filterOpenAIResponsesDecodedInput(v, scope, includeSystem)
		}
		return value
	case map[string]any:
		if isOpenAIResponsesUserItem(v) || isOpenAIResponsesSystemItem(v) {
			return filterOpenAIResponsesDecodedInput(v, scope, includeSystem)
		}
		return value
	default:
		return value
	}
}

func filterOpenAIResponsesDecodedInput(value any, scope string, includeSystem bool) any {
	switch v := value.(type) {
	case string:
		if strings.TrimSpace(v) == "" {
			return nil
		}
		return v
	case []any:
		if len(v) == 0 {
			return nil
		}
		if !hasAuditRoleItems(v) {
			return v
		}
		filtered := make([]any, 0, len(v))
		systemItems := make([]any, 0)
		var latestUser any
		for _, item := range v {
			switch {
			case isOpenAIResponsesUserItem(item):
				item = filterOpenAIResponsesUserItem(item)
				if isAuditContentEmpty(item) {
					continue
				}
				if scope == auditScopeCurrentUser {
					latestUser = item
				} else {
					filtered = append(filtered, item)
				}
			case includeSystem && isOpenAIResponsesSystemItem(item):
				if scope == auditScopeCurrentUser {
					systemItems = append(systemItems, item)
				} else {
					filtered = append(filtered, item)
				}
			}
		}
		if scope == auditScopeCurrentUser {
			filtered = append(filtered, systemItems...)
			if latestUser != nil {
				filtered = append(filtered, latestUser)
			}
		}
		if len(filtered) == 0 {
			return nil
		}
		return filtered
	case map[string]any:
		if isOpenAIResponsesUserItem(v) {
			return filterOpenAIResponsesUserItem(v)
		}
		if includeSystem && isOpenAIResponsesSystemItem(v) {
			return v
		}
		return nil
	default:
		return value
	}
}

func filterOpenAIResponsesUserItem(item any) any {
	itemMap, ok := item.(map[string]any)
	if !ok {
		return item
	}
	out := copyAuditMap(itemMap)
	if content, ok := out["content"]; ok {
		out["content"] = filterOpenAIUserContent(content)
	}
	return out
}

func filterOpenAIUserContent(content any) any {
	switch v := content.(type) {
	case []any:
		filtered := make([]any, 0, len(v))
		for _, item := range v {
			if shouldKeepOpenAIUserContentPart(item) {
				filtered = append(filtered, item)
			}
		}
		return filtered
	default:
		return content
	}
}

func filterClaudeUserContent(content any) any {
	switch v := content.(type) {
	case []any:
		filtered := make([]any, 0, len(v))
		for _, item := range v {
			itemMap, ok := item.(map[string]any)
			if !ok {
				if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
					filtered = append(filtered, item)
				}
				continue
			}
			switch getAuditMapString(itemMap, "type") {
			case "", "text", "image", "document":
				filtered = append(filtered, item)
			}
		}
		return filtered
	default:
		return content
	}
}

func filterGeminiUserParts(parts []dto.GeminiPart) []dto.GeminiPart {
	if len(parts) == 0 {
		return nil
	}
	filtered := make([]dto.GeminiPart, 0, len(parts))
	for _, part := range parts {
		if part.Thought {
			continue
		}
		kept := dto.GeminiPart{
			MediaResolution: part.MediaResolution,
			VideoMetadata:   part.VideoMetadata,
		}
		if part.Text != "" {
			kept.Text = part.Text
		}
		if part.InlineData != nil {
			kept.InlineData = part.InlineData
		}
		if part.FileData != nil {
			kept.FileData = part.FileData
		}
		if kept.Text != "" || kept.InlineData != nil || kept.FileData != nil {
			filtered = append(filtered, kept)
		}
	}
	return filtered
}

func shouldKeepOpenAIUserContentPart(item any) bool {
	itemMap, ok := item.(map[string]any)
	if !ok {
		if text, ok := item.(string); ok {
			return strings.TrimSpace(text) != ""
		}
		return false
	}
	partType := getAuditMapString(itemMap, "type")
	switch partType {
	case "", "text", "input_text", "image_url", "input_image", "input_audio", "file", "input_file", "video_url", "audio", "image", "document":
		return true
	default:
		return false
	}
}

func hasAuditRoleItems(items []any) bool {
	for _, item := range items {
		if _, ok := item.(string); ok {
			continue
		}
		itemMap, ok := item.(map[string]any)
		if !ok {
			continue
		}
		role := getAuditMapString(itemMap, "role")
		itemType := getAuditMapString(itemMap, "type")
		if role != "" || itemType == "message" || strings.HasPrefix(itemType, "function_") || itemType == "reasoning" || strings.HasPrefix(itemType, "input_") {
			return true
		}
	}
	return false
}

func isOpenAIResponsesUserItem(item any) bool {
	if text, ok := item.(string); ok {
		return strings.TrimSpace(text) != ""
	}
	itemMap, ok := item.(map[string]any)
	if !ok {
		return false
	}
	role := getAuditMapString(itemMap, "role")
	itemType := getAuditMapString(itemMap, "type")
	return role == "user" || (role == "" && (itemType == "input_text" || itemType == "input_image" || itemType == "input_file"))
}

func isOpenAIResponsesSystemItem(item any) bool {
	itemMap, ok := item.(map[string]any)
	if !ok {
		return false
	}
	return isAuditSystemRole(getAuditMapString(itemMap, "role"))
}

func isAuditSystemRole(role string) bool {
	return role == "system" || role == "developer"
}

func normalizeAuditRole(role string) string {
	return strings.ToLower(strings.TrimSpace(role))
}

func getAuditMapString(item map[string]any, key string) string {
	value, ok := item[key]
	if !ok {
		return ""
	}
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(text))
}

func copyAuditMap(src map[string]any) map[string]any {
	out := make(map[string]any, len(src))
	for key, value := range src {
		out[key] = value
	}
	return out
}

func isAuditContentEmpty(value any) bool {
	switch v := value.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(v) == ""
	case []any:
		return len(v) == 0
	case []dto.Message:
		return len(v) == 0
	case []dto.ClaudeMessage:
		return len(v) == 0
	case []dto.GeminiChatContent:
		return len(v) == 0
	case map[string]any:
		if content, ok := v["content"]; ok {
			return isAuditContentEmpty(content)
		}
		if text, ok := v["text"]; ok {
			return isAuditContentEmpty(text)
		}
		return len(v) == 0
	default:
		return false
	}
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
