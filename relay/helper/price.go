package helper

import (
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
)

func modelPriceNotConfiguredError(modelName string, userId int) error {
	if model.IsAdmin(userId) {
		return fmt.Errorf(
			"模型 %s 的价格未配置。请前往「系统设置 → 运营设置」开启自用模式，或在「系统设置 → 分组与模型定价设置」中为该模型配置价格；"+
				"Model %s price not configured. Go to System Settings → Operation Settings to enable self-use mode, or configure the model price in System Settings → Group & Model Pricing.",
			modelName, modelName,
		)
	}
	return fmt.Errorf(
		"模型 %s 的价格尚未由管理员配置，暂时无法使用，请联系站点管理员开启该模型；"+
			"Model %s has not been priced by the administrator yet. Please contact the site administrator to enable this model.",
		modelName, modelName,
	)
}

// https://docs.claude.com/en/docs/build-with-claude/prompt-caching#1-hour-cache-duration
const claudeCacheCreation1hMultiplier = 6 / 3.75

// defaultTieredPreConsumeMaxTokens is the fallback completion-token estimate
// used for tiered expression pre-consume when the client omits max_tokens, so
// the pre-consumed quota still reflects a plausible output cost in paid groups.
const defaultTieredPreConsumeMaxTokens = 8192

// HandleGroupRatio checks for "auto_group" in the context and updates the group ratio and relayInfo.UsingGroup if present
func HandleGroupRatio(ctx *gin.Context, relayInfo *relaycommon.RelayInfo) types.GroupRatioInfo {
	groupRatioInfo := types.GroupRatioInfo{
		GroupRatio:        1.0, // default ratio
		GroupSpecialRatio: -1,
	}

	// check auto group
	autoGroup, exists := ctx.Get("auto_group")
	if exists {
		logger.LogDebug(ctx, "final group: %s", autoGroup)
		relayInfo.UsingGroup = autoGroup.(string)
	}

	// check user group special ratio
	// Use request admission time, including retries and requests crossing 08:00/20:00.
	userGroupRatio, ok := ratio_setting.GetGroupGroupRatioAt(relayInfo.UserGroup, relayInfo.UsingGroup, relayInfo.StartTime)
	if ok {
		// user group special ratio
		groupRatioInfo.GroupSpecialRatio = userGroupRatio
		groupRatioInfo.GroupRatio = userGroupRatio
		groupRatioInfo.HasSpecialRatio = true
	} else {
		// normal group ratio
		groupRatioInfo.GroupRatio = ratio_setting.GetGroupRatioAt(relayInfo.UsingGroup, relayInfo.StartTime)
	}

	return groupRatioInfo
}

func resolveImageQualityPricing(c *gin.Context, info *relaycommon.RelayInfo, meta *types.TokenCountMeta) (float64, bool, error) {
	if info == nil || info.Request == nil {
		return 0, false, nil
	}

	imageReq, ok := info.Request.(*dto.ImageRequest)
	if !ok || imageReq == nil {
		return 0, false, nil
	}

	modelName := strings.TrimSpace(info.OriginModelName)
	if modelName == "" {
		return 0, false, nil
	}

	quality := model.NormalizeImageQuality(imageReq.Quality)
	resolution := model.NormalizeImageResolution(imageReq.Size)

	// 只有确实存在品质价格配置时，才要求请求尺寸可归一化。
	// 未配置品质价格的模型必须继续回退到普通模型价格/倍率计费。
	// 1. 用户专属生图品质密价优先级最高。
	userPrices, err := model.GetUserModelImageQualityPrices(info.UserId, modelName)
	if err != nil {
		return 0, false, err
	}
	if len(userPrices) > 0 {
		if resolution == "" {
			return 0, false, fmt.Errorf("unsupported image resolution for user quality pricing: model=%s size=%s", modelName, imageReq.Size)
		}
		if price, ok := model.GetUserModelImageQualityPrice(info.UserId, modelName, quality, resolution); ok {
			// 品质价格已经是最终单张价格，不再叠加 DALL-E 内置 size/quality ratio，只保留 n。
			if meta != nil {
				meta.ImagePriceRatio = 0
			}
			info.ImageQualityPriceSource = "user_image_quality_private"
			info.ImageQuality = quality
			info.ImageResolution = resolution
			info.ImageQualityUnitPrice = price
			logger.LogInfo(c, fmt.Sprintf("user image quality private price applied: userId=%d, model=%s, quality=%s, resolution=%s, price=%f", info.UserId, modelName, quality, resolution, price))
			return price, true, nil
		}
		return 0, false, fmt.Errorf("user image quality private price not configured: model=%s quality=%s resolution=%s", modelName, quality, resolution)
	}

	// 2. 模型公开生图品质价格。
	modelPrices, err := model.GetModelImageQualityPrices(modelName)
	if err != nil {
		return 0, false, err
	}
	if len(modelPrices) > 0 {
		if resolution == "" {
			return 0, false, fmt.Errorf("unsupported image resolution for model quality pricing: model=%s size=%s", modelName, imageReq.Size)
		}
		if price, ok := model.GetModelImageQualityPrice(modelName, quality, resolution); ok {
			if meta != nil {
				meta.ImagePriceRatio = 0
			}
			info.ImageQualityPriceSource = "model_image_quality_public"
			info.ImageQuality = quality
			info.ImageResolution = resolution
			info.ImageQualityUnitPrice = price
			logger.LogInfo(c, fmt.Sprintf("model image quality price applied: userId=%d, model=%s, quality=%s, resolution=%s, price=%f", info.UserId, modelName, quality, resolution, price))
			return price, true, nil
		}
		return 0, false, fmt.Errorf("model image quality price not configured: model=%s quality=%s resolution=%s", modelName, quality, resolution)
	}

	return 0, false, nil
}

