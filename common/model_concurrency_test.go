package common

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestModelConcurrencyConfigValidation(t *testing.T) {
	valid := `{"enabled":true,"rules":[{"model":"gemini-3-pro-image-preview","enabled":true,"max_concurrent":10,"default_user_limit":2,"other_users_limit":3,"user_limits":[{"user_id":42,"max_concurrent":8}]}]}`
	config, err := ParseModelConcurrencyConfig(valid)
	require.NoError(t, err)
	rule, matched := config.Rule("gemini-3-pro-image-preview")
	require.True(t, matched)
	assert.Equal(t, 8, rule.UserLimits[0].MaxConcurrent)
	_, matched = config.Rule("another-model")
	assert.False(t, matched)
	for _, raw := range []string{
		`null`, `[]`, `{"rules":[{"group":null,"model":"a"}]}`, `{"rules":[{"group":" g ","model":"a"}]}`, `{"rules":[{"group":"g","model":"a"},{"group":"g","model":"a"}]}`, `{"rules":[{"model":""}]}`,
		`{"rules":[{"model":"a","max_concurrent":-1}]}`,
		`{"rules":[{"model":"a","max_concurrent":1.5}]}`,
		`{"rules":[{"model":"a","max_concurrent":1000001}]}`,
		`{"rules":[{"model":"a"},{"model":"a"}]}`,
		`{"rules":[{"model":"a","user_limits":[{"user_id":0}]}]}`,
		`{"rules":[{"model":"a","user_limits":[{"user_id":1,"max_concurrent":-1}]}]}`,
		`{"rules":[{"model":"a","user_limits":[{"user_id":1},{"user_id":1}]}]}`,
		`{"rules":[{"model":"a","max_concurent":10}]}`,
		`{"rules":[{"model":"a","user_limits":[{"user_id":1,"limit":10}]}]}`,
		`{"enable":true}`,
		`{"enabled":null}`,
		`{"rules":[{"model":"a","max_concurrent":null}]}`,
		`{"rules":[{"model":"a","user_limits":[{"user_id":1,"max_concurrent":null}]}]}`,
	} {
		_, err := ParseModelConcurrencyConfig(raw)
		assert.Error(t, err, raw)
	}
	scoped, err := ParseModelConcurrencyConfig(`{"enabled":true,"rules":[{"group":"vip","model":"a","enabled":true},{"group":"normal","model":"a","enabled":true},{"model":"a","enabled":true}]}`)
	require.NoError(t, err)
	require.Len(t, scoped.Rules, 3)
	rule, matched = scoped.Rule("a", "vip")
	require.True(t, matched)
	assert.Equal(t, "vip", rule.Group)
	config, err = ParseModelConcurrencyConfig(`{"enabled":false,"rules":[{"model":"a","enabled":true}]}`)
	require.NoError(t, err)
	_, matched = config.Rule("a")
	assert.False(t, matched)
	config.Enabled = true
	_, matched = config.Rule("a")
	assert.True(t, matched, "zero limits are explicitly unlimited")
}

