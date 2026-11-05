package repository

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestConsumeRefreshTokenSingleWinnerAcrossClients(t *testing.T) {
	server := miniredis.RunT(t)
	ctx := context.Background()
	caches := make([]service.RefreshTokenCache, 2)
	for i := range caches {
		client := redis.NewClient(&redis.Options{Addr: server.Addr()})
		t.Cleanup(func() { _ = client.Close() })
		caches[i] = NewRefreshTokenCache(client)
	}
	require.NoError(t, caches[0].StoreRefreshToken(ctx, "hash", &service.RefreshTokenData{UserID: 42}, time.Hour))
	var winners atomic.Int32
	var unexpected atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			data, err := caches[i%2].ConsumeRefreshToken(ctx, "hash")
			if err == nil && data.UserID == 42 {
				winners.Add(1)
			} else if !errors.Is(err, service.ErrRefreshTokenNotFound) {
				unexpected.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	require.EqualValues(t, 1, winners.Load())
	require.Zero(t, unexpected.Load())
	_, err := caches[0].ConsumeRefreshToken(ctx, "hash")
	require.ErrorIs(t, err, service.ErrRefreshTokenNotFound)
}

func TestConsumeRefreshTokenFailsClosedOnStorageAndEncodingErrors(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache := NewRefreshTokenCache(client)
	server.Set(refreshTokenKey("broken"), "invalid JSON")
	data, err := cache.ConsumeRefreshToken(context.Background(), "broken")
	require.Error(t, err)
	require.Nil(t, data)
	require.False(t, server.Exists(refreshTokenKey("broken")))
	require.NoError(t, client.Close())
	data, err = cache.ConsumeRefreshToken(context.Background(), "any")
	require.Error(t, err)
	require.Nil(t, data)
}