func ModelPriceHelper(c *gin.Context, info *relaycommon.RelayInfo, promptTokens int, meta *types.TokenCountMeta) (types.PriceData, error) {
	billingMode := billing_setting.GetBillingMode(info.OriginModelName)

	var modelPrice float64
	var usePrice bool

	if billingMode == billing_setting.BillingModeImageQuality {
		// Image-quality mode is exclusive: never read or fall back to ordinary pricing.
		imageQualityPrice, ok, err := resolveImageQualityPricing(c, info, meta)
		if err != nil {
			return types.PriceData{}, err
		}
		if !ok {
			return types.PriceData{}, modelPriceNotConfiguredError(
				info.OriginModelName,
				info.UserId,
			)
		}

		modelPrice = imageQualityPrice
		usePrice = true
	} else {
		modelPrice, usePrice = ratio_setting.GetModelPrice(
			info.OriginModelName,
			false,
		)

		if imageQualityPrice, ok, err := resolveImageQualityPricing(c, info, meta); err != nil {
			return types.PriceData{}, err
		} else if ok {
			modelPrice = imageQualityPrice
			usePrice = true
		} else if usePrice {
			if userPrice, ok := model.GetUserModelPrice(
				info.UserId,
				info.OriginModelName,
			); ok {
				modelPrice = userPrice
				logger.LogInfo(c, fmt.Sprintf(
					"user private model price applied: userId=%d, model=%s, price=%f",
					info.UserId,
					info.OriginModelName,
					userPrice,
				))
			}
		}
	}

	groupRatioInfo := HandleGroupRatio(c, info)

	// Check if this model uses tiered_expr billing
	if billing_setting.GetBillingMode(info.OriginModelName) == billing_setting.BillingModeTieredExpr {
		return modelPriceHelperTiered(c, info, promptTokens, meta, groupRatioInfo)
	}

	var preConsumedQuota int
	var modelRatio float64
	var completionRatio float64
	var cacheRatio float64
	var imageRatio float64
	var cacheCreationRatio float64
	var cacheCreationRatio5m float64
	var cacheCreationRatio1h float64
	var audioRatio float64
	var audioCompletionRatio float64
	var freeModel bool
	if !usePrice {
		preConsumedTokens := common.Max(promptTokens, common.PreConsumedQuota)
		if meta.MaxTokens != 0 {
			preConsumedTokens += meta.MaxTokens
		}
		var success bool
		var matchName string
		modelRatio, success, matchName = ratio_setting.GetModelRatio(info.OriginModelName)
		if !success {
			acceptUnsetRatio := false
			if info.UserSetting.AcceptUnsetRatioModel {
				acceptUnsetRatio = true
			}
			if !acceptUnsetRatio {
				return types.PriceData{}, modelPriceNotConfiguredError(matchName, info.UserId)
			}
		}
		completionRatio = ratio_setting.GetCompletionRatio(info.OriginModelName)
		cacheRatio, _ = ratio_setting.GetCacheRatio(info.OriginModelName)
		cacheCreationRatio, _ = ratio_setting.GetCreateCacheRatio(info.OriginModelName)
		cacheCreationRatio5m = cacheCreationRatio
		// 固定1h和5min缓存写入价格的比例
		cacheCreationRatio1h = cacheCreationRatio * claudeCacheCreation1hMultiplier
		imageRatio, _ = ratio_setting.GetImageRatio(info.OriginModelName)
		audioRatio = ratio_setting.GetAudioRatio(info.OriginModelName)
		audioCompletionRatio = ratio_setting.GetAudioCompletionRatio(info.OriginModelName)
		ratio := modelRatio * groupRatioInfo.GroupRatio
		quota, err := common.QuotaFromFloatStrict(float64(preConsumedTokens) * ratio)
		if err != nil {
			return types.PriceData{}, err
		}
		preConsumedQuota = quota
	} else {
		if meta.ImagePriceRatio != 0 {
			modelPrice = modelPrice * meta.ImagePriceRatio
		}
	}

	// check if free model pre-consume is disabled
	if !operation_setting.GetQuotaSetting().EnableFreeModelPreConsume {
		// if model price or ratio is 0, do not pre-consume quota
		if groupRatioInfo.GroupRatio == 0 {
			preConsumedQuota = 0
			freeModel = true
		} else if usePrice {
			if modelPrice == 0 {
				preConsumedQuota = 0
				freeModel = true
			}
		} else {
			if modelRatio == 0 {
				preConsumedQuota = 0
				freeModel = true
			}
		}
	}

	priceData := types.PriceData{
		FreeModel:            freeModel,
		ModelPrice:           modelPrice,
		ModelRatio:           modelRatio,
		CompletionRatio:      completionRatio,
		GroupRatioInfo:       groupRatioInfo,
		UsePrice:             usePrice,
		CacheRatio:           cacheRatio,
		ImageRatio:           imageRatio,
		AudioRatio:           audioRatio,
		AudioCompletionRatio: audioCompletionRatio,
		CacheCreationRatio:   cacheCreationRatio,
		CacheCreation5mRatio: cacheCreationRatio5m,
		CacheCreation1hRatio: cacheCreationRatio1h,
		QuotaToPreConsume:    preConsumedQuota,
	}
	if usePrice {
		for name, ratio := range meta.BillingRatios {
			priceData.AddOtherRatio(name, ratio)
		}
		quotaToPreConsume := priceData.ApplyOtherRatiosToFloat(modelPrice * common.QuotaPerUnit * groupRatioInfo.GroupRatio)
		quota, err := common.QuotaFromFloatStrict(quotaToPreConsume)
		if err != nil {
			return types.PriceData{}, err
		}
		priceData.QuotaToPreConsume = quota
	}

	if common.DebugEnabled {
		logger.LogDebug(c, "model_price_helper result: %s", priceData.ToSetting())
	}
	info.PriceData = priceData
	return priceData, nil
}