func TestModelConcurrencyAdmissionAndLifecycle(t *testing.T) {
	for _, backend := range []string{"memory", "redis"} {
		t.Run(backend, func(t *testing.T) {
			ctx := context.Background()
			clock := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
			var server *miniredis.Miniredis
			var client *redis.Client
			if backend == "redis" {
				server = miniredis.RunT(t)
				server.SetTime(clock)
				client = redis.NewClient(&redis.Options{Addr: server.Addr(), MaxRetries: -1})
				t.Cleanup(func() { _ = client.Close() })
			}
			limiter := NewModelConcurrencyLimiter(client, time.Minute)
			limiter.now = func() time.Time { return clock }
			rule := ModelConcurrencyRule{Model: "image-a", Enabled: true, MaxConcurrent: 6, DefaultUserLimit: 2, OtherUsersLimit: 2, UserLimits: []ModelConcurrencyUserLimit{{UserID: 1, MaxConcurrent: 4}}}
			first, err := limiter.Acquire(ctx, rule.Model, 2, rule)
			require.NoError(t, err)
			second, err := limiter.Acquire(ctx, rule.Model, 2, rule)
			require.NoError(t, err)
			_, err = limiter.Acquire(ctx, rule.Model, 2, rule)
			var limited *ModelConcurrencyLimitError
			require.ErrorAs(t, err, &limited)
			assert.Equal(t, "user", limited.Scope)
			_, err = limiter.Acquire(ctx, rule.Model, 3, rule)
			require.ErrorAs(t, err, &limited)
			assert.Equal(t, "other_users", limited.Scope, "all other users share one combined limit")
			var privileged []*ModelConcurrencyLease
			for range 4 {
				lease, err := limiter.Acquire(ctx, rule.Model, 1, rule)
				require.NoError(t, err)
				privileged = append(privileged, lease)
			}
			_, err = limiter.Acquire(ctx, rule.Model, 1, rule)
			require.ErrorAs(t, err, &limited)
			assert.Equal(t, "model", limited.Scope)
			usage, err := limiter.Usage(ctx, rule)
			require.NoError(t, err)
			assert.Equal(t, 6, usage.Total)
			assert.Equal(t, 2, usage.OtherUsers)
			// Changed overrides reclassify work already in flight instead of
			// resetting gauges or leaving it in the old "other users" bucket.
			rule.UserLimits = []ModelConcurrencyUserLimit{{UserID: 2, MaxConcurrent: 4}}
			usage, err = limiter.Usage(ctx, rule)
			require.NoError(t, err)
			assert.Equal(t, 4, usage.OtherUsers)
			rule.MaxConcurrent = 1
			_, err = limiter.Acquire(ctx, rule.Model, 9, rule)
			require.ErrorIs(t, err, ErrModelConcurrencyExceeded, "lowering a limit does not reset active slots")
			require.NoError(t, first.Release(ctx))
			require.NoError(t, first.Release(ctx))
			usage, err = limiter.Usage(ctx, rule)
			require.NoError(t, err)
			assert.Equal(t, 5, usage.Total, "a double release cannot release somebody else's request")
			for _, lease := range append(privileged, second) {
				require.NoError(t, lease.Release(ctx))
			}
			usage, err = limiter.Usage(ctx, rule)
			require.NoError(t, err)
			assert.Zero(t, usage.Total)
			// A different model has independent capacity.
			otherRule := rule
			otherRule.Model = "image-b"
			independent, err := limiter.Acquire(ctx, otherRule.Model, 2, otherRule)
			require.NoError(t, err)
			lease, err := limiter.Acquire(ctx, rule.Model, 2, rule)
			require.NoError(t, err)
			require.NoError(t, lease.Renew(ctx))
			clock = clock.Add(2 * time.Minute)
			if server != nil {
				server.SetTime(clock)
			}
			require.ErrorIs(t, lease.Renew(ctx), ErrModelConcurrencyLeaseExpired)
			replacement, err := limiter.Acquire(ctx, rule.Model, 2, rule)
			require.NoError(t, err, "a crashed owner expires without leaking capacity")
			require.NoError(t, lease.Release(ctx))
			usage, err = limiter.Usage(ctx, rule)
			require.NoError(t, err)
			assert.Equal(t, 1, usage.Total, "late release does not affect replacement lease")
			require.NoError(t, replacement.Release(ctx))
			require.NoError(t, independent.Release(ctx))
		})
	}
}

