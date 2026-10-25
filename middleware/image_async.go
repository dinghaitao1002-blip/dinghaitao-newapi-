package middleware

import (
	"net"
	"net/http"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

// ImageTaskReadAuth follows TokenAuthReadOnly and adds expiry/IP restrictions.
func ImageTaskReadAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		token, err := model.GetTokenByIds(c.GetInt("token_id"), c.GetInt("id"))
		if err != nil || token == nil || (token.Status != common.TokenStatusEnabled && token.Status != common.TokenStatusExhausted) ||
			(token.ExpiredTime != -1 && token.ExpiredTime < time.Now().Unix()) {
			abortWithOpenAiMessage(c, http.StatusUnauthorized, "Token is expired or unavailable")
			return
		}
		if ips := token.GetIpLimits(); len(ips) > 0 && !common.IsIpInCIDRList(net.ParseIP(c.ClientIP()), ips) {
			abortWithOpenAiMessage(c, http.StatusForbidden, "Client IP is not allowed by this token")
		}
	}
}

func ImageTaskReadRateLimit() gin.HandlerFunc {
	return userRateLimitFactory(600, 60, "IMAGE-TASK-READ")
}
