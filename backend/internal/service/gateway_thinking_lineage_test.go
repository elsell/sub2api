package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type lineageCacheStub struct {
	GatewayCache
	accounts map[string]int64
	mixed    map[string]bool
}

func (c *lineageCacheStub) RecordThinkingAccount(_ context.Context, _ int64, key string, accountID int64, _ time.Duration) (bool, error) {
	if prev, ok := c.accounts[key]; ok && prev != accountID {
		c.mixed[key] = true
	}
	c.accounts[key] = accountID
	return c.mixed[key], nil
}

const crossAccountBody = `{"model":"claude-opus-5-5","thinking":{"type":"adaptive"},"messages":[` +
	`{"role":"user","content":"hi"},` +
	`{"role":"assistant","content":[{"type":"thinking","thinking":"plan","signature":"sig-a"},{"type":"tool_use","id":"t1","name":"calc","input":{"n":12345678901234567890}}]},` +
	`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]},` +
	`{"role":"assistant","content":[{"type":"redacted_thinking","data":"enc"}]},` +
	`{"role":"user","content":"next"}]}`

func TestStripCrossAccountThinking_Claude55KeepsThinkingOn(t *testing.T) {
	out := StripCrossAccountThinking([]byte(crossAccountBody), "claude-opus-5-5")
	require.NotContains(t, string(out), `"type":"thinking"`)
	require.NotContains(t, string(out), `redacted_thinking`)
	require.Equal(t, "adaptive", gjson.GetBytes(out, "thinking.type").String(), "5.5 keeps thinking enabled")
	require.Equal(t, "tool_use", gjson.GetBytes(out, "messages.1.content.0.type").String())
	require.Contains(t, string(out), `12345678901234567890`, "large integers stay exact")
	require.Equal(t, "(assistant content removed)", gjson.GetBytes(out, "messages.3.content.0.text").String())
	require.Equal(t, `{"model":"claude-opus-5-5","thinking":{"type":"adaptive"},"messages":`, string(out[:len(`{"model":"claude-opus-5-5","thinking":{"type":"adaptive"},"messages":`)]), "fields outside messages keep their bytes")
	require.Equal(t, out, StripCrossAccountThinking(out, "claude-opus-5-5"), "deterministic and idempotent, so the prompt cache prefix is stable")
}

func TestStripCrossAccountThinking_OlderModelDowngrades(t *testing.T) {
	out := StripCrossAccountThinking([]byte(crossAccountBody), "claude-sonnet-4-6")
	require.False(t, gjson.GetBytes(out, "thinking").Exists(), "older models disable thinking instead of sending an incomplete tool turn")
	require.NotContains(t, string(out), `"signature"`)
}

func TestThinkingHistoryCrossedAccounts(t *testing.T) {
	stub := &lineageCacheStub{accounts: map[string]int64{}, mixed: map[string]bool{}}
	svc := &GatewayService{cache: stub}
	ctx := context.Background()
	group := int64(1)
	parsed := &ParsedRequest{GroupID: &group, SessionContext: &SessionContext{APIKeyID: 7, SessionAffinityID: "conv-a"}}
	sub := &Account{ID: 1, Platform: PlatformAnthropic}
	key := &Account{ID: 3, Platform: PlatformAnthropic}

	require.False(t, svc.thinkingHistoryCrossedAccounts(ctx, sub, parsed, "claude-opus-5-5"))
	require.False(t, svc.thinkingHistoryCrossedAccounts(ctx, sub, parsed, "claude-opus-5-5"))
	require.True(t, svc.thinkingHistoryCrossedAccounts(ctx, key, parsed, "claude-opus-5-5"), "failover to another account")
	require.True(t, svc.thinkingHistoryCrossedAccounts(ctx, sub, parsed, "claude-opus-5-5"), "history stays mixed after switching back")

	contentOnly := &ParsedRequest{GroupID: &group}
	require.False(t, svc.thinkingHistoryCrossedAccounts(ctx, key, contentOnly, "claude-opus-5-5"), "content hashes are not stable lineage keys")
	require.False(t, svc.thinkingHistoryCrossedAccounts(ctx, &Account{ID: 9, Platform: PlatformOpenAI}, parsed, "gpt-6.1-sol"))
	require.False(t, (&GatewayService{}).thinkingHistoryCrossedAccounts(ctx, key, parsed, "claude-opus-5-5"), "caches without the capability are a no-op")
}

func TestDefaultRectifierSettings_APIKeySignatureEnabled(t *testing.T) {
	require.True(t, DefaultRectifierSettings().APIKeySignatureEnabled, "API-key accounts retry signature errors by default")
}
