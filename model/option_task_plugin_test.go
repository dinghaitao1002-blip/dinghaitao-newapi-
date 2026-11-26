package model

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestGroupModelPricingOptionPersistenceAndValidation(t *testing.T) {
	truncateTables(t)
	require.NoError(t, DB.AutoMigrate(&Option{}))
	previousOptions := common.OptionMap
	previousPricing := config.GlobalConfig.ExportAllConfigs()[billing_setting.GroupModelPricingOption]
	common.OptionMap = map[string]string{}
	t.Cleanup(func() {
		common.OptionMap = previousOptions
		config.UpdateConfigFromMap(config.GlobalConfig.Get("billing_setting"), map[string]string{"group_model_pricing": previousPricing})
		require.NoError(t, DB.Where(Option{Key: billing_setting.GroupModelPricingOption}).Delete(&Option{}).Error)
	})
	rules := map[string]map[string]billing_setting.GroupModelPricingRule{
		"retail": {
			"request-model": {Unit: "request", Expression: `tier("group_model", fixed(0))`, Enabled: true},
			"image-model":   {Unit: "image", Expression: `tier("group_model", fixed(0.02)) * image_count`, Enabled: true},
			"video-model":   {Unit: "second", Expression: `tier("group_model", u("seconds") * 5e-2)`, Enabled: true},
			"token-model":   {Unit: "token", Expression: `tier("group_model", p * 2 + c * 8)`, Enabled: true},
		},
	}
	encoded, err := common.Marshal(rules)
	require.NoError(t, err)
	validJSON := string(encoded)
	require.NoError(t, UpdateOption(billing_setting.GroupModelPricingOption, validJSON))
	var persisted Option
	require.NoError(t, DB.Where(Option{Key: billing_setting.GroupModelPricingOption}).First(&persisted).Error)
	assert.Equal(t, validJSON, persisted.Value)
	for _, tc := range []struct {
		model string
		want  billing_setting.GroupModelPricingValues
	}{
		{"request-model", billing_setting.GroupModelPricingValues{UnitPrice: 0}},
		{"image-model", billing_setting.GroupModelPricingValues{UnitPrice: 0.02}},
		{"video-model", billing_setting.GroupModelPricingValues{UnitPrice: 0.05}},
		{"token-model", billing_setting.GroupModelPricingValues{InputPrice: 2, OutputPrice: 8}},
	} {
		rule, ok := billing_setting.GetGroupModelPricingRule("retail", tc.model)
		require.True(t, ok, "explicit zero must remain configured")
		values, err := billing_setting.ParseGroupModelPricingRule(rule)
		require.NoError(t, err)
		assert.Equal(t, tc.want, values)
	}
	_, found := billing_setting.GetGroupModelPricingRule("other-group", "image-model")
	assert.False(t, found, "group rules must not leak into other groups")
	_, found = billing_setting.GetGroupModelPricingRule("retail", "other-model")
	assert.False(t, found, "model names require an exact match")
	snapshot := billing_setting.GetGroupModelPricingCopy()
	delete(snapshot["retail"], "image-model")
	_, found = billing_setting.GetGroupModelPricingRule("retail", "image-model")
	assert.True(t, found, "editing a returned snapshot must not mutate serving prices")

	// Exercise the same publication boundary used when loading persisted options.
	config.UpdateConfigFromMap(config.GlobalConfig.Get("billing_setting"), map[string]string{"group_model_pricing": "{}"})
	_, found = billing_setting.GetGroupModelPricingRule("retail", "image-model")
	require.False(t, found)
	require.NoError(t, updateOptionMap(persisted.Key, persisted.Value))
	_, found = billing_setting.GetGroupModelPricingRule("retail", "image-model")
	require.True(t, found)

	for _, invalid := range []string{
		`null`, `[]`, `{"retail":null}`, `{"retail":{"model":null}}`,
		`{"retail":{"model":{"unit":"request","expression":"tier(\"group_model\",fixed(1))"}}}`,
		`{"retail":{"model":{"unit":"request","expression":"tier(\"group_model\",fixed(1))","enabled":null}}}`,
		`{"retail":{"model":{"unit":"request","expression":"tier(\"group_model\",fixed(1))","enabled":"true"}}}`,
		`{"retail":{"model":{"unit":"request","expression":"tier(\"group_model\",fixed(1))","enabled":true,"price":1}}}`,
		`{" ":{}}`, `{"retail":{" ":{}}}`,
	} {
		t.Run(invalid, func(t *testing.T) {
			require.Error(t, UpdateOption(billing_setting.GroupModelPricingOption, invalid))
			require.Error(t, updateOptionMap(billing_setting.GroupModelPricingOption, invalid))
			require.NoError(t, DB.Where(Option{Key: persisted.Key}).First(&persisted).Error)
			assert.Equal(t, validJSON, persisted.Value, "invalid writes must preserve the stored configuration")
			assert.Equal(t, validJSON, common.OptionMap[billing_setting.GroupModelPricingOption])
			_, found := billing_setting.GetGroupModelPricingRule("retail", "image-model")
			assert.True(t, found, "invalid startup data must not replace the serving configuration")
		})
	}
	for _, invalid := range []billing_setting.GroupModelPricingRule{
		{Unit: "unknown", Expression: `tier("group_model", fixed(1))`},
		{Unit: "request", Expression: `tier("group_model", fixed(-1))`},
		{Unit: "request", Expression: `tier("group_model", fixed(1e309))`},
		{Unit: "request", Expression: `tier("group_model", fixed(1e308))`},
		{Unit: "request", Expression: `tier("other", fixed(1))`},
		{Unit: "request", Expression: `true ? tier("group_model", fixed(1)) : tier("hidden", fixed(2))`},
		{Unit: "image", Expression: `tier("group_model", fixed(1))`},
		{Unit: "image", Expression: `tier("group_model", fixed(1)) * image_count * 2`},
		{Unit: "second", Expression: `tier("group_model", u("image_count") * 1)`},
		{Unit: "second", Expression: `tier("group_model", u("seconds") * 1 + 1)`},
		{Unit: "token", Expression: `tier("group_model", p * 2 + c * -1)`},
		{Unit: "token", Expression: `tier("group_model", p * 2 + c * 8 + cr * 1)`},
	} {
		t.Run(invalid.Unit+":"+invalid.Expression, func(t *testing.T) {
			_, err := billing_setting.ParseGroupModelPricingRule(invalid)
			require.Error(t, err)
			bad, err := common.Marshal(map[string]map[string]billing_setting.GroupModelPricingRule{"retail": {"invalid-model": invalid}})
			require.NoError(t, err)
			require.Error(t, UpdateOption(billing_setting.GroupModelPricingOption, string(bad)), "disabled rules must also validate")
			assert.Equal(t, validJSON, common.OptionMap[billing_setting.GroupModelPricingOption])
		})
	}

	t.Run("failed database write keeps serving prices", func(t *testing.T) {
		const callback = "test:reject_group_model_pricing_update"
		require.NoError(t, DB.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
			if option, ok := tx.Statement.Dest.(*Option); ok && option.Key == billing_setting.GroupModelPricingOption {
				tx.AddError(errors.New("forced group pricing persistence failure"))
			}
		}))
		defer func() { require.NoError(t, DB.Callback().Update().Remove(callback)) }()
		require.ErrorContains(t, UpdateOption(billing_setting.GroupModelPricingOption, "{}"), "forced group pricing persistence failure")
		require.NoError(t, DB.Where(Option{Key: persisted.Key}).First(&persisted).Error)
		assert.Equal(t, validJSON, persisted.Value)
		assert.Equal(t, validJSON, common.OptionMap[billing_setting.GroupModelPricingOption])
		_, found := billing_setting.GetGroupModelPricingRule("retail", "image-model")
		assert.True(t, found)
	})

	rules["retail"]["image-model"] = billing_setting.GroupModelPricingRule{Unit: "image", Expression: `tier("group_model", fixed(0)) * image_count`, Enabled: false}
	encoded, err = common.Marshal(rules)
	require.NoError(t, err)
	require.NoError(t, UpdateOption(billing_setting.GroupModelPricingOption, string(encoded)))
	_, found = billing_setting.GetGroupModelPricingRule("retail", "image-model")
	assert.False(t, found, "disabled zero rules must fall back instead of granting free requests")
	delete(rules["retail"], "video-model")
	encoded, err = common.Marshal(rules)
	require.NoError(t, err)
	require.NoError(t, UpdateOption(billing_setting.GroupModelPricingOption, string(encoded)))
	_, found = billing_setting.GetGroupModelPricingRule("retail", "video-model")
	assert.False(t, found, "replacing the map must remove deleted model rules")
	require.NoError(t, UpdateOption(billing_setting.GroupModelPricingOption, "{}"))
	_, found = billing_setting.GetGroupModelPricingRule("retail", "request-model")
	assert.False(t, found, "deleting a group must clear its explicit zero rule")
}

