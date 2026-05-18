package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tiktoken-go/tokenizer"
)

type anthropicCacheSimulator struct {
	mu           sync.Mutex
	seenPrefixes map[string]struct{}
}

func newAnthropicCacheSimulator() *anthropicCacheSimulator {
	return &anthropicCacheSimulator{
		seenPrefixes: make(map[string]struct{}),
	}
}

func (s *anthropicCacheSimulator) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_ = r.Body.Close()

	totalTokens := estimateAnthropicPromptTokens(body)
	prefixKey, prefixTokens := anthropicCachedPrefix(body)

	var cachedTokens int64
	var createdTokens int64

	if prefixKey != "" && prefixTokens > 0 {
		s.mu.Lock()
		if _, ok := s.seenPrefixes[prefixKey]; ok {
			cachedTokens = prefixTokens
		} else {
			s.seenPrefixes[prefixKey] = struct{}{}
			createdTokens = prefixTokens
		}
		s.mu.Unlock()
	}

	inputTokens := totalTokens - cachedTokens
	if inputTokens < 0 {
		inputTokens = 0
	}

	w.Header().Set("Content-Type", "application/json")
	_, _ = fmt.Fprintf(w, `{"id":"msg_cache","type":"message","model":"claude-sonnet-4-6","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":%d,"output_tokens":7,"cache_creation_input_tokens":%d,"cache_read_input_tokens":%d}}`,
		inputTokens,
		createdTokens,
		cachedTokens,
	)
}

func anthropicCachedPrefix(payload []byte) (string, int64) {
	segments := collectAnthropicCachedPrefixSegments(gjson.ParseBytes(payload))
	joined := strings.TrimSpace(strings.Join(segments, "\n"))
	if joined == "" {
		return "", 0
	}

	enc, err := tokenizer.Get(tokenizer.O200kBase)
	if err != nil {
		return joined, int64(len(joined) / 4)
	}
	count, err := enc.Count(joined)
	if err != nil {
		return joined, int64(len(joined) / 4)
	}
	return joined, int64(count)
}

func collectAnthropicCachedPrefixSegments(root gjson.Result) []string {
	segments := make([]string, 0, 32)
	appendAnthropicCachedToolSegments(root.Get("tools"), &segments)
	appendAnthropicCachedSystemSegments(root.Get("system"), &segments)
	appendAnthropicCachedMessageSegments(root.Get("messages"), &segments)
	return segments
}

func appendAnthropicCachedToolSegments(tools gjson.Result, segments *[]string) {
	if !tools.Exists() || !tools.IsArray() {
		return
	}
	for _, tool := range tools.Array() {
		appendAnthropicSegment(segments, tool.Get("name").String())
		appendAnthropicSegment(segments, tool.Get("description").String())
		if inputSchema := tool.Get("input_schema"); inputSchema.Exists() {
			appendAnthropicSegment(segments, inputSchema.Raw)
		}
		if schema := tool.Get("schema"); schema.Exists() {
			appendAnthropicSegment(segments, schema.Raw)
		}
		if tool.Get("cache_control").Exists() {
			return
		}
	}
}

func appendAnthropicCachedSystemSegments(system gjson.Result, segments *[]string) {
	if !system.Exists() {
		return
	}
	if system.Type == gjson.String {
		appendAnthropicSegment(segments, system.String())
		return
	}
	if !system.IsArray() {
		appendAnthropicSegment(segments, system.Raw)
		return
	}
	for _, part := range system.Array() {
		if text := part.Get("text"); text.Exists() {
			appendAnthropicSegment(segments, text.String())
		} else {
			appendAnthropicSegment(segments, part.Raw)
		}
		if part.Get("cache_control").Exists() {
			return
		}
	}
}

func appendAnthropicCachedMessageSegments(messages gjson.Result, segments *[]string) {
	if !messages.Exists() || !messages.IsArray() {
		return
	}
	for _, msg := range messages.Array() {
		content := msg.Get("content")
		if !content.Exists() {
			continue
		}
		if content.Type == gjson.String {
			appendAnthropicSegment(segments, content.String())
			continue
		}
		if !content.IsArray() {
			appendAnthropicSegment(segments, content.Raw)
			continue
		}
		for _, part := range content.Array() {
			collectAnthropicContentPart(part, segments)
			if part.Get("cache_control").Exists() {
				return
			}
		}
	}
}

