package model

import (
	"context"
	"errors"
	"strings"

	"gorm.io/gorm"
)

type RequestContentLog struct {
	Id             int    `json:"id" gorm:"index:idx_request_content_created_id,priority:1;index:idx_request_content_user_id,priority:2"`
	RequestId      string `json:"request_id" gorm:"type:varchar(64);index:idx_request_content_request_id;default:''"`
	UserId         int    `json:"user_id" gorm:"index;index:idx_request_content_user_id,priority:1"`
	Username       string `json:"username" gorm:"index;default:''"`
	TokenId        int    `json:"token_id" gorm:"index;default:0"`
	TokenName      string `json:"token_name" gorm:"index;default:''"`
	ChannelId      int    `json:"channel_id" gorm:"index;default:0"`
	ModelName      string `json:"model_name" gorm:"index;default:''"`
	Group          string `json:"group" gorm:"index;default:''"`
	RelayMode      int    `json:"relay_mode" gorm:"index;default:0"`
	Path           string `json:"path" gorm:"type:varchar(255);default:''"`
	Content        string `json:"content" gorm:"type:text"`
	ContentText    string `json:"content_text" gorm:"type:text"`
	ContentHash    string `json:"content_hash" gorm:"type:varchar(64);index;default:''"`
	DuplicateCount int    `json:"duplicate_count" gorm:"default:1"`
	LastRequestId  string `json:"last_request_id" gorm:"type:varchar(64);index;default:''"`
	LastSeenAt     int64  `json:"last_seen_at" gorm:"bigint;index;default:0"`
	StorageBytes   int64  `json:"storage_bytes" gorm:"bigint;index;default:0"`
	Truncated      bool   `json:"truncated"`
	CreatedAt      int64  `json:"created_at" gorm:"bigint;index:idx_request_content_created_id,priority:2"`
}

type RequestContentLogListItem struct {
	Id             int    `json:"id"`
	RequestId      string `json:"request_id"`
	UserId         int    `json:"user_id"`
	Username       string `json:"username"`
	TokenId        int    `json:"token_id"`
	TokenName      string `json:"token_name"`
	ChannelId      int    `json:"channel_id"`
	ModelName      string `json:"model_name"`
	Group          string `json:"group"`
	RelayMode      int    `json:"relay_mode"`
	Path           string `json:"path"`
	ContentText    string `json:"content_text"`
	ContentHash    string `json:"content_hash"`
	ContentLength  int64  `json:"content_length"`
	DuplicateCount int    `json:"duplicate_count"`
	LastRequestId  string `json:"last_request_id"`
	LastSeenAt     int64  `json:"last_seen_at"`
	StorageBytes   int64  `json:"storage_bytes"`
	Truncated      bool   `json:"truncated"`
	CreatedAt      int64  `json:"created_at"`
}

type RequestContentLogQuery struct {
	UserId         int
	Username       string
	TokenId        int
	TokenName      string
	ChannelId      int
	ModelName      string
	Group          string
	RequestId      string
	Keyword        string
	StartTimestamp int64
	EndTimestamp   int64
}

func RecordRequestContentLog(log *RequestContentLog) error {
	if log == nil {
		return errors.New("request content log is nil")
	}
	if LOG_DB == nil {
		return errors.New("log database is not initialized")
	}
	prepareRequestContentLogDefaults(log)
	return LOG_DB.Create(log).Error
}