func TestGroupModelPricingForceOptionPersistenceAndValidation(t *testing.T) {
	truncateTables(t)
	require.NoError(t, DB.AutoMigrate(&Option{}))
	previousOptions := common.OptionMap
	previous := config.GlobalConfig.ExportAllConfigs()[billing_setting.GroupModelPricingForceOption]
	common.OptionMap = map[string]string{}
	t.Cleanup(func() {
		common.OptionMap = previousOptions
		config.UpdateConfigFromMap(config.GlobalConfig.Get("billing_setting"), map[string]string{"group_model_pricing_force": previous})
		require.NoError(t, DB.Where(Option{Key: billing_setting.GroupModelPricingForceOption}).Delete(&Option{}).Error)
	})
	const validJSON = `{"香蕉提升成本分组":true,"other":false}`
	require.NoError(t, UpdateOption(billing_setting.GroupModelPricingForceOption, validJSON))
	var persisted Option
	require.NoError(t, DB.Where(Option{Key: billing_setting.GroupModelPricingForceOption}).First(&persisted).Error)
	assert.Equal(t, validJSON, persisted.Value)
	assert.True(t, billing_setting.IsGroupModelPricingForced("香蕉提升成本分组"))
	assert.False(t, billing_setting.IsGroupModelPricingForced("other"))
	assert.False(t, billing_setting.IsGroupModelPricingForced("unconfigured"))
	config.UpdateConfigFromMap(config.GlobalConfig.Get("billing_setting"), map[string]string{"group_model_pricing_force": `{}`})
	assert.False(t, billing_setting.IsGroupModelPricingForced("香蕉提升成本分组"))
	require.NoError(t, updateOptionMap(persisted.Key, persisted.Value))
	assert.True(t, billing_setting.IsGroupModelPricingForced("香蕉提升成本分组"))
	for _, invalid := range []string{`null`, `[]`, `{"retail":null}`, `{"retail":1}`, `{"retail":"true"}`, `{"":true}`, `{" retail":true}`, `{"retail":{}}`} {
		t.Run(invalid, func(t *testing.T) {
			require.Error(t, UpdateOption(billing_setting.GroupModelPricingForceOption, invalid))
			require.Error(t, updateOptionMap(billing_setting.GroupModelPricingForceOption, invalid))
			require.NoError(t, DB.Where(Option{Key: persisted.Key}).First(&persisted).Error)
			assert.Equal(t, validJSON, persisted.Value)
			assert.Equal(t, validJSON, common.OptionMap[billing_setting.GroupModelPricingForceOption])
			assert.True(t, billing_setting.IsGroupModelPricingForced("香蕉提升成本分组"))
		})
	}
	t.Run("failed persistence retains old priority", func(t *testing.T) {
		const callback = "test:reject_group_price_priority_update"
		require.NoError(t, DB.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
			if option, ok := tx.Statement.Dest.(*Option); ok && option.Key == billing_setting.GroupModelPricingForceOption {
				tx.AddError(errors.New("forced group priority persistence failure"))
			}
		}))
		defer func() { require.NoError(t, DB.Callback().Update().Remove(callback)) }()
		require.ErrorContains(t, UpdateOption(billing_setting.GroupModelPricingForceOption, `{}`), "forced group priority persistence failure")
		require.NoError(t, DB.Where(Option{Key: persisted.Key}).First(&persisted).Error)
		assert.Equal(t, validJSON, persisted.Value)
		assert.True(t, billing_setting.IsGroupModelPricingForced("香蕉提升成本分组"))
	})
	require.NoError(t, UpdateOptionsBulk(map[string]string{billing_setting.GroupModelPricingForceOption: `{}`}))
	assert.False(t, billing_setting.IsGroupModelPricingForced("香蕉提升成本分组"))
}