func newClaudeBenchmarkExecutor(t testing.TB) (*ClaudeExecutor, *cliproxyauth.Auth, *httptest.Server) {
	t.Helper()

	cacheEnabled := true
	server := httptest.NewServer(newAnthropicCacheSimulator())

	executor := NewClaudeExecutor(&config.Config{
		ClaudeKey: []config.ClaudeKey{
			{
				APIKey:  "bench-key",
				BaseURL: server.URL,
				Cloak: &config.CloakConfig{
					Mode:        "never",
					CacheUserID: &cacheEnabled,
				},
			},
		},
	})
	auth := &cliproxyauth.Auth{
		Provider: "claude",
		Attributes: map[string]string{
			"api_key":    "bench-key",
			"base_url":   server.URL,
			"cloak_mode": "never",
		},
	}
	return executor, auth, server
}

func multiTurnCacheBenchmarkPayload(lastUser string) []byte {
	large := cacheEligibleAnthropicText()
	return []byte(fmt.Sprintf(`{
		"model":"claude-sonnet-4-6",
		"tools":[
			{"name":"Read","description":%q,"input_schema":{"type":"object","properties":{"path":{"type":"string"}}}},
			{"name":"Write","description":%q,"input_schema":{"type":"object","properties":{"path":{"type":"string"},"content":{"type":"string"}}}}
		],
		"system":[
			{"type":"text","text":%q},
			{"type":"text","text":"Keep answers precise and action-oriented."}
		],
		"messages":[
			{"role":"user","content":[{"type":"text","text":%q}]},
			{"role":"assistant","content":[{"type":"text","text":"I inspected the repository and noted the relevant files."}]},
			{"role":"user","content":[{"type":"text","text":%q}]},
			{"role":"assistant","content":[{"type":"text","text":"I updated the patch set and prepared the next step."}]},
			{"role":"user","content":[{"type":"text","text":%q}]}
		]
	}`,
		large,
		large,
		large,
		large,
		large,
		lastUser,
	))
}

func executeClaudeBenchmarkRequest(t testing.TB, executor *ClaudeExecutor, auth *cliproxyauth.Auth, payload []byte) []byte {
	t.Helper()

	resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-sonnet-4-6",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatClaude,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	return resp.Payload
}

func cacheHitRateFromClaudeResponse(t testing.TB, payload []byte) float64 {
	t.Helper()

	inputTokens := gjson.GetBytes(payload, "usage.input_tokens").Int()
	cachedTokens := gjson.GetBytes(payload, "usage.cache_read_input_tokens").Int()
	totalPromptTokens := inputTokens + cachedTokens
	if totalPromptTokens <= 0 {
		t.Fatalf("expected prompt tokens in usage payload, got %s", string(payload))
	}
	return float64(cachedTokens) / float64(totalPromptTokens)
}

func TestClaudeExecutor_MultiTurnCacheHitRateExceedsNinetyPercent(t *testing.T) {
	t.Parallel()

	executor, auth, server := newClaudeBenchmarkExecutor(t)
	defer server.Close()

	_ = executeClaudeBenchmarkRequest(t, executor, auth, multiTurnCacheBenchmarkPayload("Warm the cache for this established coding session."))

	const measuredTurns = 5
	var aggregate float64
	for turn := 0; turn < measuredTurns; turn++ {
		payload := multiTurnCacheBenchmarkPayload(fmt.Sprintf("Continue iteration %d with one more small change.", turn+1))
		resp := executeClaudeBenchmarkRequest(t, executor, auth, payload)
		aggregate += cacheHitRateFromClaudeResponse(t, resp)
	}

	averageHitRate := aggregate / measuredTurns
	if averageHitRate <= 0.90 {
		t.Fatalf("average multi-turn cache hit rate = %.2f%%, want > 90%%", averageHitRate*100)
	}
}

func BenchmarkClaudeExecutor_MultiTurnCacheHitRate(b *testing.B) {
	executor, auth, server := newClaudeBenchmarkExecutor(b)
	defer server.Close()

	_ = executeClaudeBenchmarkRequest(b, executor, auth, multiTurnCacheBenchmarkPayload("Warm the cache for benchmark traffic."))

	var aggregate float64
	b.ResetTimer()
	for turn := 0; turn < b.N; turn++ {
		payload := multiTurnCacheBenchmarkPayload(fmt.Sprintf("Benchmark continuation turn %d.", turn))
		resp := executeClaudeBenchmarkRequest(b, executor, auth, payload)
		aggregate += cacheHitRateFromClaudeResponse(b, resp)
	}
	b.StopTimer()

	averageHitRate := aggregate / float64(b.N)
	b.ReportMetric(averageHitRate*100, "cache_hit_%")
	if averageHitRate <= 0.90 {
		b.Fatalf("average multi-turn cache hit rate = %.2f%%, want > 90%%", averageHitRate*100)
	}
}
