package misc

import (
	"context"
	"testing"
	"time"
)

func TestRefreshAntigravityVersionKeepsLastUsedVersionWhenStaleFetchFails(t *testing.T) {
	restoreAntigravityVersionState(t)

	const lastUsedVersion = "9.8.7"
	antigravityVersionMu.Lock()
	cachedAntigravityVersion = lastUsedVersion
	antigravityVersionExpiry = time.Now().Add(-time.Hour)
	antigravityVersionMu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	refreshAntigravityVersion(ctx)

	if got := AntigravityLatestVersion(); got != lastUsedVersion {
		t.Fatalf("AntigravityLatestVersion() = %q, want last used %q", got, lastUsedVersion)
	}
}

func TestRefreshAntigravityVersionUsesFallbackWhenNoLastUsedVersionExists(t *testing.T) {
	restoreAntigravityVersionState(t)

	antigravityVersionMu.Lock()
	cachedAntigravityVersion = ""
	antigravityVersionExpiry = time.Time{}
	antigravityVersionMu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	refreshAntigravityVersion(ctx)

	if got := AntigravityLatestVersion(); got != antigravityFallbackVersion {
		t.Fatalf("AntigravityLatestVersion() = %q, want fallback %q", got, antigravityFallbackVersion)
	}
}

func restoreAntigravityVersionState(t *testing.T) {
	t.Helper()

	antigravityVersionMu.Lock()
	originalVersion := cachedAntigravityVersion
	originalExpiry := antigravityVersionExpiry
	antigravityVersionMu.Unlock()

	t.Cleanup(func() {
		antigravityVersionMu.Lock()
		cachedAntigravityVersion = originalVersion
		antigravityVersionExpiry = originalExpiry
		antigravityVersionMu.Unlock()
	})
}