// The existing model TestMain selects a real SQLite, MySQL or PostgreSQL
// database. Run this contract on each engine; Redis is optional for local runs.
func TestModelConcurrencyOptionAndTaskRecovery(t *testing.T) {
	truncateTables(t)
	require.NoError(t, DB.AutoMigrate(&Option{}))
	originalConfig := setting.ModelConcurrencyConfig2JSONString()
	originalMap := common.OptionMap
	originalRedisEnabled, originalRedis := common.RedisEnabled, common.RDB
	common.OptionMap = map[string]string{}
	t.Cleanup(func() {
		common.RedisEnabled, common.RDB = originalRedisEnabled, originalRedis
		common.OptionMap = originalMap
		require.NoError(t, setting.UpdateModelConcurrencyConfigByJSONString(originalConfig))
		require.NoError(t, DB.Where(Option{Key: common.ModelConcurrencyOptionKey}).Delete(&Option{}).Error)
	})

	storages := []string{"memory"}
	if os.Getenv("NEWAPI_TEST_CONCURRENCY_REDIS_ADDR") != "" {
		storages = append(storages, "redis")
	}
	for _, storage := range storages {
		t.Run(storage, func(t *testing.T) {
			common.RedisEnabled = storage == "redis"
			if common.RedisEnabled {
				client := redis.NewClient(&redis.Options{Addr: os.Getenv("NEWAPI_TEST_CONCURRENCY_REDIS_ADDR"), DB: 15})
				require.NoError(t, client.Ping(context.Background()).Err())
				common.RDB = client
				t.Cleanup(func() { require.NoError(t, client.Close()) })
			}
			modelName := "concurrency-recovery-" + storage + "-" + string(common.MainDatabaseType())
			config := common.ModelConcurrencyConfig{Enabled: true, Rules: []common.ModelConcurrencyRule{{
				Model: modelName, Enabled: true, MaxConcurrent: 2, DefaultUserLimit: 1, OtherUsersLimit: 1,
				UserLimits: []common.ModelConcurrencyUserLimit{{UserID: 101, MaxConcurrent: 1}},
			}}}
			encoded, err := common.Marshal(config)
			require.NoError(t, err)
			require.NoError(t, UpdateOption(common.ModelConcurrencyOptionKey, string(encoded)))
			var option Option
			require.NoError(t, DB.Where(Option{Key: common.ModelConcurrencyOptionKey}).First(&option).Error)
			require.NoError(t, setting.UpdateModelConcurrencyConfigByJSONString(`{"enabled":false,"rules":[]}`))
			require.NoError(t, updateOptionMap(option.Key, option.Value))
			assert.Equal(t, config, setting.GetModelConcurrencyConfig(), "persisted configuration must restore after restart")
			require.Error(t, UpdateOption(common.ModelConcurrencyOptionKey, `{"enabled":true,"rules":[{"model":"bad","max_concurrent":-1}]}`))
			assert.Equal(t, config, setting.GetModelConcurrencyConfig(), "invalid saves must leave effective configuration unchanged")

			pluginTask := &Task{UserId: 101, Status: TaskStatusSubmitted, Progress: "0%", Properties: Properties{OriginModelName: modelName + "@temperature:0.2"}}
			require.NoError(t, DB.Create(pluginTask).Error)
			imageID, err := GenerateImageAsyncTaskID()
			require.NoError(t, err)
			imageTask := &ImageAsyncTask{ID: imageID, UserID: 102, Model: modelName, Status: ImageAsyncStatusQueued}
			require.NoError(t, DB.Create(imageTask).Error)
			t.Cleanup(func() {
				ReleaseTaskModelConcurrency(TaskModelConcurrencyIdentity(pluginTask))
				ReleaseTaskModelConcurrency(ImageTaskModelConcurrencyIdentity(imageTask))
			})
			ctx := context.Background()
			require.NoError(t, ReconcileModelConcurrencyTasks(ctx, config))
			require.NoError(t, ReconcileModelConcurrencyTasks(ctx, config))
			usage, err := common.GetModelConcurrencyUsage(ctx, config.Rules)
			require.NoError(t, err)
			require.Len(t, usage, 1)
			assert.Equal(t, 2, usage[0].Total, "recovery must count each accepted task once")
			assert.Equal(t, 1, usage[0].OtherUsers)
			_, err = common.AcquireModelConcurrency(ctx, modelName, 103, config.Rules[0])
			require.ErrorIs(t, err, common.ErrModelConcurrencyExceeded)

			config.Rules = append(config.Rules, common.ModelConcurrencyRule{Model: pluginTask.Properties.OriginModelName, Enabled: true, MaxConcurrent: 1})
			require.NoError(t, ReconcileModelConcurrencyTasks(ctx, config))
			usage, err = common.GetModelConcurrencyUsage(ctx, config.Rules)
			require.NoError(t, err)
			assert.Equal(t, 1, usage[0].Total, "enabling a specific rule must move the existing task out of its base bucket")
			assert.Equal(t, 1, usage[1].Total)
			config.Rules = config.Rules[:1]
			require.NoError(t, ReconcileModelConcurrencyTasks(ctx, config))
			usage, err = common.GetModelConcurrencyUsage(ctx, config.Rules)
			require.NoError(t, err)
			assert.Equal(t, 2, usage[0].Total, "removing the specific rule must restore accounting in its base bucket")

			config.Enabled = false
			require.NoError(t, ReconcileModelConcurrencyTasks(ctx, config))
			usage, err = common.GetModelConcurrencyUsage(ctx, config.Rules)
			require.NoError(t, err)
			assert.Equal(t, 2, usage[0].Total, "disabling admission must retain in-flight task accounting")
			pluginTask.Status, pluginTask.Progress = TaskStatusSuccess, "100%"
			require.NoError(t, pluginTask.Update())
			usage, err = common.GetModelConcurrencyUsage(ctx, config.Rules)
			require.NoError(t, err)
			assert.Equal(t, 1, usage[0].Total, "successful task must release immediately")
			require.NoError(t, DB.Delete(imageTask).Error)
			require.NoError(t, ReconcileModelConcurrencyTasks(ctx, config))
			usage, err = common.GetModelConcurrencyUsage(ctx, config.Rules)
			require.NoError(t, err)
			assert.Zero(t, usage[0].Total, "deleted tasks must be reclaimed even while the rule is disabled")
		})
	}
}

