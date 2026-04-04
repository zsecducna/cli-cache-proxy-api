package helps

import (
	"strings"
	"sync"
	"time"
)

type CodexCache struct {
	ID     string
	Expire time.Time
}

// codexCacheMap stores prompt cache IDs keyed by model+user_id.
// Protected by codexCacheMu. Entries expire after 1 hour.
var (
	codexCacheMap = make(map[string]CodexCache)
	codexCacheMu  sync.RWMutex
)

// codexCacheCleanupInterval controls how often expired entries are purged.
const codexCacheCleanupInterval = 15 * time.Minute

// codexCacheCleanupOnce ensures the background cleanup goroutine starts only once.
var codexCacheCleanupOnce sync.Once

const codexResponsePromptCachePrefix = "response:"

// startCodexCacheCleanup launches a background goroutine that periodically
// removes expired entries from codexCacheMap to prevent memory leaks.
func startCodexCacheCleanup() {
	go func() {
		ticker := time.NewTicker(codexCacheCleanupInterval)
		defer ticker.Stop()
		for range ticker.C {
			purgeExpiredCodexCache()
		}
	}()
}

// purgeExpiredCodexCache removes entries that have expired.
func purgeExpiredCodexCache() {
	now := time.Now()
	codexCacheMu.Lock()
	defer codexCacheMu.Unlock()
	for key, cache := range codexCacheMap {
		if cache.Expire.Before(now) {
			delete(codexCacheMap, key)
		}
	}
}

// GetCodexCache retrieves a cached entry, returning ok=false if not found or expired.
func GetCodexCache(key string) (CodexCache, bool) {
	codexCacheCleanupOnce.Do(startCodexCacheCleanup)
	codexCacheMu.RLock()
	cache, ok := codexCacheMap[key]
	codexCacheMu.RUnlock()
	if !ok || cache.Expire.Before(time.Now()) {
		return CodexCache{}, false
	}
	return cache, true
}

// SetCodexCache stores a cache entry.
func SetCodexCache(key string, cache CodexCache) {
	codexCacheCleanupOnce.Do(startCodexCacheCleanup)
	codexCacheMu.Lock()
	codexCacheMap[key] = cache
	codexCacheMu.Unlock()
}

func GetCodexResponsePromptCacheKey(responseID string) (string, bool) {
	responseID = strings.TrimSpace(responseID)
	if responseID == "" {
		return "", false
	}
	cache, ok := GetCodexCache(codexResponsePromptCachePrefix + responseID)
	if !ok || strings.TrimSpace(cache.ID) == "" {
		return "", false
	}
	return cache.ID, true
}

func SetCodexResponsePromptCacheKey(responseID, promptCacheKey string, ttl time.Duration) {
	responseID = strings.TrimSpace(responseID)
	promptCacheKey = strings.TrimSpace(promptCacheKey)
	if responseID == "" || promptCacheKey == "" {
		return
	}
	if ttl <= 0 {
		ttl = time.Hour
	}
	SetCodexCache(codexResponsePromptCachePrefix+responseID, CodexCache{
		ID:     promptCacheKey,
		Expire: time.Now().Add(ttl),
	})
}
