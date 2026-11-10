package service

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Thinking signatures are bound to the upstream account that produced them.
// When a conversation fails over to another account (for example, from a
// rate-limited subscription to an API key), its history carries signatures the
// new account rejects with 400 "Invalid `signature` in `thinking` block".
// Once a conversation has been served by more than one account, every later
// request strips historical thinking before forwarding. The rewrite is
// deterministic, so the prompt cache stays valid after the first switched turn.

// ThinkingLineageTTL bounds how long a conversation's account lineage is kept.
const ThinkingLineageTTL = 7 * 24 * time.Hour

// ThinkingLineageCache is an optional GatewayCache capability. RecordThinkingAccount
// atomically records the account serving a conversation and reports whether
// the conversation has now been served by more than one account.
type ThinkingLineageCache interface {
	RecordThinkingAccount(ctx context.Context, groupID int64, lineageKey string, accountID int64, ttl time.Duration) (mixed bool, err error)
}

// thinkingLineageKey returns a conversation identity that stays stable as the
// conversation grows. Content-derived sticky hashes change between turns, so
// they cannot carry lineage and are ignored.
func (s *GatewayService) thinkingLineageKey(parsed *ParsedRequest) string {
	if parsed == nil {
		return ""
	}
	if parsed.MetadataUserID != "" {
		if uid := ParseMetadataUserID(parsed.MetadataUserID); uid != nil && uid.SessionID != "" {
			return "metadata:" + uid.SessionID
		}
	}
	if sc := parsed.SessionContext; sc != nil && sc.SessionAffinityID != "" {
		return s.hashContent("client-affinity:" + strconv.FormatInt(sc.APIKeyID, 10) + ":" + sc.SessionAffinityID)
	}
	return ""
}

// thinkingHistoryCrossedAccounts records the serving account and reports
// whether the conversation's history may hold another account's signatures.
// Cache errors fail open (no stripping); the reactive rectifier still applies.
func (s *GatewayService) thinkingHistoryCrossedAccounts(ctx context.Context, account *Account, parsed *ParsedRequest, mappedModel string) bool {
	if account == nil || account.Platform != PlatformAnthropic || !ShouldPreFilterThinkingBlocks(mappedModel) {
		return false
	}
	lineage, ok := s.cache.(ThinkingLineageCache)
	if !ok {
		return false
	}
	key := s.thinkingLineageKey(parsed)
	if key == "" {
		return false
	}
	var groupID int64
	if parsed.GroupID != nil {
		groupID = *parsed.GroupID
	}
	mixed, err := lineage.RecordThinkingAccount(ctx, groupID, key, account.ID, ThinkingLineageTTL)
	if err != nil {
		logger.LegacyPrintf("service.gateway", "[ThinkingLineage] record failed for account %d: %v", account.ID, err)
		return false
	}
	return mixed
}

// StripCrossAccountThinking removes historical thinking that another account
// signed. Claude 5.5 models accept a history with no thinking blocks while
// thinking stays on (the same rule FilterThinkingBlocks applies), so only the
// blocks are removed. Older models require the latest assistant turn's
// thinking during tool use, so they fall back to the retry downgrade, which
// keeps the reasoning as text and disables thinking for the request.
func StripCrossAccountThinking(body []byte, mappedModel string) []byte {
	if !ShouldPreFilterThinkingBlocks(mappedModel) {
		return body
	}
	if !isClaude55SignedThinkingModel(mappedModel) {
		return FilterThinkingBlocksForRetry(body, mappedModel)
	}
	return stripAllHistoricalThinking(body)
}

func stripAllHistoricalThinking(body []byte) []byte {
	msgs := gjson.GetBytes(body, "messages")
	if !msgs.IsArray() {
		return body
	}
	// UseNumber keeps large integers in tool inputs exact.
	decoder := json.NewDecoder(bytes.NewReader([]byte(msgs.Raw)))
	decoder.UseNumber()
	var messages []map[string]any
	if err := decoder.Decode(&messages); err != nil {
		return body
	}
	modified := false
	for _, msg := range messages {
		content, ok := msg["content"].([]any)
		if !ok {
			continue
		}
		kept := make([]any, 0, len(content))
		for _, block := range content {
			if blockMap, ok := block.(map[string]any); ok {
				blockType, _ := blockMap["type"].(string)
				_, untypedThinking := blockMap["thinking"]
				if blockType == "thinking" || blockType == "redacted_thinking" || (blockType == "" && untypedThinking) {
					continue
				}
			}
			kept = append(kept, block)
		}
		if len(kept) == len(content) {
			continue
		}
		modified = true
		if len(kept) == 0 {
			placeholder := "(content removed)"
			if role, _ := msg["role"].(string); role == "assistant" {
				placeholder = "(assistant content removed)"
			}
			kept = append(kept, map[string]any{"type": "text", "text": placeholder})
		}
		msg["content"] = kept
	}
	if !modified {
		return body
	}
	raw, err := json.Marshal(messages)
	if err != nil {
		return body
	}
	out, err := sjson.SetRawBytes(body, "messages", raw)
	if err != nil {
		return body
	}
	return out
}
