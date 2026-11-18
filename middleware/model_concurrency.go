package middleware

import (
	"errors"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

// BeginModelConcurrency rejects before billing/upstream work. Status/result
// requests do not call this; asynchronous admission calls it after idempotency.
func BeginModelConcurrency(c *gin.Context, model string) (func(), bool) {
	finish, err := service.BeginModelConcurrency(c, model)
	if err == nil {
		return finish, true
	}
	status, code, message := http.StatusServiceUnavailable, "model_concurrency_unavailable", i18n.T(c, "model_concurrency.unavailable")
	var limit *common.ModelConcurrencyLimitError
	if errors.As(err, &limit) {
		status, code, message = http.StatusTooManyRequests, "model_concurrency_exceeded", i18n.T(c, "model_concurrency.exceeded", map[string]any{"Model": model, "Limit": limit.Limit, "Current": limit.Current})
		c.Header("Retry-After", "1")
	} else {
		common.SysError("model concurrency admission failed: " + err.Error())
	}
	c.AbortWithStatusJSON(status, gin.H{"error": gin.H{"type": "rate_limit_error", "code": code, "message": message}})
	return nil, false
}

func isDeferredImageAdmission(c *gin.Context) bool {
	if c.Request.Method != http.MethodPost {
		return false
	}
	switch c.Request.URL.Path {
	case "/v1/images/generations", "/v1/images/edits", "/v1/edits":
	default:
		return false
	}
	if c.Query("async") == "true" {
		return true
	}
	for pref := range strings.SplitSeq(c.GetHeader("Prefer"), ",") {
		if strings.EqualFold(strings.TrimSpace(pref), "respond-async") {
			return true
		}
	}
	return false
}
