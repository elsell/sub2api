//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type revocationUserRepo struct {
	UserRepository
	user User
	err  error
}

func (r *revocationUserRepo) GetByID(context.Context, int64) (*User, error) {
	copy := r.user
	return &copy, nil
}
func (r *revocationUserRepo) IncrementTokenVersion(context.Context, int64) error {
	if r.err != nil {
		return r.err
	}
	r.user.TokenVersion++
	return nil
}

type tokenSecurityCache struct {
	RefreshTokenCache
	data   *RefreshTokenData
	err    error
	stores int
}

func (c *tokenSecurityCache) ConsumeRefreshToken(context.Context, string) (*RefreshTokenData, error) {
	return c.data, c.err
}
func (c *tokenSecurityCache) DeleteTokenFamily(context.Context, string) error      { return nil }
func (c *tokenSecurityCache) DeleteUserRefreshTokens(context.Context, int64) error { return c.err }
func (c *tokenSecurityCache) StoreRefreshToken(context.Context, string, *RefreshTokenData, time.Duration) error {
	c.stores++
	return nil
}

func TestRevokeAllUserTokensInvalidatesOldAccessAndRefreshTokens(t *testing.T) {
	ctx := context.Background()
	repo := &revocationUserRepo{user: User{ID: 1, Email: "owner@example.com", PasswordHash: "hash", Role: RoleAdmin, Status: StatusActive}}
	auth := newAuthService(&userRepoStub{}, nil, nil, nil)
	auth.userRepo = repo
	old, err := auth.GenerateToken(ctx, &repo.user)
	require.NoError(t, err)
	oldVersion := resolvedTokenVersion(&repo.user)
	require.NoError(t, auth.RevokeAllUserTokens(ctx, 1))
	// Reloading through another service instance observes the persistent generation.
	other := newAuthService(&userRepoStub{}, nil, nil, nil)
	other.userRepo = repo
	_, err = other.RefreshToken(ctx, old)
	require.ErrorIs(t, err, ErrTokenRevoked)
	cache := &tokenSecurityCache{data: &RefreshTokenData{UserID: 1, TokenVersion: oldVersion, ExpiresAt: time.Now().Add(time.Hour)}}
	other.refreshTokenCache = cache
	_, err = other.RefreshTokenPair(ctx, refreshTokenPrefix+"old")
	require.ErrorIs(t, err, ErrTokenRevoked)
	require.Zero(t, cache.stores)
	fresh, err := other.GenerateToken(ctx, &repo.user)
	require.NoError(t, err)
	_, err = other.RefreshToken(ctx, fresh)
	require.NoError(t, err)
}

func TestRefreshTokenPairStorageFailureDoesNotMintTokens(t *testing.T) {
	auth := newAuthService(&userRepoStub{}, nil, nil, nil)
	cache := &tokenSecurityCache{err: errors.New("redis unavailable")}
	auth.refreshTokenCache = cache
	pair, err := auth.RefreshTokenPair(context.Background(), refreshTokenPrefix+"old")
	require.ErrorIs(t, err, ErrServiceUnavailable)
	require.Nil(t, pair)
	require.Zero(t, cache.stores)
}

func TestRevokeAllUserTokensReportsPersistenceAndCleanupFailures(t *testing.T) {
	failure := errors.New("storage unavailable")
	repo := &revocationUserRepo{err: failure}
	auth := newAuthService(&userRepoStub{}, nil, nil, nil)
	auth.userRepo = repo
	require.ErrorIs(t, auth.RevokeAllUserTokens(context.Background(), 1), failure)
	require.Zero(t, repo.user.TokenVersion)
	repo.err = nil
	auth.refreshTokenCache = &tokenSecurityCache{err: failure}
	require.ErrorIs(t, auth.RevokeAllUserTokens(context.Background(), 1), failure)
	require.EqualValues(t, 1, repo.user.TokenVersion)
}
