package controller

import (
	"net/http"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
)

func GetRequestContentLogs(c *gin.Context) {
	pageInfo := common.GetPageQuery(c)
	userId, _ := strconv.Atoi(c.Query("user_id"))
	tokenId, _ := strconv.Atoi(c.Query("token_id"))
	channelId, _ := strconv.Atoi(c.Query("channel_id"))
	startTimestamp, _ := strconv.ParseInt(c.Query("start_timestamp"), 10, 64)
	endTimestamp, _ := strconv.ParseInt(c.Query("end_timestamp"), 10, 64)

	logs, total, err := model.GetRequestContentLogs(model.RequestContentLogQuery{
		UserId:         userId,
		Username:       c.Query("username"),
		TokenId:        tokenId,
		TokenName:      c.Query("token_name"),
		ChannelId:      channelId,
		ModelName:      c.Query("model_name"),
		Group:          c.Query("group"),
		RequestId:      c.Query("request_id"),
		Keyword:        c.Query("keyword"),
		StartTimestamp: startTimestamp,
		EndTimestamp:   endTimestamp,
	}, pageInfo.GetStartIdx(), pageInfo.GetPageSize())
	if err != nil {
		common.ApiError(c, err)
		return
	}
	pageInfo.SetTotal(int(total))
	pageInfo.SetItems(logs)
	common.ApiSuccess(c, pageInfo)
}

func GetRequestContentLogByRequestId(c *gin.Context) {
	requestId := c.Param("request_id")
	log, err := model.GetRequestContentLogByRequestId(requestId)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, log)
}

func DeleteHistoryRequestContentLogs(c *gin.Context) {
	targetTimestamp, _ := strconv.ParseInt(c.Query("target_timestamp"), 10, 64)
	if targetTimestamp == 0 {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": "target timestamp is required",
		})
		return
	}
	count, err := model.DeleteOldRequestContentLog(c.Request.Context(), targetTimestamp, 100)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, count)
}
