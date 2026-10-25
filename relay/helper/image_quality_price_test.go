package helper

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestImageQualityPricingPrecedesOnlyBuiltinTieredDefault(t *testing.T) {
	gin.SetMode(gin.TestMode)
	oldDB := model.DB
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	pool.SetMaxOpenConns(1)
	model.DB = db
	t.Cleanup(func() {
		model.DB = oldDB
		_ = pool.Close()
	})
	require.NoError(t, db.AutoMigrate(&model.UserModelPrice{}, &model.ModelImageQualityPrice{}, &model.UserModelImageQualityPrice{}))
	settings := config.GlobalConfig.Get("billing_setting").(*billing_setting.BillingSetting)
	savedSettings := *settings
	savedRatios, savedPrices := ratio_setting.ModelRatio2JSONString(), ratio_setting.ModelPrice2JSONString()
	t.Cleanup(func() {
		*settings = savedSettings
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(savedRatios))
		require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(savedPrices))
	})
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{}`))
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{}`))
	const modelName = "gpt-image-2"
	const userID = 4242
	const explicitExpression = `tier("explicit", fixed(0.7))`
	for _, tc := range []struct {
		name, mode, source string
		public, private    bool
		wantPrice          float64
		wantTiered         bool
	}{
		{name: "public quality overrides builtin", public: true, source: "model_image_quality_public", wantPrice: 0.2},
		{name: "private quality overrides public and builtin", public: true, private: true, source: "user_image_quality_private", wantPrice: 0.1},
		{name: "explicit ratio retains quality price", mode: billing_setting.BillingModeRatio, public: true, source: "model_image_quality_public", wantPrice: 0.2},
		{name: "explicit quality retains private price", mode: billing_setting.BillingModeImageQuality, public: true, private: true, source: "user_image_quality_private", wantPrice: 0.1},
		{name: "explicit tiered still precedes public quality", mode: billing_setting.BillingModeTieredExpr, public: true, wantTiered: true},
		{name: "explicit tiered still precedes private quality", mode: billing_setting.BillingModeTieredExpr, public: true, private: true, wantTiered: true},
		{name: "builtin remains fallback without quality price", wantTiered: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, db.Where("1 = 1").Delete(&model.ModelImageQualityPrice{}).Error)
			require.NoError(t, db.Where("1 = 1").Delete(&model.UserModelImageQualityPrice{}).Error)
			*settings = billing_setting.BillingSetting{BillingMode: map[string]string{}, BillingExpr: map[string]string{}}
			if tc.mode != "" {
				settings.BillingMode[modelName] = tc.mode
			}
			if tc.mode == billing_setting.BillingModeTieredExpr {
				settings.BillingExpr[modelName] = explicitExpression
			}
			if tc.public {
				require.NoError(t, model.SaveModelImageQualityPrices(modelName, fullImageQualityPriceMatrix(0.2), true))
			}
			if tc.private {
				require.NoError(t, model.SaveUserModelImageQualityPrices(userID, modelName, fullImageQualityPriceMatrix(0.1), true))
			}
			ctx, info, meta := newImageQualityPriceTestContext(modelName, userID, "high", "2048x2048", 2)
			priceData, err := ModelPriceHelper(ctx, info, 1000, meta)
			require.NoError(t, err)
			if tc.wantTiered {
				require.NotNil(t, info.TieredBillingSnapshot)
				require.False(t, priceData.UsePrice)
				if tc.mode == billing_setting.BillingModeTieredExpr {
					require.Equal(t, explicitExpression, info.TieredBillingSnapshot.ExprString)
					expected, err := common.QuotaRoundStrict(0.7 * common.QuotaPerUnit * priceData.GroupRatioInfo.GroupRatio)
					require.NoError(t, err)
					require.Equal(t, expected, priceData.QuotaToPreConsume)
				}
				return
			}
			require.Nil(t, info.TieredBillingSnapshot)
			require.True(t, priceData.UsePrice)
			require.Equal(t, tc.wantPrice, priceData.ModelPrice)
			require.Equal(t, tc.source, info.ImageQualityPriceSource)
			require.Zero(t, meta.ImagePriceRatio)
			require.Equal(t, float64(2), priceData.OtherRatioMultiplier())
			expected, err := common.QuotaFromFloatStrict(tc.wantPrice * common.QuotaPerUnit * priceData.GroupRatioInfo.GroupRatio * 2)
			require.NoError(t, err)
			require.Equal(t, expected, priceData.QuotaToPreConsume)
		})
	}
}

func fullImageQualityPriceMatrix(base float64) []model.ImageQualityPriceItem {
	qualities := []string{"low", "medium", "high"}
	resolutions := []string{"1k", "2k", "4k"}
	items := make([]model.ImageQualityPriceItem, 0, 9)
	for _, quality := range qualities {
		for _, resolution := range resolutions {
			items = append(items, model.ImageQualityPriceItem{
				Quality:    quality,
				Resolution: resolution,
				Price:      base,
				Enabled:    true,
			})
		}
	}
	return items
}

func newImageQualityPriceTestContext(
	modelName string,
	userID int,
	quality string,
	size string,
	n uint,
) (*gin.Context, *relaycommon.RelayInfo, *types.TokenCountMeta) {
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
	ctx.Set("group", "default")

	request := &dto.ImageRequest{
		Model:   modelName,
		Prompt:  "test image",
		N:       &n,
		Quality: quality,
		Size:    size,
	}
	meta := &types.TokenCountMeta{
		ImagePriceRatio: 9,
		BillingRatios:   map[string]float64{"n": float64(n)},
	}
	info := &relaycommon.RelayInfo{
		UserId:          userID,
		OriginModelName: modelName,
		UserGroup:       "default",
		UsingGroup:      "default",
		Request:         request,
	}
	return ctx, info, meta
}

