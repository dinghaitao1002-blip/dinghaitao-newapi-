package model

import "sync/atomic"

var imageAsyncQuotaReads atomic.Bool

// EnableImageAsyncQuotaReads makes quota admission use the shared database for
// this process. Async ledger commits invalidate caches, whereas legacy sync
// billing schedules Redis quota deltas independently of its database writes.
// Reading cached quota while mixing those paths can observe a delayed delta or
// stale refill. Enable this once before admitting async work on every replica;
// Redis remains available for authentication fences, sessions and rate limits.
func EnableImageAsyncQuotaReads() {
	imageAsyncQuotaReads.Store(true)
}
