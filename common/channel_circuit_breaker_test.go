package common

import (
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func withChannelCircuitTestSettings(t *testing.T, threshold int) {
	t.Helper()
	oldRedisEnabled := RedisEnabled
	oldThreshold := ChannelCircuitFailureThreshold
	oldForbiddenThreshold := ChannelCircuitForbiddenThreshold
	oldInternalThreshold := ChannelCircuitInternalThreshold
	oldWindow := ChannelCircuitFailureWindow
	oldCooldown := ChannelCircuitCooldown
	RedisEnabled = false
	ChannelCircuitFailureThreshold = threshold
	ChannelCircuitForbiddenThreshold = 10
	ChannelCircuitInternalThreshold = 10
	ChannelCircuitFailureWindow = time.Minute
	ChannelCircuitCooldown = time.Minute
	channelCircuitMemory.Lock()
	channelCircuitMemory.states = make(map[string]channelCircuitMemoryState)
	channelCircuitMemory.Unlock()
	t.Cleanup(func() {
		RedisEnabled = oldRedisEnabled
		ChannelCircuitFailureThreshold = oldThreshold
		ChannelCircuitForbiddenThreshold = oldForbiddenThreshold
		ChannelCircuitInternalThreshold = oldInternalThreshold
		ChannelCircuitFailureWindow = oldWindow
		ChannelCircuitCooldown = oldCooldown
		channelCircuitMemory.Lock()
		channelCircuitMemory.states = make(map[string]channelCircuitMemoryState)
		channelCircuitMemory.Unlock()
	})
}

func TestChannelCircuitInputImageFetchErrorsDoNotAffectChannelHealth(t *testing.T) {
	for _, useRedis := range []bool{false, true} {
		name := "memory"
		if useRedis {
			name = "redis"
		}
		t.Run(name, func(t *testing.T) {
			withChannelCircuitTestSettings(t, 3)
			if useRedis {
				server := miniredis.RunT(t)
				oldRDB := RDB
				RDB = redis.NewClient(&redis.Options{Addr: server.Addr()})
				RedisEnabled = true
				t.Cleanup(func() { _ = RDB.Close(); RDB = oldRDB })
			}
			fetchErrors := []string{
				"image_url fetch failed: Failed to perform, curl: (28) Connection timed out after 60001 milliseconds.",
				"status_code=400, image_url fetch failed: HTTP 404",
				" IMAGE_URL fetch failed: deadline exceeded ",
			}
			for _, message := range fetchErrors {
				for range 3 {
					count, tripped := RecordChannelCircuitFailure(94, "image-model", 400, message)
					assert.Zero(t, count)
					assert.False(t, tripped)
				}
			}
			assert.Equal(t, []int{94}, FilterUsableChannelIDs([]int{94}, "image-model"))
			// Input errors must not reset a real failure sequence.
			for range 2 {
				_, tripped := RecordChannelCircuitFailure(94, "image-model", 504, "gateway timeout")
				require.False(t, tripped)
			}
			_, _ = RecordChannelCircuitFailure(94, "image-model", 400, fetchErrors[0])
			count, tripped := RecordChannelCircuitFailure(94, "image-model", 504, "gateway timeout")
			require.Equal(t, 3, count)
			require.True(t, tripped)
			_, _ = RecordChannelCircuitFailure(94, "image-model", 400, fetchErrors[0])
			assert.True(t, IsChannelCircuitOpen(94, "image-model"))
		})
	}
}

func TestChannelCircuitInputImageExemptionKeepsUpstreamProtection(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		message string
	}{
		{"generic_timeout", 400, "upstream connection timed out"},
		{"embedded_marker", 400, "upstream timeout at https://example.com/image_url fetch failed: x"},
		{"rate_limit", 429, "image_url fetch failed: rate limited"},
		{"internal", 500, "image_url fetch failed: internal error"},
		{"unavailable", 503, "upstream unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withChannelCircuitTestSettings(t, 2)
			ChannelCircuitInternalThreshold = 2
			_, tripped := RecordChannelCircuitFailure(94, "image-model", tc.status, tc.message)
			require.False(t, tripped)
			_, tripped = RecordChannelCircuitFailure(94, "image-model", tc.status, tc.message)
			assert.True(t, tripped)
			assert.True(t, IsChannelCircuitOpen(94, "image-model"))
		})
	}
}

func TestChannelCircuitInternalErrorTripsOnTenthConsecutiveFailure(t *testing.T) {
	withChannelCircuitTestSettings(t, 30)

	for attempt := 1; attempt <= 9; attempt++ {
		count, tripped := RecordChannelCircuitFailure(10, "image-model", 500)
		require.Equal(t, attempt, count)
		require.False(t, tripped)
	}
	count, tripped := RecordChannelCircuitFailure(10, "image-model", 500)
	require.Equal(t, 10, count)
	require.True(t, tripped)
	require.True(t, IsChannelCircuitOpen(10, "image-model"))
}

func TestChannelCircuitForbiddenTripsOnTenthConsecutiveFailure(t *testing.T) {
	withChannelCircuitTestSettings(t, 30)

	for attempt := 1; attempt <= 9; attempt++ {
		count, tripped := RecordChannelCircuitFailure(10, "image-model", 403)
		require.Equal(t, attempt, count)
		require.False(t, tripped)
	}
	count, tripped := RecordChannelCircuitFailure(10, "image-model", 403)
	require.Equal(t, 10, count)
	require.True(t, tripped)
	require.True(t, IsChannelCircuitOpen(10, "image-model"))
}

