package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestGatewayCacheRecordThinkingAccount(t *testing.T) {
	mr := miniredis.RunT(t)
	cache := NewGatewayCache(redis.NewClient(&redis.Options{Addr: mr.Addr()})).(service.ThinkingLineageCache)
	ctx := context.Background()
	record := func(group, account int64, key string) bool {
		t.Helper()
		mixed, err := cache.RecordThinkingAccount(ctx, group, key, account, time.Hour)
		require.NoError(t, err)
		return mixed
	}

	require.False(t, record(1, 10, "conv"), "first account starts the lineage")
	require.False(t, record(1, 10, "conv"), "same account stays clean")
	require.True(t, record(1, 20, "conv"), "a second account marks the conversation mixed")
	require.True(t, record(1, 10, "conv"), "returning to the first account is still mixed")
	require.False(t, record(2, 20, "conv"), "groups are isolated")
	require.False(t, record(1, 20, "other"), "conversations are isolated")

	mr.FastForward(2 * time.Hour)
	require.False(t, record(1, 20, "conv"), "lineage expires with its TTL")
}