func TestTaskPluginEnabledOptionUpdatesRegistry(t *testing.T) {
	originalEnabled := constant.TaskPluginEnabled
	originalMap := common.OptionMap
	common.OptionMap = map[string]string{}
	const key = "option-master-off"
	source := `
export const meta = {apiVersion: 1, key: "option-master-off", name: "Option Master", version: "1.0.0", author: {name: "Test"}, models: ["option-master-model"], fetchMode: "per_task"};
export function buildSubmitRequest() { return {}; }
export function parseSubmitResponse() { return {}; }
export function buildQueryRequest() { return {}; }
export function parseTaskResult() { return {}; }
`
	_, err := jsplugin.DefaultRegistry.RegisterFactory(source, jsplugin.Options{})
	require.NoError(t, err)
	t.Cleanup(func() {
		constant.TaskPluginEnabled = originalEnabled
		jsplugin.DefaultRegistry.SetEnabled(originalEnabled)
		common.OptionMap = originalMap
	})

	_, ok := jsplugin.DefaultRegistry.Get(key)
	require.True(t, ok)

	require.NoError(t, updateOptionMap("TaskPluginEnabled", "false"))

	assert.False(t, constant.TaskPluginEnabled)
	assert.Equal(t, "false", common.OptionMap["TaskPluginEnabled"])
	_, ok = jsplugin.DefaultRegistry.Get(key)
	assert.False(t, ok)

	require.NoError(t, updateOptionMap("TaskPluginEnabled", "true"))
	_, ok = jsplugin.DefaultRegistry.Get(key)
	assert.True(t, ok)
}