func TestImageQualityPricingPriorityAndFallback(t *testing.T) {
	gin.SetMode(gin.TestMode)

	oldDB := model.DB
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() {
		model.DB = oldDB
		_ = sqlDB.Close()
	})
	model.DB = db

	require.NoError(t, db.AutoMigrate(
		&model.UserModelPrice{},
		&model.ModelImageQualityPrice{},
		&model.UserModelImageQualityPrice{},
	))

	savedModelPrices := ratio_setting.ModelPrice2JSONString()
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(savedModelPrices))
	})

	const (
		modelName = "quality-priority-image-model"
		userID    = 4242
	)
	priceJSON, err := common.Marshal(map[string]float64{modelName: 0.8})
	require.NoError(t, err)
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(string(priceJSON)))

	resetPrices := func(t *testing.T) {
		t.Helper()
		require.NoError(t, db.Where("1 = 1").Delete(&model.UserModelPrice{}).Error)
		require.NoError(t, db.Where("1 = 1").Delete(&model.ModelImageQualityPrice{}).Error)
		require.NoError(t, db.Where("1 = 1").Delete(&model.UserModelImageQualityPrice{}).Error)
	}

	t.Run("user quality private price wins over public quality price", func(t *testing.T) {
		resetPrices(t)
		require.NoError(t, model.SaveModelImageQualityPrices(
			modelName,
			fullImageQualityPriceMatrix(2),
			true,
		))
		require.NoError(t, model.SaveUserModelImageQualityPrices(
			userID,
			modelName,
			fullImageQualityPriceMatrix(3),
			true,
		))

		ctx, info, meta := newImageQualityPriceTestContext(
			modelName, userID, "high", "2048x2048", 2,
		)
		priceData, err := ModelPriceHelper(ctx, info, 0, meta)

		require.NoError(t, err)
		require.True(t, priceData.UsePrice)
		require.Equal(t, float64(3), priceData.ModelPrice)
		require.Equal(t, "user_image_quality_private", info.ImageQualityPriceSource)
		require.Equal(t, "high", info.ImageQuality)
		require.Equal(t, "2k", info.ImageResolution)
		require.Equal(t, float64(3), info.ImageQualityUnitPrice)
		require.Equal(t, float64(2), priceData.OtherRatioMultiplier())
		require.Zero(t, meta.ImagePriceRatio)

		expected, err := common.QuotaFromFloatStrict(
			3 * float64(common.QuotaPerUnit) *
				priceData.GroupRatioInfo.GroupRatio * 2,
		)
		require.NoError(t, err)
		require.Equal(t, expected, priceData.QuotaToPreConsume)
	})

	t.Run("public quality price wins over ordinary user private price", func(t *testing.T) {
		resetPrices(t)
		require.NoError(t, model.UpsertUserModelPrice(userID, modelName, 1, true))
		require.NoError(t, model.SaveModelImageQualityPrices(
			modelName,
			fullImageQualityPriceMatrix(2),
			true,
		))

		ctx, info, meta := newImageQualityPriceTestContext(
			modelName, userID, "medium", "1024x1024", 1,
		)
		priceData, err := ModelPriceHelper(ctx, info, 0, meta)

		require.NoError(t, err)
		require.Equal(t, float64(2), priceData.ModelPrice)
		require.Equal(t, "model_image_quality_public", info.ImageQualityPriceSource)
		require.Equal(t, "medium", info.ImageQuality)
		require.Equal(t, "1k", info.ImageResolution)
	})

	t.Run("ordinary user private price is used when quality pricing is absent", func(t *testing.T) {
		resetPrices(t)
		require.NoError(t, model.UpsertUserModelPrice(userID, modelName, 1.25, true))

		ctx, info, meta := newImageQualityPriceTestContext(
			modelName, userID, "high", "2048x2048", 1,
		)
		// 本用例只验证普通用户密价回退；不叠加旧图片尺寸倍率。
		meta.ImagePriceRatio = 0
		priceData, err := ModelPriceHelper(ctx, info, 0, meta)

		require.NoError(t, err)
		require.Equal(t, float64(1.25), priceData.ModelPrice)
		require.Empty(t, info.ImageQualityPriceSource)
	})

	t.Run("unsupported size still falls back when quality pricing is absent", func(t *testing.T) {
		resetPrices(t)
		require.NoError(t, model.UpsertUserModelPrice(userID, modelName, 1.5, true))

		ctx, info, meta := newImageQualityPriceTestContext(
			modelName, userID, "high", "unsupported-size", 1,
		)
		// 未配置品质价格时，不支持的尺寸应能回退；本用例不测试旧图片倍率。
		meta.ImagePriceRatio = 0
		priceData, err := ModelPriceHelper(ctx, info, 0, meta)

		require.NoError(t, err)
		require.Equal(t, float64(1.5), priceData.ModelPrice)
		require.Empty(t, info.ImageQualityPriceSource)
	})

	t.Run("configured but incomplete public quality price rejects missing combination", func(t *testing.T) {
		resetPrices(t)
		require.NoError(t, db.Create(&model.ModelImageQualityPrice{
			ModelName:  modelName,
			Quality:    "low",
			Resolution: "1k",
			Price:      0.5,
			Enabled:    true,
		}).Error)

		ctx, info, meta := newImageQualityPriceTestContext(
			modelName, userID, "high", "2048x2048", 1,
		)
		_, err := ModelPriceHelper(ctx, info, 0, meta)

		require.Error(t, err)
		require.Contains(t, err.Error(), "model image quality price not configured")
	})
}
