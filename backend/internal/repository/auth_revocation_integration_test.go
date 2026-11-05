//go:build integration

package repository

import (
	"context"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestDurableTokenRevocationPostgres(t *testing.T) {
	ctx := context.Background()
	repo := newUserRepositoryWithSQL(integrationEntClient, integrationDB)
	owner := &service.User{Email: "auth-revocation-integration@example.com", PasswordHash: "test-only-hash", Role: service.RoleAdmin, Status: service.StatusActive}
	require.NoError(t, repo.Create(ctx, owner))
	t.Cleanup(func() { _, _ = integrationDB.ExecContext(ctx, "DELETE FROM users WHERE id = $1", owner.ID) })
	cfg := &config.Config{}
	cfg.JWT.Secret = "integration-test-only-secret"
	cfg.JWT.ExpireHour = 1
	auth := service.NewAuthService(nil, repo, nil, NewRefreshTokenCache(integrationRedis), cfg, nil, nil, nil, nil, nil, nil, nil, nil)
	old, err := auth.GenerateToken(ctx, owner)
	require.NoError(t, err)
	require.NoError(t, auth.RevokeAllUserTokens(ctx, owner.ID))
	replicaRepo := newUserRepositoryWithSQL(integrationEntClient, integrationDB)
	replica := service.NewAuthService(nil, replicaRepo, nil, nil, cfg, nil, nil, nil, nil, nil, nil, nil, nil)
	_, err = replica.RefreshToken(ctx, old)
	require.ErrorIs(t, err, service.ErrTokenRevoked)
	loaded, err := replicaRepo.GetByID(ctx, owner.ID)
	require.NoError(t, err)
	require.EqualValues(t, 1, loaded.TokenVersion)
	fresh, err := replica.GenerateToken(ctx, loaded)
	require.NoError(t, err)
	_, err = replica.RefreshToken(ctx, fresh)
	require.NoError(t, err)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- repo.IncrementTokenVersion(ctx, owner.ID) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	owner.Username = "changed"
	require.NoError(t, repo.Update(ctx, owner, service.UserUpdateFields{Username: true}))
	loaded, err = replicaRepo.GetByID(ctx, owner.ID)
	require.NoError(t, err)
	require.EqualValues(t, 9, loaded.TokenVersion)
	require.Equal(t, "changed", loaded.Username)
}