func RecordRequestContentLogWithDedupe(log *RequestContentLog, windowSeconds int64) error {
	if log == nil {
		return errors.New("request content log is nil")
	}
	if LOG_DB == nil {
		return errors.New("log database is not initialized")
	}
	if windowSeconds <= 0 {
		return RecordRequestContentLog(log)
	}
	prepareRequestContentLogDefaults(log)
	cutoff := log.LastSeenAt - windowSeconds
	return LOG_DB.Transaction(func(tx *gorm.DB) error {
		existing := &RequestContentLog{}
		err := tx.Where("user_id = ? AND token_id = ? AND path = ? AND model_name = ? AND relay_mode = ? AND content_hash = ?",
			log.UserId, log.TokenId, log.Path, log.ModelName, log.RelayMode, log.ContentHash).
			Where("(last_seen_at >= ? OR (last_seen_at = 0 AND created_at >= ?))", cutoff, cutoff).
			Order(requestContentLogSeenAtExpr() + " DESC").
			Order("id desc").
			First(existing).Error
		if err == nil {
			return tx.Model(existing).Updates(map[string]any{
				"duplicate_count": gorm.Expr("CASE WHEN duplicate_count > 0 THEN duplicate_count + ? ELSE ? END", 1, 2),
				"last_seen_at":    log.LastSeenAt,
				"last_request_id": log.RequestId,
			}).Error
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		return tx.Create(log).Error
	})
}

func GetRequestContentLogs(query RequestContentLogQuery, startIdx int, num int) (logs []*RequestContentLogListItem, total int64, err error) {
	tx, err := buildRequestContentLogQuery(query)
	if err != nil {
		return nil, 0, err
	}
	if err = tx.Model(&RequestContentLog{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	err = tx.Select(requestContentLogListSelectClause()).
		Order(requestContentLogSeenAtExpr() + " DESC").
		Order("request_content_logs.id desc").
		Limit(num).
		Offset(startIdx).
		Scan(&logs).Error
	return logs, total, err
}

func GetRequestContentLogByRequestId(requestId string) (log *RequestContentLog, err error) {
	if strings.TrimSpace(requestId) == "" {
		return nil, errors.New("request_id is required")
	}
	log = &RequestContentLog{}
	err = LOG_DB.Where("request_id = ? OR last_request_id = ?", requestId, requestId).Order("id desc").First(log).Error
	return log, err
}

func DeleteOldRequestContentLog(ctx context.Context, targetTimestamp int64, limit int) (int64, error) {
	var total int64
	for {
		if ctx.Err() != nil {
			return total, ctx.Err()
		}
		result := LOG_DB.Where(requestContentLogSeenAtExpr()+" < ?", targetTimestamp).Limit(limit).Delete(&RequestContentLog{})
		if result.Error != nil {
			return total, result.Error
		}
		total += result.RowsAffected
		if result.RowsAffected < int64(limit) {
			break
		}
	}
	return total, nil
}

func EnforceRequestContentLogStorageLimit(ctx context.Context, maxBytes int64, targetBytes int64, batchSize int) (deleted int64, totalBytes int64, err error) {
	if LOG_DB == nil {
		return 0, 0, errors.New("log database is not initialized")
	}
	if maxBytes <= 0 {
		return 0, 0, nil
	}
	if targetBytes <= 0 || targetBytes >= maxBytes {
		targetBytes = maxBytes * 9 / 10
	}
	if batchSize <= 0 {
		batchSize = 1000
	}
	cleanupStarted := false
	for {
		if ctx.Err() != nil {
			return deleted, totalBytes, ctx.Err()
		}
		totalBytes, err = GetRequestContentLogStorageBytes(ctx)
		if err != nil {
			return deleted, totalBytes, err
		}
		if !cleanupStarted {
			if totalBytes <= maxBytes {
				return deleted, totalBytes, nil
			}
			cleanupStarted = true
		}
		if totalBytes <= targetBytes {
			return deleted, totalBytes, nil
		}

		var ids []int
		err = LOG_DB.WithContext(ctx).
			Model(&RequestContentLog{}).
			Order(requestContentLogSeenAtExpr()+" ASC").
			Order("request_content_logs.created_at ASC").
			Order("request_content_logs.id ASC").
			Limit(batchSize).
			Pluck("request_content_logs.id", &ids).Error
		if err != nil {
			return deleted, totalBytes, err
		}
		if len(ids) == 0 {
			return deleted, totalBytes, nil
		}

		result := LOG_DB.WithContext(ctx).Delete(&RequestContentLog{}, ids)
		if result.Error != nil {
			return deleted, totalBytes, result.Error
		}
		deleted += result.RowsAffected
		if result.RowsAffected == 0 {
			return deleted, totalBytes, nil
		}
	}
}

func GetRequestContentLogStorageBytes(ctx context.Context) (int64, error) {
	if LOG_DB == nil {
		return 0, errors.New("log database is not initialized")
	}
	var totalBytes int64
	err := LOG_DB.WithContext(ctx).
		Model(&RequestContentLog{}).
		Select(requestContentLogStorageSumExpr()).
		Scan(&totalBytes).Error
	return totalBytes, err
}

func buildRequestContentLogQuery(query RequestContentLogQuery) (*gorm.DB, error) {
	tx := LOG_DB.Model(&RequestContentLog{})
	if query.UserId != 0 {
		tx = tx.Where("request_content_logs.user_id = ?", query.UserId)
	}
	if query.Username != "" {
		tx = tx.Where("request_content_logs.username = ?", query.Username)
	}
	if query.TokenId != 0 {
		tx = tx.Where("request_content_logs.token_id = ?", query.TokenId)
	}
	if query.TokenName != "" {
		tx = tx.Where("request_content_logs.token_name = ?", query.TokenName)
	}
	if query.ChannelId != 0 {
		tx = tx.Where("request_content_logs.channel_id = ?", query.ChannelId)
	}
	if query.ModelName != "" {
		pattern := requestContentLikePattern(query.ModelName)
		tx = tx.Where("request_content_logs.model_name LIKE ? ESCAPE '!'", pattern)
	}
	if query.Group != "" {
		tx = tx.Where("request_content_logs."+logGroupCol+" = ?", query.Group)
	}
	if query.RequestId != "" {
		tx = tx.Where("request_content_logs.request_id = ?", query.RequestId)
	}
	if query.Keyword != "" {
		pattern := requestContentLikePattern(query.Keyword)
		tx = tx.Where("request_content_logs.content_text LIKE ? ESCAPE '!'", pattern)
	}
	if query.StartTimestamp != 0 {
		tx = tx.Where(requestContentLogSeenAtExpr()+" >= ?", query.StartTimestamp)
	}
	if query.EndTimestamp != 0 {
		tx = tx.Where(requestContentLogSeenAtExpr()+" <= ?", query.EndTimestamp)
	}
	return tx, nil
}

func requestContentLikePattern(input string) string {
	replacer := strings.NewReplacer(
		"!", "!!",
		"%", "!%",
		"_", "!_",
	)
	return "%" + replacer.Replace(input) + "%"
}

func requestContentLogListSelectClause() string {
	return "request_content_logs.id, " +
		"request_content_logs.request_id, " +
		"request_content_logs.user_id, " +
		"request_content_logs.username, " +
		"request_content_logs.token_id, " +
		"request_content_logs.token_name, " +
		"request_content_logs.channel_id, " +
		"request_content_logs.model_name, " +
		"request_content_logs." + logGroupCol + ", " +
		"request_content_logs.relay_mode, " +
		"request_content_logs.path, " +
		"request_content_logs.content_text, " +
		"request_content_logs.content_hash, " +
		"LENGTH(request_content_logs.content) AS content_length, " +
		"request_content_logs.duplicate_count, " +
		"request_content_logs.last_request_id, " +
		"request_content_logs.last_seen_at, " +
		"CASE WHEN request_content_logs.storage_bytes > 0 THEN request_content_logs.storage_bytes ELSE LENGTH(request_content_logs.content) + LENGTH(request_content_logs.content_text) + 1024 END AS storage_bytes, " +
		"request_content_logs.truncated, " +
		"request_content_logs.created_at"
}

func prepareRequestContentLogDefaults(log *RequestContentLog) {
	if log.CreatedAt == 0 {
		log.CreatedAt = log.LastSeenAt
	}
	if log.LastSeenAt == 0 {
		log.LastSeenAt = log.CreatedAt
	}
	if log.LastRequestId == "" {
		log.LastRequestId = log.RequestId
	}
	if log.DuplicateCount <= 0 {
		log.DuplicateCount = 1
	}
	if log.StorageBytes <= 0 {
		log.StorageBytes = EstimateRequestContentLogStorageBytes(log.Content, log.ContentText)
	}
}

func requestContentLogSeenAtExpr() string {
	return "CASE WHEN request_content_logs.last_seen_at > 0 THEN request_content_logs.last_seen_at ELSE request_content_logs.created_at END"
}

func requestContentLogStorageSumExpr() string {
	return "COALESCE(SUM(CASE WHEN request_content_logs.storage_bytes > 0 THEN request_content_logs.storage_bytes ELSE LENGTH(request_content_logs.content) + LENGTH(request_content_logs.content_text) + 1024 END), 0)"
}

func EstimateRequestContentLogStorageBytes(content string, contentText string) int64 {
	return int64(len(content) + len(contentText) + 1024)
}