// ModelPriceHelperPerCall 按次/按量计费的 PriceHelper (MJ、Task)
func ModelPriceHelperPerCall(c *gin.Context, info *relaycommon.RelayInfo) (types.PriceData, error) {
	groupRatioInfo := HandleGroupRatio(c, info)

	modelPrice, success := ratio_setting.GetModelPrice(info.OriginModelName, true)
	usePrice := success
	if billing_setting.GetBillingMode(info.OriginModelName) == billing_setting.BillingModeVideoPerSecond {
		basePrice, ok := billing_setting.GetVideoSecondBasePrice(info.OriginModelName)
		if !ok {
			return types.PriceData{}, fmt.Errorf("video per-second price not configured for model %s", info.OriginModelName)
		}
		modelPrice = basePrice
		success = true
		usePrice = true
	} else if userPrice, ok := model.GetUserModelPrice(info.UserId, info.OriginModelName); ok {
		modelPrice = userPrice
		success = true
		usePrice = true
		logger.LogInfo(c, fmt.Sprintf("user private model price applied: userId=%d, model=%s, price=%f", info.UserId, info.OriginModelName, userPrice))
	}
	var modelRatio float64

	if !success {
		defaultPrice, ok := ratio_setting.GetDefaultModelPriceMap()[info.OriginModelName]
		if ok {
			modelPrice = defaultPrice
			usePrice = true
		} else {
			var ratioSuccess bool
			var matchName string
			modelRatio, ratioSuccess, matchName = ratio_setting.GetModelRatio(info.OriginModelName)
			acceptUnsetRatio := false
			if info.UserSetting.AcceptUnsetRatioModel {
				acceptUnsetRatio = true
			}
			if !ratioSuccess && !acceptUnsetRatio {
				return types.PriceData{}, modelPriceNotConfiguredError(matchName, info.UserId)
			}
		}
	}

	var quota int
	freeModel := false

	if usePrice {
		var err error
		quota, err = common.QuotaFromFloatStrict(modelPrice * common.QuotaPerUnit * groupRatioInfo.GroupRatio)
		if err != nil {
			return types.PriceData{}, err
		}
		if !operation_setting.GetQuotaSetting().EnableFreeModelPreConsume {
			if groupRatioInfo.GroupRatio == 0 || modelPrice == 0 {
				quota = 0
				freeModel = true
			}
		}
	} else {
		// 按量计费：以模型倍率的一半作为预扣额度
		var err error
		quota, err = common.QuotaFromFloatStrict(modelRatio / 2 * common.QuotaPerUnit * groupRatioInfo.GroupRatio)
		if err != nil {
			return types.PriceData{}, err
		}
		modelPrice = -1
		if !operation_setting.GetQuotaSetting().EnableFreeModelPreConsume {
			if groupRatioInfo.GroupRatio == 0 || modelRatio == 0 {
				quota = 0
				freeModel = true
			}
		}
	}

	priceData := types.PriceData{
		FreeModel:      freeModel,
		ModelPrice:     modelPrice,
		ModelRatio:     modelRatio,
		UsePrice:       usePrice,
		Quota:          quota,
		GroupRatioInfo: groupRatioInfo,
	}
	return priceData, nil
}

