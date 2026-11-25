package controller

import (
	"fmt"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"strings"

	"github.com/gin-gonic/gin"
)

func filterPricingByUsableGroups(pricing []model.Pricing, usableGroup map[string]string) []model.Pricing {
	if len(pricing) == 0 {
		return pricing
	}
	if len(usableGroup) == 0 {
		return []model.Pricing{}
	}

	filtered := make([]model.Pricing, 0, len(pricing))
	for _, item := range pricing {
		if common.StringsContains(item.EnableGroup, "all") {
			filtered = append(filtered, item)
			continue
		}
		for _, group := range item.EnableGroup {
			if _, ok := usableGroup[group]; ok {
				filtered = append(filtered, item)
				break
			}
		}
	}
	return filtered
}

func GetPricing(c *gin.Context) {
	pricing := model.GetPricing()
	userId, exists := c.Get("id")
	usableGroup := map[string]string{}
	var group string
	if exists {
		user, err := model.GetUserCache(userId.(int))
		if err == nil {
			group = user.Group
		}
	}

	usableGroup = service.GetUserUsableGroups(group)
	groupRatio := visibleGroupRatios(group, usableGroup)
	pricing = filterPricingByUsableGroups(pricing, usableGroup)
	pricing = withVisibleGroupModelPricing(pricing, usableGroup)

	c.JSON(200, gin.H{
		"success":            true,
		"data":               pricing,
		"vendors":            model.GetVendors(),
		"group_ratio":        groupRatio,
		"usable_group":       usableGroup,
		"supported_endpoint": model.GetSupportedEndpointMap(),
		"auto_groups":        service.GetUserAutoGroup(group),
		"pricing_version":    "a42d372ccf0b5dd13ecf71203521f9d2",
	})
}

func visibleGroupRatios(userGroup string, usableGroups map[string]string) map[string]float64 {
	ratios := make(map[string]float64, len(usableGroups))
	for group := range usableGroups {
		ratio, special := ratio_setting.GetGroupGroupRatio(userGroup, group)
		if !special {
			ratio = ratio_setting.GetGroupRatio(group)
		}
		ratios[group] = ratio
	}
	return ratios
}

// Work on copies: the cached public catalog is shared by viewers with different
// usable groups. Never attach one viewer's group prices to that cache.
func withVisibleGroupModelPricing(pricing []model.Pricing, usableGroups map[string]string) []model.Pricing {
	result := make([]model.Pricing, len(pricing))
	copy(result, pricing)
	for i := range result {
		result[i].GroupModelPricing = nil
		for group := range usableGroups {
			if !common.StringsContains(result[i].EnableGroup, "all") && !common.StringsContains(result[i].EnableGroup, group) {
				continue
			}
			if rule, ok := helper.GroupModelPricingRuleForModel(group, result[i].ModelName); ok {
				if result[i].GroupModelPricing == nil {
					result[i].GroupModelPricing = make(map[string]billing_setting.GroupModelPricingRule)
				}
				result[i].GroupModelPricing[group] = rule
			}
		}
	}
	return result
}

func ResetModelRatio(c *gin.Context) {
	defaultStr := ratio_setting.DefaultModelRatio2JSONString()
	err := model.UpdateOption("ModelRatio", defaultStr)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	err = ratio_setting.UpdateModelRatioByJSONString(defaultStr)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccessT(c, "Model ratios reset", nil)
}

type ModelImageQualityPriceRequest struct {
	ModelName string                        `json:"model_name"`
	Enabled   bool                          `json:"enabled"`
	Prices    []model.ImageQualityPriceItem `json:"prices"`
}

func isImageQualityPublicPriceAllowedModel(modelName string) bool {
	modelName = strings.TrimSpace(modelName)
	if modelName == "" {
		return false
	}
	for _, p := range model.GetPricing() {
		if p.ModelName != modelName {
			continue
		}
		for _, endpointType := range p.SupportedEndpointTypes {
			et := strings.ToLower(strings.TrimSpace(string(endpointType)))
			if strings.Contains(et, "image") || strings.Contains(et, "images") {
				return true
			}
		}
		lowerName := strings.ToLower(modelName)
		return strings.Contains(lowerName, "image") ||
			strings.Contains(lowerName, "dall") ||
			strings.Contains(lowerName, "flux") ||
			strings.Contains(lowerName, "mj") ||
			strings.Contains(lowerName, "midjourney")
	}
	return false
}

func GetModelImageQualityPrices(c *gin.Context) {
	modelName := strings.TrimSpace(c.Query("model_name"))
	if modelName == "" {
		common.ApiError(c, fmt.Errorf("model_name is required"))
		return
	}
	prices, err := model.GetModelImageQualityPrices(modelName)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, prices)
}

func SaveModelImageQualityPrices(c *gin.Context) {
	var req ModelImageQualityPriceRequest
	if err := common.DecodeJson(c.Request.Body, &req); err != nil {
		common.ApiError(c, err)
		return
	}
	req.ModelName = strings.TrimSpace(req.ModelName)
	if req.ModelName == "" {
		common.ApiError(c, fmt.Errorf("model_name is required"))
		return
	}
	if !isImageQualityPublicPriceAllowedModel(req.ModelName) {
		common.ApiError(c, fmt.Errorf("only image fixed/per-call priced models can use image quality pricing"))
		return
	}
	if err := model.SaveModelImageQualityPrices(req.ModelName, req.Prices, req.Enabled); err != nil {
		common.ApiError(c, err)
		return
	}
	model.InvalidatePricingCache()
	common.ApiSuccess(c, nil)
}

func DeleteModelImageQualityPrices(c *gin.Context) {
	modelName := strings.TrimSpace(c.Query("model_name"))
	if modelName == "" {
		common.ApiError(c, fmt.Errorf("model_name is required"))
		return
	}
	if err := model.DeleteModelImageQualityPrices(modelName); err != nil {
		common.ApiError(c, err)
		return
	}
	model.InvalidatePricingCache()
	common.ApiSuccess(c, nil)
}
