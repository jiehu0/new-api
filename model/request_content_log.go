package model

import (
	"context"
	"errors"
	"strings"

	"gorm.io/gorm"
)

type RequestContentLog struct {
	Id          int    `json:"id" gorm:"index:idx_request_content_created_id,priority:1;index:idx_request_content_user_id,priority:2"`
	RequestId   string `json:"request_id" gorm:"type:varchar(64);index:idx_request_content_request_id;default:''"`
	UserId      int    `json:"user_id" gorm:"index;index:idx_request_content_user_id,priority:1"`
	Username    string `json:"username" gorm:"index;default:''"`
	TokenId     int    `json:"token_id" gorm:"index;default:0"`
	TokenName   string `json:"token_name" gorm:"index;default:''"`
	ChannelId   int    `json:"channel_id" gorm:"index;default:0"`
	ModelName   string `json:"model_name" gorm:"index;default:''"`
	Group       string `json:"group" gorm:"index;default:''"`
	RelayMode   int    `json:"relay_mode" gorm:"index;default:0"`
	Path        string `json:"path" gorm:"type:varchar(255);default:''"`
	Content     string `json:"content" gorm:"type:text"`
	ContentText string `json:"content_text" gorm:"type:text"`
	ContentHash string `json:"content_hash" gorm:"type:varchar(64);index;default:''"`
	Truncated   bool   `json:"truncated"`
	CreatedAt   int64  `json:"created_at" gorm:"bigint;index:idx_request_content_created_id,priority:2"`
}

type RequestContentLogListItem struct {
	Id            int    `json:"id"`
	RequestId     string `json:"request_id"`
	UserId        int    `json:"user_id"`
	Username      string `json:"username"`
	TokenId       int    `json:"token_id"`
	TokenName     string `json:"token_name"`
	ChannelId     int    `json:"channel_id"`
	ModelName     string `json:"model_name"`
	Group         string `json:"group"`
	RelayMode     int    `json:"relay_mode"`
	Path          string `json:"path"`
	ContentText   string `json:"content_text"`
	ContentHash   string `json:"content_hash"`
	ContentLength int64  `json:"content_length"`
	Truncated     bool   `json:"truncated"`
	CreatedAt     int64  `json:"created_at"`
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
	return LOG_DB.Create(log).Error
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
	err = LOG_DB.Where("request_id = ?", requestId).Order("id desc").First(log).Error
	return log, err
}

func DeleteOldRequestContentLog(ctx context.Context, targetTimestamp int64, limit int) (int64, error) {
	var total int64
	for {
		if ctx.Err() != nil {
			return total, ctx.Err()
		}
		result := LOG_DB.Where("created_at < ?", targetTimestamp).Limit(limit).Delete(&RequestContentLog{})
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
		tx = tx.Where("request_content_logs.created_at >= ?", query.StartTimestamp)
	}
	if query.EndTimestamp != 0 {
		tx = tx.Where("request_content_logs.created_at <= ?", query.EndTimestamp)
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
		"request_content_logs.truncated, " +
		"request_content_logs.created_at"
}
