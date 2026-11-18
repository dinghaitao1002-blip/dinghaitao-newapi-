package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestModelConcurrencyAdminConfigurationContract(t *testing.T) {
	db, _ := openTaskDialectDatabase(t, &model.Option{})
	originalDB, originalMap := model.DB, common.OptionMap
	originalRedisEnabled, originalRedis := common.RedisEnabled, common.RDB
	originalConfig := setting.ModelConcurrencyConfig2JSONString()
	model.DB, common.OptionMap = db, map[string]string{}
	common.RedisEnabled, common.RDB = true, nil
	t.Cleanup(func() {
		model.DB, common.OptionMap = originalDB, originalMap
		common.RedisEnabled, common.RDB = originalRedisEnabled, originalRedis
		require.NoError(t, setting.UpdateModelConcurrencyConfigByJSONString(originalConfig))
	})

	for _, body := range []string{
		`{}`,
		`{"config":{"enabled":true,"rules":[{"model":"image","max_concurrnt":2}]}}`,
		`{"config":{"enabled":true,"rules":[{"model":"image","max_concurrent":1.5}]}}`,
	} {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPatch, "/api/option/model_concurrency", strings.NewReader(body))
		UpdateModelConcurrency(c)
		assert.Equal(t, http.StatusBadRequest, recorder.Code, body)
	}
	for _, enabled := range []string{"true", "false"} {
		body := `{"config":{"enabled":` + enabled + `,"rules":[{"model":"admin-concurrency-image","enabled":true,"max_concurrent":5,"default_user_limit":2,"other_users_limit":1,"user_limits":[]}]}}`
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPatch, "/api/option/model_concurrency", strings.NewReader(body))
		UpdateModelConcurrency(c)
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		var saved struct {
			Success bool `json:"success"`
		}
		require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &saved))
		require.True(t, saved.Success, "configuration must remain editable when Redis is offline")

		recorder = httptest.NewRecorder()
		c, _ = gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/option/model_concurrency", nil)
		GetModelConcurrency(c)
		var response struct {
			Success bool `json:"success"`
			Data    struct {
				Config         common.ModelConcurrencyConfig `json:"config"`
				UsageAvailable bool                          `json:"usage_available"`
			} `json:"data"`
		}
		require.Equal(t, http.StatusOK, recorder.Code)
		require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
		assert.True(t, response.Success)
		assert.Equal(t, enabled == "true", response.Data.Config.Enabled)
		assert.False(t, response.Data.UsageAvailable, "missing counters must not be presented as zero active tasks")
	}
}

func TestUpdateOptionRejectsNegativeClaudeDefaultMaxTokens(t *testing.T) {
	response := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(response)
	context.Request = httptest.NewRequest(
		http.MethodPut,
		"/api/option/",
		strings.NewReader(`{"key":"claude.default_max_tokens","value":"{\"default\":-1}"}`),
	)

	UpdateOption(context)

	assert.Equal(t, http.StatusOK, response.Code)
	var payload struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
	}
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &payload))
	assert.False(t, payload.Success)
	assert.Contains(t, payload.Message, "-1")
}
