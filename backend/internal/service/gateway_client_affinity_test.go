package service

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestGenerateSessionHash_ClientAffinity(t *testing.T) {
	svc := &GatewayService{}
	parse := func(body string, keyID int64, session string) *ParsedRequest {
		t.Helper()
		parsed, err := ParseGatewayRequest(NewRequestBodyRef([]byte(body)), "")
		require.NoError(t, err)
		parsed.SessionContext = &SessionContext{APIKeyID: keyID, SessionAffinityID: session}
		return parsed
	}
	first := `{"messages":[{"role":"user","content":[{"type":"text","text":"first turn","cache_control":{"type":"ephemeral"}}]}]}`
	later := `{"messages":[{"role":"user","content":"first turn"},{"role":"assistant","content":"answer"},{"role":"user","content":[{"type":"text","text":"next turn","cache_control":{"type":"ephemeral"}}]}]}`
	initial := svc.GenerateSessionHash(parse(first, 7, "conversation-a"))
	require.NotEmpty(t, initial)
	require.Equal(t, initial, svc.GenerateSessionHash(parse(later, 7, "conversation-a")), "growing cache checkpoints must not change account affinity")
	require.NotEqual(t, initial, svc.GenerateSessionHash(parse(first, 8, "conversation-a")), "API keys must have independent affinity namespaces")
	require.NotEqual(t, initial, svc.GenerateSessionHash(parse(first, 7, "conversation-b")), "different conversations must remain independent")
	require.NotEqual(t, svc.GenerateSessionHash(parse(first, 7, "")), svc.GenerateSessionHash(parse(later, 7, "")), "legacy content fallback remains available without a header")
	metadata := `{"metadata":{"user_id":"user_a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2_account_550e8400-e29b-41d4-a716-446655440000_session_123e4567-e89b-12d3-a456-426614174000"},"messages":[{"role":"user","content":"hello"}]}`
	require.Equal(t, svc.GenerateSessionHash(parse(metadata, 7, "")), svc.GenerateSessionHash(parse(metadata, 7, "conversation-a")), "native Claude metadata retains priority")
}

func TestClaudeSessionAffinityIDFromHeader(t *testing.T) {
	require.Empty(t, ClaudeSessionAffinityIDFromHeader(nil))
	for _, tc := range []struct{ value, want string }{
		{"", ""}, {" stable-session ", "stable-session"}, {"bad\nidentity", ""}, {strings.Repeat("a", 256), ""},
	} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("POST", "/v1/messages", nil)
		c.Request.Header.Set("X-Session-Affinity", tc.value)
		require.Equal(t, tc.want, ClaudeSessionAffinityIDFromHeader(c))
	}
}
