package helper

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/reasoning"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/QuantumNous/new-api/setting/model_setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	hostreasoning "github.com/QuantumNous/new-api/setting/reasoning"
	hosttypes "github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
)

func modelPriceNotConfiguredError(c *gin.Context, modelName string, userId int) error {
	key := i18n.MsgRelayModelPriceNotConfigured
	if model.IsAdmin(userId) {
		key = i18n.MsgRelayModelPriceNotConfiguredAdmin
	}
	return errors.New(i18n.T(c, key, map[string]any{"Model": modelName}))
}

// HandleGroupRatio checks for "auto_group" in the context and updates the group ratio and relayInfo.UsingGroup if present
func HandleGroupRatio(ctx *gin.Context, relayInfo *relaycommon.RelayInfo) hosttypes.GroupRatioInfo {
	groupRatioInfo := hosttypes.GroupRatioInfo{
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

// HasGroupModelPricing checks the client-facing identities, never the upstream
// channel mapping. Auto-group retries use it before refreshing their reservation.
func HasGroupModelPricing(info *relaycommon.RelayInfo, group string) bool {
	if info == nil {
		return false
	}
	_, _, ok := groupModelPricingRule(group, info.GetOriginModelName())
	return ok
}

// GroupModelPricingRuleForModel shares relay's client-name/canonical-name
// precedence with public pricing. Channel model mappings never set the price.
func GroupModelPricingRuleForModel(group, origin string) (billing_setting.GroupModelPricingRule, bool) {
	_, rule, ok := groupModelPricingRule(group, origin)
	return rule, ok
}

func groupModelPricingRule(group, origin string) (string, billing_setting.GroupModelPricingRule, bool) {
	for _, name := range []string{origin, resolveBillingModelName(origin)} {
		if rule, ok := billing_setting.GetGroupModelPricingRule(group, name); ok {
			return name, rule, true
		}
	}
	return "", billing_setting.GroupModelPricingRule{}, false
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

	// The alias table in NormalizeImageQuality is frozen. Any other client value
	// is priced as medium, the same tier as an absent or "auto" quality.
	quality := model.NormalizeImageQuality(imageReq.Quality)
	switch quality {
	case model.ImageQualityLow, model.ImageQualityMedium, model.ImageQualityHigh:
	default:
		quality = model.ImageQualityMedium
	}
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

func ModelPriceHelper(c *gin.Context, info *relaycommon.RelayInfo, promptTokens int, meta *types.TokenCountMeta) (hosttypes.PriceData, error) {
	if info != nil {
		if matched := resolveBillingModelName(info.GetOriginModelName()); matched != "" && matched != info.OriginModelName {
			info.BillingModelName = matched
		}
	}
	billingModelName := info.GetBillingModelName()
	groupRatioInfo := HandleGroupRatio(c, info)
	c.Set("group_model_pricing_applied", false)
	if name, rule, configured := groupModelPricingRule(info.UsingGroup, info.GetOriginModelName()); configured {
		if _, err := billing_setting.ParseGroupModelPricingRule(rule); err != nil {
			return hosttypes.PriceData{}, err
		}
		c.Set("group_model_pricing_applied", true)
		info.BillingModelName = name
		expression := rule.Expression
		if billing_setting.IsGroupModelPricingForced(info.UsingGroup) {
			if rule.Unit == "second" {
				return hosttypes.PriceData{}, fmt.Errorf("per-second group pricing requires a task usage meter")
			}
			return modelPriceHelperTieredExpression(c, info, name, promptTokens, groupRatioInfo, expression, true)
		}
		// User-specific image grids retain their per-image meaning, even when
		// the group's default is a request price. Public grids are below group prices.
		if _, image := info.Request.(*dto.ImageRequest); image && info.UserId > 0 {
			prices, err := model.GetUserModelImageQualityPrices(info.UserId, info.GetOriginModelName())
			if err != nil {
				return hosttypes.PriceData{}, err
			}
			if len(prices) > 0 {
				price, matched, err := resolveImageQualityPricing(c, info, meta)
				if err != nil {
					return hosttypes.PriceData{}, err
				}
				if matched {
					expression = `tier("user_private", fixed(` + strconv.FormatFloat(price, 'g', -1, 64) + `)) * image_count`
					return modelPriceHelperTieredExpression(c, info, name, promptTokens, groupRatioInfo, expression, true)
				}
			}
		}
		for index, userModelName := range []string{billingModelName, name} {
			if index == 1 && name == billingModelName {
				break
			}
			price, ok := model.GetUserModelPrice(info.UserId, userModelName)
			if !ok {
				continue
			}
			unit := rule.Unit
			if expression, configured := billing_setting.GetBillingExpr(userModelName); configured {
				if _, flatImage := billingexpr.ImageUnitPrice(expression, false); flatImage {
					unit = "image"
				}
			}
			if _, legacyPrice := ratio_setting.GetModelPrice(userModelName, false); legacyPrice {
				unit = "request"
				if _, image := info.Request.(*dto.ImageRequest); image {
					unit = "image"
				}
			}
			// A scalar user override cannot represent a pair of token prices.
			if unit == "request" || unit == "image" {
				expression = `tier("user_private", fixed(` + strconv.FormatFloat(price, 'g', -1, 64) + `))`
				if unit == "image" {
					expression += ` * image_count`
				}
				info.BillingModelName = userModelName
				return modelPriceHelperTieredExpression(c, info, userModelName, promptTokens, groupRatioInfo, expression, true)
			}
		}
		if rule.Unit == "second" {
			return hosttypes.PriceData{}, fmt.Errorf("per-second group pricing requires a task usage meter")
		}
		return modelPriceHelperTieredExpression(c, info, name, promptTokens, groupRatioInfo, expression, true)
	}
	billingMode := billing_setting.GetBillingMode(billingModelName)

	var modelPrice float64
	var usePrice bool
	var qualityPriceMatched bool

	if billingMode == billing_setting.BillingModeImageQuality {
		// Image-quality mode is exclusive: never read or fall back to ordinary pricing.
		imageQualityPrice, ok, err := resolveImageQualityPricing(c, info, meta)
		if err != nil {
			return hosttypes.PriceData{}, err
		}
		if !ok {
			return hosttypes.PriceData{}, modelPriceNotConfiguredError(
				c,
				billingModelName,
				info.UserId,
			)
		}

		modelPrice = imageQualityPrice
		usePrice = true
		qualityPriceMatched = true
	} else {
		modelPrice, usePrice = ratio_setting.GetModelPrice(
			billingModelName,
			false,
		)

		if imageQualityPrice, ok, err := resolveImageQualityPricing(c, info, meta); err != nil {
			return hosttypes.PriceData{}, err
		} else if ok {
			modelPrice = imageQualityPrice
			usePrice = true
			qualityPriceMatched = true
		} else if usePrice {
			if userPrice, ok := model.GetUserModelPrice(
				info.UserId,
				billingModelName,
			); ok {
				modelPrice = userPrice
				logger.LogInfo(c, fmt.Sprintf(
					"user private model price applied: userId=%d, model=%s, price=%f",
					info.UserId,
					billingModelName,
					userPrice,
				))
			}
		}
	}

	// A new built-in expression must not displace an existing quality price.
	// Explicit tiered pricing retains the precedence it had before the upgrade.
	_, explicitBillingMode := billing_setting.GetConfiguredBillingMode(billingModelName)
	if billingMode == billing_setting.BillingModeTieredExpr && (!qualityPriceMatched || explicitBillingMode) {
		return modelPriceHelperTiered(c, info, billingModelName, promptTokens, groupRatioInfo)
	}

	var preConsumedQuota int
	var modelRatio float64
	var completionRatio float64
	var cacheRatio float64
	var imageRatio float64
	var cacheCreationRatio float64
	var cacheCreationRatio5m float64
	var cacheCreationRatio1h float64
	var cacheCreationRatio12h float64
	var audioRatio float64
	var audioCompletionRatio float64
	var freeModel bool
	if !usePrice {
		preConsumeMultiplier, err := operation_setting.InputPreConsumeMultiplier()
		if err != nil {
			return hosttypes.PriceData{}, err
		}
		preConsumedTokens := float64(promptTokens) * preConsumeMultiplier
		var success bool
		var matchName string
		modelRatio, success, matchName = ratio_setting.GetModelRatio(billingModelName)
		if !success {
			acceptUnsetRatio := false
			if info.UserSetting.AcceptUnsetRatioModel {
				acceptUnsetRatio = true
			}
			if !acceptUnsetRatio {
				return hosttypes.PriceData{}, modelPriceNotConfiguredError(c, matchName, info.UserId)
			}
		}
		completionRatio = ratio_setting.GetCompletionRatio(billingModelName)
		cacheRatio, _ = ratio_setting.GetCacheRatio(billingModelName)
		cacheCreationRatio, _ = ratio_setting.GetCreateCacheRatio(billingModelName)
		cacheCreationRatio5m = cacheCreationRatio
		// 固定1h和5min缓存写入价格的比例
		cacheCreationRatio1h = cacheCreationRatio * hosttypes.ClaudeCacheCreation1hMultiplier
		cacheCreationRatio12h = cacheCreationRatio * hosttypes.OpenAICacheCreation12hMultiplier
		imageRatio, _ = ratio_setting.GetImageRatio(billingModelName)
		audioRatio = ratio_setting.GetAudioRatio(billingModelName)
		audioCompletionRatio = ratio_setting.GetAudioCompletionRatio(billingModelName)
		ratio := modelRatio * groupRatioInfo.GroupRatio
		quota, err := common.QuotaFromFloatStrict(preConsumedTokens * ratio)
		if err != nil {
			return hosttypes.PriceData{}, err
		}
		preConsumedQuota = quota
		if _, image := info.Request.(*dto.ImageRequest); image {
			info.ImageQuotaBeforeGroup = preConsumedTokens * modelRatio
		}
	} else {
		if meta.ImagePriceRatio != 0 {
			modelPrice = modelPrice * meta.ImagePriceRatio
		}
		if _, image := info.Request.(*dto.ImageRequest); image {
			info.ImageQuotaBeforeGroup = modelPrice * common.QuotaPerUnit
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

	priceData := hosttypes.PriceData{
		FreeModel:             freeModel,
		ModelPrice:            modelPrice,
		ModelRatio:            modelRatio,
		CompletionRatio:       completionRatio,
		GroupRatioInfo:        groupRatioInfo,
		UsePrice:              usePrice,
		CacheRatio:            cacheRatio,
		ImageRatio:            imageRatio,
		AudioRatio:            audioRatio,
		AudioCompletionRatio:  audioCompletionRatio,
		CacheCreationRatio:    cacheCreationRatio,
		CacheCreation5mRatio:  cacheCreationRatio5m,
		CacheCreation1hRatio:  cacheCreationRatio1h,
		CacheCreation12hRatio: cacheCreationRatio12h,
		QuotaToPreConsume:     preConsumedQuota,
	}
	if usePrice {
		for name, ratio := range meta.BillingRatios {
			priceData.AddOtherRatio(name, ratio)
		}
	}
	if request, image := info.Request.(*dto.ImageRequest); image {
		count, err := request.ImageCount(false)
		if err != nil {
			return hosttypes.PriceData{}, err
		}
		if usePrice {
			priceData.AddOtherRatio("n", float64(count))
		}
		if !usePrice {
			quota, err := common.QuotaFromFloatStrict(priceData.ApplyOtherRatiosToFloat(info.ImageQuotaBeforeGroup * groupRatioInfo.GroupRatio))
			if err != nil {
				return hosttypes.PriceData{}, err
			}
			priceData.QuotaToPreConsume = quota
		}
	}
	if usePrice {
		quotaToPreConsume := priceData.ApplyOtherRatiosToFloat(modelPrice * common.QuotaPerUnit * groupRatioInfo.GroupRatio)
		quota, err := common.QuotaFromFloatStrict(quotaToPreConsume)
		if err != nil {
			return hosttypes.PriceData{}, err
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
func ModelPriceHelperPerCall(c *gin.Context, info *relaycommon.RelayInfo) (hosttypes.PriceData, error) {
	groupRatioInfo := HandleGroupRatio(c, info)
	if name, rule, configured := groupModelPricingRule(info.UsingGroup, info.GetOriginModelName()); configured {
		values, err := billing_setting.ParseGroupModelPricingRule(rule)
		if err != nil {
			return hosttypes.PriceData{}, err
		}
		if rule.Unit != "request" {
			return hosttypes.PriceData{}, fmt.Errorf("group pricing unit %s requires a compatible usage meter", rule.Unit)
		}
		price := values.UnitPrice
		if !billing_setting.IsGroupModelPricingForced(info.UsingGroup) {
			if userPrice, ok := model.GetUserModelPrice(info.UserId, name); ok {
				price = userPrice
			}
		}
		quota, err := common.QuotaFromFloatStrict(price * common.QuotaPerUnit * groupRatioInfo.GroupRatio)
		if err != nil {
			return hosttypes.PriceData{}, err
		}
		return hosttypes.PriceData{ModelPrice: price, UsePrice: true, Quota: quota, QuotaToPreConsume: quota,
			FreeModel: !operation_setting.GetQuotaSetting().EnableFreeModelPreConsume && (price == 0 || groupRatioInfo.GroupRatio == 0), GroupRatioInfo: groupRatioInfo}, nil
	}

	modelPrice, success := ratio_setting.GetModelPrice(info.OriginModelName, true)
	usePrice := success
	if billing_setting.GetBillingMode(info.OriginModelName) == billing_setting.BillingModeVideoPerSecond {
		basePrice, ok := billing_setting.GetVideoSecondBasePrice(info.OriginModelName)
		if !ok {
			return hosttypes.PriceData{}, fmt.Errorf("video per-second price not configured for model %s", info.OriginModelName)
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
				return hosttypes.PriceData{}, modelPriceNotConfiguredError(c, matchName, info.UserId)
			}
		}
	}

	var quota int
	freeModel := false

	if usePrice {
		var err error
		quota, err = common.QuotaFromFloatStrict(modelPrice * common.QuotaPerUnit * groupRatioInfo.GroupRatio)
		if err != nil {
			return hosttypes.PriceData{}, err
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
			return hosttypes.PriceData{}, err
		}
		modelPrice = -1
		if !operation_setting.GetQuotaSetting().EnableFreeModelPreConsume {
			if groupRatioInfo.GroupRatio == 0 || modelRatio == 0 {
				quota = 0
				freeModel = true
			}
		}
	}

	priceData := hosttypes.PriceData{
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

// HasPriceOrRatioEntry reports whether name has a configured price, ratio, or
// tiered billing-mode entry after a single wildcard normalization. Self-use
// fallback does not count as a configured ratio.
func HasPriceOrRatioEntry(name string) bool {
	formatted := ratio_setting.FormatMatchingModelName(name)
	if _, ok := ratio_setting.GetModelPrice(formatted, false); ok {
		return true
	}
	if ratio_setting.HasConfiguredModelRatio(formatted) {
		return true
	}
	switch billing_setting.GetBillingMode(formatted) {
	case billing_setting.BillingModeTieredExpr, billing_setting.BillingModeImageQuality, billing_setting.BillingModeVideoPerSecond:
		return true
	default:
		return false
	}
}

func resolveBillingModelName(origin string) string {
	var candidates []string
	if !reasoning.ParseModelModifiers(origin).HasModifiers() {
		candidates = append(candidates, origin)
	}
	candidates = append(candidates, hostreasoning.CanonicalBillingModelNames(origin)...)
	base := hostreasoning.BaseModelName(origin)
	// DeepSeek thinking tails (-none/-low/-max) bill at the base model's price,
	// with or without @ modifiers after them.
	if deepSeekBase, _, _, ok := reasoning.ParseDeepSeekV4ThinkingSuffix(base); ok && !model_setting.ShouldPreserveThinkingSuffix(base) {
		candidates = append(candidates, deepSeekBase)
	}
	candidates = append(candidates, base)

	seen := make(map[string]struct{}, len(candidates))
	matched := ""
	for _, name := range candidates {
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		if HasPriceOrRatioEntry(name) {
			matched = name
			break
		}
	}
	if matched == "" {
		matched = base
	}
	return matched
}

func modelPriceHelperTiered(c *gin.Context, info *relaycommon.RelayInfo, billingModelName string, promptTokens int, groupRatioInfo hosttypes.GroupRatioInfo) (hosttypes.PriceData, error) {
	exprStr, ok := billing_setting.GetBillingExpr(billingModelName)
	if !ok {
		return hosttypes.PriceData{}, fmt.Errorf("model %s is configured as tiered_expr but has no billing expression", billingModelName)
	}
	// A flat per-image private price changes the unit price only. Keep image
	// quantity validation and settlement on the existing frozen expression path.
	if _, flatImage := billingexpr.ImageUnitPrice(exprStr, false); flatImage {
		if price, configured := model.GetUserModelPrice(info.UserId, billingModelName); configured {
			exprStr = `tier("user_private", fixed(` + strconv.FormatFloat(price, 'g', -1, 64) + `)) * image_count`
		}
	}
	return modelPriceHelperTieredExpression(c, info, billingModelName, promptTokens, groupRatioInfo, exprStr, false)
}

func modelPriceHelperTieredExpression(c *gin.Context, info *relaycommon.RelayInfo, billingModelName string, promptTokens int, groupRatioInfo hosttypes.GroupRatioInfo, exprStr string, groupScoped bool) (hosttypes.PriceData, error) {
	exprHash := billingexpr.ExprHashString(exprStr)
	usedVars := billingexpr.UsedVarsByHash(exprStr, exprHash)
	if usedVars["u"] {
		return hosttypes.PriceData{}, errors.New(i18n.T(c, i18n.MsgRelayTaskUsagePriceUnsupported, map[string]any{"Model": billingModelName}))
	}
	fixedPricing := billingexpr.UsesFixedPricingByHash(exprStr, exprHash)
	value, _ := c.Get(jsplugin.ContextKeyRelayEndpoint)
	// A scoped token template is explicitly ordinary pricing; the task path
	// rejects that unit rather than inheriting it as a USD usage expression.
	if endpoint, ok := value.(jsplugin.PinnedEndpoint); ok && !fixedPricing && !groupScoped {
		for _, candidate := range endpoint.Candidates {
			pluginKey := candidate.Plugin.Meta.Key
			if _, overridden := billing_setting.GetPluginBillingExpr(pluginKey, endpoint.Model); overridden {
				continue
			}
			if _, overridden := billing_setting.GetPluginBillingExpr(pluginKey, endpoint.MappedModel); endpoint.MappedModel != "" && overridden {
				continue
			}
			taskBillingModel := endpoint.Model
			if billing_setting.GetBillingMode(taskBillingModel) != billing_setting.BillingModeTieredExpr {
				taskBillingModel = endpoint.MappedModel
			}
			// Task expressions return USD; ordinary expressions return USD *
			// 1M. Even a constant without u() cannot safely serve both contracts.
			if taskBillingModel == billingModelName {
				return hosttypes.PriceData{}, errors.New(i18n.T(c, i18n.MsgRelaySharedTaskPriceNeedsOverride, map[string]any{"Model": billingModelName, "Plugin": pluginKey}))
			}
		}
	}
	if info.RelayFormat == types.RelayFormatOpenAIRealtime && fixedPricing {
		return hosttypes.PriceData{}, fmt.Errorf("fixed pricing is not supported for Realtime requests")
	}

	preConsumeMultiplier, err := operation_setting.InputPreConsumeMultiplier()
	if err != nil {
		return hosttypes.PriceData{}, err
	}

	requestInput, err := ResolveIncomingBillingExprRequestInput(c, info)
	if err != nil {
		return hosttypes.PriceData{}, err
	}
	if usedVars["image_count"] {
		requestInput, err = ResolveImageBillingRequestInput(c, info, requestInput)
		if err != nil {
			return hosttypes.PriceData{}, err
		}
	}

	rawCost, trace, err := billingexpr.RunExprByHashWithRequest(exprStr, exprHash, billingexpr.TokenParams{
		P:   float64(promptTokens),
		C:   0,
		Len: float64(promptTokens),
	}, requestInput)
	if err != nil {
		return hosttypes.PriceData{}, fmt.Errorf("model %s tiered expr run failed: %w", billingModelName, err)
	}

	// Expression coefficients are $/1M tokens prices; convert to quota the same way per-call billing does.
	quotaBeforeGroup := rawCost / 1_000_000 * common.QuotaPerUnit
	// Scale the reservation, preserving the expression's context-length tier
	// and leaving actual settlement and fixed request prices unchanged.
	if trace.BillingUnit != billingexpr.BillingUnitRequest {
		quotaBeforeGroup *= preConsumeMultiplier
	}
	preConsumedQuota, err := billingexpr.QuotaRoundStrict(quotaBeforeGroup * groupRatioInfo.GroupRatio)
	if err != nil {
		return hosttypes.PriceData{}, err
	}

	freeModel := false
	if !operation_setting.GetQuotaSetting().EnableFreeModelPreConsume {
		if groupRatioInfo.GroupRatio == 0 {
			preConsumedQuota = 0
			freeModel = true
		}
	}

	snapshot := &billingexpr.BillingSnapshot{
		EstimatedImageCount:       trace.ImageCount,
		BillingMode:               billing_setting.BillingModeTieredExpr,
		ModelName:                 billingModelName,
		ExprString:                exprStr,
		ExprHash:                  exprHash,
		GroupRatio:                groupRatioInfo.GroupRatio,
		EstimatedPromptTokens:     promptTokens,
		EstimatedCompletionTokens: 0,
		PreConsumeMultiplier:      preConsumeMultiplier,
		EstimatedQuotaBeforeGroup: quotaBeforeGroup,
		EstimatedQuotaAfterGroup:  preConsumedQuota,
		EstimatedTier:             trace.MatchedTier,
		EstimatedBillingUnit:      trace.BillingUnit,
		EstimatedFixedPrice:       trace.FixedPrice,
		QuotaPerUnit:              common.QuotaPerUnit,
		ExprVersion:               billingexpr.ExprVersion(exprStr),
	}
	info.TieredBillingSnapshot = snapshot
	info.BillingRequestInput = &requestInput

	priceData := hosttypes.PriceData{
		FreeModel:         freeModel,
		GroupRatioInfo:    groupRatioInfo,
		QuotaToPreConsume: preConsumedQuota,
	}

	logger.LogDebug(c, "model_price_helper_tiered result: model=%s preConsume=%d quotaBeforeGroup=%.2f groupRatio=%.2f tier=%s", billingModelName, preConsumedQuota, quotaBeforeGroup, groupRatioInfo.GroupRatio, trace.MatchedTier)

	info.PriceData = priceData
	return priceData, nil
}
