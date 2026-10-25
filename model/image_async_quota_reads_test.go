package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestImageAsyncQuotaAdmissionIgnoresStaleRedisBalances(t *testing.T) {
	for _, cachedQuota := range []int{0, 100000} {
		t.Run(map[int]string{0: "false_exhaustion", 100000: "stale_surplus"}[cachedQuota], func(t *testing.T) {
			truncateTables(t)
			useUserCacheMiniRedis(t)
			oldMode := imageAsyncQuotaReads.Load()
			t.Cleanup(func() { imageAsyncQuotaReads.Store(oldMode) })
			imageAsyncQuotaReads.Store(false)
			user := User{Username: "async-cache-test", Quota: 1000, Status: common.UserStatusEnabled, AuthVersion: 1}
			require.NoError(t, DB.Create(&user).Error)
			token := Token{UserId: user.Id, Key: "async-cache-test-key", Status: common.TokenStatusEnabled, ExpiredTime: -1, RemainQuota: 1000}
			require.NoError(t, DB.Create(&token).Error)
			cachedUser := user
			cachedUser.Quota = cachedQuota
			require.NoError(t, populateUserCache(cachedUser))
			cachedToken := token
			cachedToken.RemainQuota = cachedQuota
			require.NoError(t, cacheSetTokenForTest(cachedToken))
			before, err := GetTokenByKey(token.Key, false)
			require.NoError(t, err)
			assert.Equal(t, cachedQuota, before.RemainQuota)
			quota, err := GetUserQuota(user.Id, false)
			require.NoError(t, err)
			assert.Equal(t, cachedQuota, quota)

			EnableImageAsyncQuotaReads()
			after, err := ValidateUserToken(token.Key)
			require.NoError(t, err)
			assert.Equal(t, 1000, after.RemainQuota)
			quota, err = GetUserQuota(user.Id, false)
			require.NoError(t, err)
			assert.Equal(t, 1000, quota)

			// Existing authentication metadata caching remains available; only
			// billing admission is made database-authoritative.
			cached, err := cacheGetUserBase(user.Id)
			require.NoError(t, err)
			assert.Equal(t, common.UserStatusEnabled, cached.Status)
			assert.Equal(t, cachedQuota, cached.Quota)
		})
	}
}
