package controller

import (
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/gin-gonic/gin"
)

func GetModelConcurrency(c *gin.Context) {
	config := setting.GetModelConcurrencyConfig()
	usage, err := common.GetModelConcurrencyUsage(c.Request.Context(), config.Rules)
	data := gin.H{"config": config, "usage": usage, "storage": common.ModelConcurrencyStorage(), "usage_available": err == nil}
	if err != nil {
		data["usage"] = []common.ModelConcurrencyUsage{}
		data["usage_error"] = "Concurrency storage is unavailable"
	}
	common.ApiSuccess(c, data)
}

func UpdateModelConcurrency(c *gin.Context) {
	var request struct {
		Config common.RawMessage `json:"config"`
	}
	if err := common.DecodeJson(c.Request.Body, &request); err != nil || len(request.Config) == 0 {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Invalid parameters"))
		return
	}
	config, err := common.ParseModelConcurrencyConfig(string(request.Config))
	if err != nil {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Invalid model concurrency configuration: {{error}}", map[string]any{"error": err.Error()}))
		return
	}
	encoded, err := common.Marshal(config)
	if err == nil {
		err = model.UpdateOption(common.ModelConcurrencyOptionKey, string(encoded))
	}
	if err != nil {
		common.ApiErrorStatus(c, http.StatusInternalServerError, common.NewMessage("Failed to save model concurrency configuration"))
		return
	}
	// Persistence succeeded even if Redis usage counters are currently offline.
	// The UI can refresh live usage independently of this save result.
	common.ApiSuccess(c, gin.H{"config": config})
}
