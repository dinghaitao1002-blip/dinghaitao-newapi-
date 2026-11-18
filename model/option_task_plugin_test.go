package model

import (
	"context"
	"os"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/setting"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