func TestModelConcurrencyAtomicAdmissionAcrossReplicas(t *testing.T) {
	for _, backend := range []string{"memory", "redis"} {
		t.Run(backend, func(t *testing.T) {
			var client *redis.Client
			if backend == "redis" {
				server := miniredis.RunT(t)
				client = redis.NewClient(&redis.Options{Addr: server.Addr(), MaxRetries: -1})
				t.Cleanup(func() { _ = client.Close() })
			}
			first := NewModelConcurrencyLimiter(client, time.Minute)
			second := first
			if client != nil {
				secondClient := redis.NewClient(client.Options())
				t.Cleanup(func() { _ = secondClient.Close() })
				second = NewModelConcurrencyLimiter(secondClient, time.Minute)
			}
			rule := ModelConcurrencyRule{Model: "image", Enabled: true, MaxConcurrent: 5}
			start := make(chan struct{})
			admitted := make(chan *ModelConcurrencyLease, 32)
			failed := make(chan error, 32)
			var workers sync.WaitGroup
			for i := range 32 {
				workers.Go(func() {
					<-start
					limiter := first
					if i%2 == 0 {
						limiter = second
					}
					lease, err := limiter.Acquire(context.Background(), rule.Model, i+1, rule)
					if err != nil {
						failed <- err
					} else {
						admitted <- lease
					}
				})
			}
			close(start)
			workers.Wait()
			close(admitted)
			close(failed)
			require.Len(t, admitted, 5, "atomic admission must not overbook across independent Redis clients/engines")
			require.Len(t, failed, 27)
			for err := range failed {
				require.ErrorIs(t, err, ErrModelConcurrencyExceeded)
			}
			for lease := range admitted {
				require.NoError(t, lease.Release(context.Background()))
			}
			usage, err := first.Usage(context.Background(), rule)
			require.NoError(t, err)
			assert.Zero(t, usage.Total)
		})
	}
}

func TestModelConcurrencyDurableTaskTransferAndRecovery(t *testing.T) {
	for _, backend := range []string{"memory", "redis"} {
		t.Run(backend, func(t *testing.T) {
			previousRDB, previousRedis, previousMemory := RDB, RedisEnabled, modelConcurrencyMemory
			t.Cleanup(func() { RDB, RedisEnabled, modelConcurrencyMemory = previousRDB, previousRedis, previousMemory })
			RedisEnabled = backend == "redis"
			clock := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
			var server *miniredis.Miniredis
			var client *redis.Client
			if RedisEnabled {
				server = miniredis.RunT(t)
				server.SetTime(clock)
				client = redis.NewClient(&redis.Options{Addr: server.Addr(), MaxRetries: -1})
				t.Cleanup(func() { _ = client.Close() })
				RDB = client
			}
			modelConcurrencyMemory = NewModelConcurrencyLimiter(nil, time.Minute)
			modelConcurrencyMemory.now = func() time.Time { return clock }
			rule := ModelConcurrencyRule{Model: "durable-image", Enabled: true, MaxConcurrent: 1}
			ctx := context.Background()
			lease, err := AcquireModelConcurrency(ctx, rule.Model, 1, rule)
			require.NoError(t, err)
			// Recovery can observe the committed task row just before the HTTP
			// owner transfers its provisional UUID. Transfer must coalesce both.
			earlyIdentity := lease.Identity()
			earlyIdentity.ID, earlyIdentity.Persistent = "task:123", true
			_, err = RecoverModelConcurrencyLease(ctx, earlyIdentity)
			require.NoError(t, err)
			require.NoError(t, lease.Transfer(ctx, "task:123"))
			identity := lease.Identity()
			assert.True(t, identity.Persistent)
			assert.Equal(t, "task:123", identity.ID)
			require.NoError(t, lease.Transfer(ctx, "task:123"), "transfer is idempotent")
			recovered, err := RecoverModelConcurrencyLease(ctx, identity)
			require.NoError(t, err)
			// A temporary request on the same model must not put an expiry on
			// the model's persistent task gauge or erase its durable member.
			rule.MaxConcurrent = 2
			transient, err := AcquireModelConcurrency(ctx, rule.Model, 2, rule)
			require.NoError(t, err)
			clock = clock.Add(time.Hour)
			if server != nil {
				server.SetTime(clock)
				server.FastForward(time.Hour)
			}
			rule.MaxConcurrent = 1
			_, err = AcquireModelConcurrency(ctx, rule.Model, 2, rule)
			require.ErrorIs(t, err, ErrModelConcurrencyExceeded, "an accepted async task keeps capacity beyond the HTTP lease TTL")
			leases, err := ListDurableModelConcurrencyLeases(ctx)
			require.NoError(t, err)
			require.Len(t, leases, 1)
			assert.Equal(t, identity, leases[0], "durable enumeration does not depend on current configuration")
			require.NoError(t, transient.Release(ctx))
			require.NoError(t, recovered.Renew(ctx))
			require.NoError(t, recovered.Release(ctx))
			require.NoError(t, lease.Release(ctx))
			leases, err = ListDurableModelConcurrencyLeases(ctx)
			require.NoError(t, err)
			assert.Empty(t, leases)
			if RedisEnabled {
				server.Close()
				_, err = AcquireModelConcurrency(ctx, rule.Model, 1, rule)
				assert.True(t, errors.Is(err, ErrModelConcurrencyUnavailable), "Redis errors must not fall back to uncoordinated memory admission")
			}
		})
	}
}

