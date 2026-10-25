package common

import (
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/go-redis/redis/v8"
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