func TestTaskPluginDisabledFactoryKeysOptionUpdatesRegistry(t *testing.T) {
	originalMap := common.OptionMap
	common.OptionMap = map[string]string{}
	const key = "option-factory-off"
	source := `
export const meta = {apiVersion: 1, key: "option-factory-off", name: "Option Factory", version: "1.0.0", author: {name: "Test"}, models: ["option-factory-model"], fetchMode: "per_task"};
export function buildSubmitRequest() { return {}; }
export function parseSubmitResponse() { return {}; }
export function buildQueryRequest() { return {}; }
export function parseTaskResult() { return {}; }
`
	_, err := jsplugin.DefaultRegistry.RegisterFactory(source, jsplugin.Options{})
	require.NoError(t, err)
	t.Cleanup(func() {
		jsplugin.DefaultRegistry.SetDisabledFactoryKeys(nil)
		common.OptionMap = originalMap
	})

	_, ok := jsplugin.DefaultRegistry.Get(key)
	require.True(t, ok)

	require.NoError(t, updateOptionMap(setting.TaskPluginDisabledFactoryKeysKey, `["option-factory-off"]`))

	assert.Equal(t, `["option-factory-off"]`, common.OptionMap[setting.TaskPluginDisabledFactoryKeysKey])
	_, ok = jsplugin.DefaultRegistry.Get(key)
	assert.False(t, ok)
	assert.Equal(t, []string{key}, jsplugin.DefaultRegistry.Snapshot().DisabledFactory)
}