func TestModelConcurrencyGroupsIsolateAdmissionAndDurableLifecycle(t *testing.T) {
	for _, backend := range []string{"memory", "redis"} {
		t.Run(backend, func(t *testing.T) {
			originalEnabled, originalRDB, originalMemory := RedisEnabled, RDB, modelConcurrencyMemory
			RedisEnabled, RDB = false, nil
			modelConcurrencyMemory = NewModelConcurrencyLimiter(nil, time.Minute)
			t.Cleanup(func() { RedisEnabled, RDB, modelConcurrencyMemory = originalEnabled, originalRDB, originalMemory })
			if backend == "redis" {
				server := miniredis.RunT(t)
				RDB = redis.NewClient(&redis.Options{Addr: server.Addr()})
				RedisEnabled = true
				t.Cleanup(func() { require.NoError(t, RDB.Close()) })
			}
			ctx := context.Background()
			rules := []ModelConcurrencyRule{
				{Model: "same:model", Enabled: true, MaxConcurrent: 1},
				{Group: "vip:model", Model: "same:model", Enabled: true, MaxConcurrent: 1, DefaultUserLimit: 1},
				{Group: "vip", Model: "model:same:model", Enabled: true, MaxConcurrent: 1},
				{Group: "standard", Model: "same:model", Enabled: true, MaxConcurrent: 1},
			}
			leases := []*ModelConcurrencyLease{}
			for _, rule := range rules {
				lease, err := AcquireModelConcurrency(ctx, rule.Model, 21, rule)
				require.NoError(t, err)
				leases = append(leases, lease)
			}
			for _, rule := range rules {
				_, err := AcquireModelConcurrency(ctx, rule.Model, 21, rule)
				require.ErrorIs(t, err, ErrModelConcurrencyExceeded)
			}
			for i, lease := range leases {
				require.NoError(t, lease.Renew(ctx))
				require.NoError(t, lease.Transfer(ctx, "task:group-"+backend+"-"+strconv.Itoa(i)))
			}
			durable, err := ListDurableModelConcurrencyLeases(ctx)
			require.NoError(t, err)
			require.Len(t, durable, 4)
			for _, identity := range durable {
				restored, err := RestoreModelConcurrencyLease(identity)
				require.NoError(t, err)
				require.NoError(t, restored.Renew(ctx))
				require.NoError(t, restored.Release(ctx))
			}
			usage, err := GetModelConcurrencyUsage(ctx, rules)
			require.NoError(t, err)
			for i, row := range usage {
				assert.Equal(t, rules[i].Group, row.Group)
				assert.Zero(t, row.Total)
			}
			durable, err = ListDurableModelConcurrencyLeases(ctx)
			require.NoError(t, err)
			assert.Empty(t, durable)
		})
	}
}
