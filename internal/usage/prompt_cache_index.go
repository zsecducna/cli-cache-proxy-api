package usage

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

func (s *CacheStatisticsStore) initPromptCacheIndex() error {
	if s == nil || s.db == nil {
		return fmt.Errorf("cache statistics store: not initialized")
	}
	if s.isPostgres() {
		return s.initPostgresSchema()
	}
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS prompt_cache_response_index (
    response_id TEXT PRIMARY KEY,
    prompt_cache_key TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_prompt_cache_response_index_expires_at ON prompt_cache_response_index(expires_at);
`)
	if err != nil {
		return fmt.Errorf("cache statistics store: init prompt cache index: %w", err)
	}
	return nil
}

func (s *CacheStatisticsStore) LookupPromptCacheKeyByResponseID(ctx context.Context, responseID string) (string, bool, error) {
	if s == nil || s.db == nil {
		return "", false, nil
	}
	responseID = strings.TrimSpace(responseID)
	if responseID == "" {
		return "", false, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := s.deleteExpiredPromptCacheKeys(ctx, time.Now().UTC()); err != nil {
		return "", false, err
	}

	var promptCacheKey string
	var expiresAtRaw any
	row := s.db.QueryRowContext(ctx, fmt.Sprintf(`
SELECT prompt_cache_key, expires_at
FROM %s
WHERE response_id = %s`, s.promptCacheTableName(), s.bind(1)), responseID)
	if err := row.Scan(&promptCacheKey, &expiresAtRaw); err != nil {
		if err == sql.ErrNoRows {
			return "", false, nil
		}
		return "", false, fmt.Errorf("cache statistics store: lookup prompt cache key: %w", err)
	}

	expiresAt, err := scanCacheStatisticsTime(expiresAtRaw)
	if err != nil {
		return "", false, fmt.Errorf("cache statistics store: parse prompt cache expiry: %w", err)
	}

	if !expiresAt.After(time.Now().UTC()) {
		_, _ = s.db.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %s WHERE response_id = %s`, s.promptCacheTableName(), s.bind(1)), responseID)
		return "", false, nil
	}
	promptCacheKey = strings.TrimSpace(promptCacheKey)
	if promptCacheKey == "" {
		return "", false, nil
	}
	return promptCacheKey, true, nil
}

func (s *CacheStatisticsStore) UpsertPromptCacheKeyByResponseID(ctx context.Context, responseID, promptCacheKey string, ttl time.Duration) error {
	if s == nil || s.db == nil {
		return nil
	}
	responseID = strings.TrimSpace(responseID)
	promptCacheKey = strings.TrimSpace(promptCacheKey)
	if responseID == "" || promptCacheKey == "" {
		return nil
	}
	if ttl <= 0 {
		ttl = time.Hour
	}
	if ctx == nil {
		ctx = context.Background()
	}
	now := time.Now().UTC()
	expiresAt := now.Add(ttl)
	if err := s.upsertPromptCacheKeyByResponseID(ctx, responseID, promptCacheKey, expiresAt, now); err != nil {
		return err
	}
	_ = s.deleteExpiredPromptCacheKeys(ctx, now)
	return nil
}

func (s *CacheStatisticsStore) upsertPromptCacheKeyByResponseID(ctx context.Context, responseID, promptCacheKey string, expiresAt, updatedAt time.Time) error {
	if s == nil || s.db == nil {
		return nil
	}
	responseID = strings.TrimSpace(responseID)
	promptCacheKey = strings.TrimSpace(promptCacheKey)
	if responseID == "" || promptCacheKey == "" {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if expiresAt.IsZero() {
		expiresAt = time.Now().UTC().Add(time.Hour)
	}
	if updatedAt.IsZero() {
		updatedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, fmt.Sprintf(`
INSERT INTO %s (response_id, prompt_cache_key, expires_at, updated_at)
VALUES (%s, %s, %s, %s)
ON CONFLICT(response_id) DO UPDATE SET
    prompt_cache_key = excluded.prompt_cache_key,
    expires_at = excluded.expires_at,
    updated_at = excluded.updated_at
`, s.promptCacheTableName(), s.bind(1), s.bind(2), s.bind(3), s.bind(4)),
		responseID,
		promptCacheKey,
		s.timestampArg(expiresAt),
		s.timestampArg(updatedAt),
	)
	if err != nil {
		return fmt.Errorf("cache statistics store: upsert prompt cache key: %w", err)
	}
	return nil
}

func (s *CacheStatisticsStore) deleteExpiredPromptCacheKeys(ctx context.Context, now time.Time) error {
	if s == nil || s.db == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %s WHERE expires_at <= %s`, s.promptCacheTableName(), s.bind(1)), s.timestampArg(now))
	if err != nil {
		return fmt.Errorf("cache statistics store: delete expired prompt cache keys: %w", err)
	}
	return nil
}

func LookupPromptCacheKeyByResponseID(responseID string) (string, bool) {
	store := GetCacheStatisticsStore()
	if store == nil {
		return "", false
	}
	key, ok, err := store.LookupPromptCacheKeyByResponseID(context.Background(), responseID)
	if err != nil {
		return "", false
	}
	return key, ok
}

func RememberPromptCacheKeyForResponse(responseID, promptCacheKey string, ttl time.Duration) {
	store := GetCacheStatisticsStore()
	if store == nil {
		return
	}
	_ = store.UpsertPromptCacheKeyByResponseID(context.Background(), responseID, promptCacheKey, ttl)
}