func TestChannelCircuitSwitchingFailureClassRestartsSequence(t *testing.T) {
	withChannelCircuitTestSettings(t, 3)

	_, _ = RecordChannelCircuitFailure(10, "image-model", 403)
	_, _ = RecordChannelCircuitFailure(10, "image-model", 403)
	count, tripped := RecordChannelCircuitFailure(10, "image-model", 429)
	require.Equal(t, 1, count)
	require.False(t, tripped)
	count, tripped = RecordChannelCircuitFailure(10, "image-model", 403)
	require.Equal(t, 1, count)
	require.False(t, tripped)
}

func TestChannelCircuitRequiresConsecutiveTargetFailures(t *testing.T) {
	withChannelCircuitTestSettings(t, 3)

	_, tripped := RecordChannelCircuitFailure(10, "image-model", 429)
	require.False(t, tripped)
	_, tripped = RecordChannelCircuitFailure(10, "image-model", 502)
	require.False(t, tripped)

	// A different status breaks the 429/502/504 sequence.
	_, tripped = RecordChannelCircuitFailure(10, "image-model", 500)
	require.False(t, tripped)
	_, tripped = RecordChannelCircuitFailure(10, "image-model", 504)
	require.False(t, tripped)
	_, tripped = RecordChannelCircuitFailure(10, "image-model", 504)
	require.False(t, tripped)
	_, tripped = RecordChannelCircuitFailure(10, "image-model", 504)
	require.True(t, tripped)
	require.True(t, IsChannelCircuitOpen(10, "image-model"))
}

func TestChannelCircuitSuccessResetsAndFilteringIsModelSpecific(t *testing.T) {
	withChannelCircuitTestSettings(t, 2)

	_, _ = RecordChannelCircuitFailure(10, "image-model", 502)
	_, tripped := RecordChannelCircuitFailure(10, "image-model", 502)
	require.True(t, tripped)
	require.Equal(t, []int{20}, FilterUsableChannelIDs([]int{10, 20}, "image-model"))
	require.Equal(t, []int{10, 20}, FilterUsableChannelIDs([]int{10, 20}, "other-model"))

	ResetChannelCircuitOnSuccess(10, "image-model")
	require.True(t, IsChannelCircuitOpen(10, "image-model"))
	require.Equal(t, []int{20}, FilterUsableChannelIDs([]int{10, 20}, "image-model"))
}

func TestChannelCircuitRedisStateIsSharedAndAtomic(t *testing.T) {
	server := miniredis.RunT(t)
	oldRedisEnabled := RedisEnabled
	oldRDB := RDB
	oldThreshold := ChannelCircuitFailureThreshold
	oldForbiddenThreshold := ChannelCircuitForbiddenThreshold
	oldInternalThreshold := ChannelCircuitInternalThreshold
	oldWindow := ChannelCircuitFailureWindow
	oldCooldown := ChannelCircuitCooldown
	RedisEnabled = true
	RDB = redis.NewClient(&redis.Options{Addr: server.Addr()})
	ChannelCircuitFailureThreshold = 2
	ChannelCircuitForbiddenThreshold = 6
	ChannelCircuitInternalThreshold = 10
	ChannelCircuitFailureWindow = time.Minute
	ChannelCircuitCooldown = time.Minute
	t.Cleanup(func() {
		_ = RDB.Close()
		RedisEnabled = oldRedisEnabled
		RDB = oldRDB
		ChannelCircuitFailureThreshold = oldThreshold
		ChannelCircuitForbiddenThreshold = oldForbiddenThreshold
		ChannelCircuitInternalThreshold = oldInternalThreshold
		ChannelCircuitFailureWindow = oldWindow
		ChannelCircuitCooldown = oldCooldown
	})

	_, tripped := RecordChannelCircuitFailure(10, "image-model", 504)
	require.False(t, tripped)
	count, tripped := RecordChannelCircuitFailure(10, "image-model", 504)
	require.Equal(t, 2, count)
	require.True(t, tripped)
	require.True(t, IsChannelCircuitOpen(10, "image-model"))
	require.Equal(t, []int{20}, FilterUsableChannelIDs([]int{10, 20}, "image-model"))

	ResetChannelCircuitOnSuccess(10, "image-model")
	require.True(t, IsChannelCircuitOpen(10, "image-model"))
	server.FastForward(time.Minute)
	require.False(t, IsChannelCircuitOpen(10, "image-model"))
	// If the first trial after cooldown fails, immediately divert new traffic again.
	_, tripped = RecordChannelCircuitFailure(10, "image-model", 504)
	require.True(t, tripped)
	server.FastForward(time.Minute)
	ResetChannelCircuitOnSuccess(10, "image-model")
	require.False(t, IsChannelCircuitOpen(10, "image-model"))

	for attempt := 1; attempt <= 5; attempt++ {
		count, tripped = RecordChannelCircuitFailure(10, "image-model", 403)
		require.Equal(t, attempt, count)
		require.False(t, tripped)
	}
	count, tripped = RecordChannelCircuitFailure(10, "image-model", 403)
	require.Equal(t, 6, count)
	require.True(t, tripped)
}

func TestChannelCircuitIgnoresPolicyAndClearsPreThresholdFailures(t *testing.T) {
	withChannelCircuitTestSettings(t, 3)
	_, _ = RecordChannelCircuitFailure(50, "image-model", 504)
	ResetChannelCircuitOnSuccess(50, "image-model")
	count, tripped := RecordChannelCircuitFailure(50, "image-model", 504)
	require.Equal(t, 1, count)
	require.False(t, tripped)
	for _, status := range []int{422, 451, 500, 403} {
		for attempt := 0; attempt < 10; attempt++ {
			_, tripped = RecordChannelCircuitFailure(50, "image-model", status, "content moderation rejected")
			require.False(t, tripped)
		}
	}
	require.False(t, IsChannelCircuitOpen(50, "image-model"))
}