func HasModelBillingConfig(modelName string) bool {
	if _, ok := ratio_setting.GetModelPrice(modelName, false); ok {
		return true
	}
	if _, ok, _ := ratio_setting.GetModelRatio(modelName); ok {
		return true
	}

	switch billing_setting.GetBillingMode(modelName) {
	case billing_setting.BillingModeTieredExpr:
		expr, ok := billing_setting.GetBillingExpr(modelName)
		return ok && strings.TrimSpace(expr) != ""
	case billing_setting.BillingModeVideoPerSecond:
		expr, ok := billing_setting.GetBillingExpr(modelName)
		return ok && strings.TrimSpace(expr) != ""
	case billing_setting.BillingModeImageQuality:
		prices, err := model.GetModelImageQualityPrices(modelName)
		if err != nil {
			return false
		}
		for _, price := range prices {
			if price.Enabled {
				return true
			}
		}
		return false
	default:
		return false
	}
}

func modelPriceHelperTiered(c *gin.Context, info *relaycommon.RelayInfo, promptTokens int, meta *types.TokenCountMeta, groupRatioInfo types.GroupRatioInfo) (types.PriceData, error) {
	exprStr, ok := billing_setting.GetBillingExpr(info.OriginModelName)
	if !ok {
		return types.PriceData{}, fmt.Errorf("model %s is configured as tiered_expr but has no billing expression", info.OriginModelName)
	}

	estimatedCompletionTokens := meta.MaxTokens
	if estimatedCompletionTokens == 0 && groupRatioInfo.GroupRatio != 0 {
		estimatedCompletionTokens = defaultTieredPreConsumeMaxTokens
	}

	requestInput, err := ResolveIncomingBillingExprRequestInput(c, info)
	if err != nil {
		return types.PriceData{}, err
	}

	rawCost, trace, err := billingexpr.RunExprWithRequest(exprStr, billingexpr.TokenParams{
		P:   float64(promptTokens),
		C:   float64(estimatedCompletionTokens),
		Len: float64(promptTokens),
	}, requestInput)
	if err != nil {
		return types.PriceData{}, fmt.Errorf("model %s tiered expr run failed: %w", info.OriginModelName, err)
	}

	// Expression coefficients are $/1M tokens prices; convert to quota the same way per-call billing does.
	quotaBeforeGroup := rawCost / 1_000_000 * common.QuotaPerUnit
	preConsumedQuota, err := billingexpr.QuotaRoundStrict(quotaBeforeGroup * groupRatioInfo.GroupRatio)
	if err != nil {
		return types.PriceData{}, err
	}

	freeModel := false
	if !operation_setting.GetQuotaSetting().EnableFreeModelPreConsume {
		if groupRatioInfo.GroupRatio == 0 {
			preConsumedQuota = 0
			freeModel = true
		}
	}

	exprHash := billingexpr.ExprHashString(exprStr)
	snapshot := &billingexpr.BillingSnapshot{
		BillingMode:               billing_setting.BillingModeTieredExpr,
		ModelName:                 info.OriginModelName,
		ExprString:                exprStr,
		ExprHash:                  exprHash,
		GroupRatio:                groupRatioInfo.GroupRatio,
		EstimatedPromptTokens:     promptTokens,
		EstimatedCompletionTokens: estimatedCompletionTokens,
		EstimatedQuotaBeforeGroup: quotaBeforeGroup,
		EstimatedQuotaAfterGroup:  preConsumedQuota,
		EstimatedTier:             trace.MatchedTier,
		QuotaPerUnit:              common.QuotaPerUnit,
		ExprVersion:               billingexpr.ExprVersion(exprStr),
	}
	info.TieredBillingSnapshot = snapshot
	info.BillingRequestInput = &requestInput

	priceData := types.PriceData{
		FreeModel:         freeModel,
		GroupRatioInfo:    groupRatioInfo,
		QuotaToPreConsume: preConsumedQuota,
	}

	logger.LogDebug(c, "model_price_helper_tiered result: model=%s preConsume=%d quotaBeforeGroup=%.2f groupRatio=%.2f tier=%s", info.OriginModelName, preConsumedQuota, quotaBeforeGroup, groupRatioInfo.GroupRatio, trace.MatchedTier)

	info.PriceData = priceData
	return priceData, nil
}
