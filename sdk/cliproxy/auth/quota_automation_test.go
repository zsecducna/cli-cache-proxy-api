package auth

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestManager_Register_AutoDisablesWhenFiveHourRemainingQuotaReachesThreshold(t *testing.T) {
	manager := NewManager(nil, nil, nil)

	auth := &Auth{
		ID:       "quota-5hrs.json",
		Provider: "codex",
		Status:   StatusActive,
		Metadata: map[string]any{
			"quota": map[string]any{
				"5hrs": map[string]any{
					"remaining_percent": 5,
				},
				"7days": map[string]any{
					"remaining_percent": 40,
				},
			},
		},
	}

	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	stored, ok := manager.GetByID(auth.ID)
	if !ok || stored == nil {
		t.Fatalf("expected auth %q to be registered", auth.ID)
	}
	if !stored.Disabled || stored.Status != StatusDisabled {
		t.Fatalf("expected auth to be auto-disabled, got disabled=%v status=%q", stored.Disabled, stored.Status)
	}
	if !quotaAutomationMarked(stored.Metadata) {
		t.Fatal("expected quota auto-disable marker to be persisted")
	}
	if reason := quotaAutoDisabledReason(stored.Metadata); !strings.Contains(reason, "5hrs remaining quota 5% <= 5%") {
		t.Fatalf("unexpected auto-disable reason: %q", reason)
	}
	if !strings.Contains(stored.StatusMessage, "5hrs remaining quota 5% <= 5%") {
		t.Fatalf("unexpected status message: %q", stored.StatusMessage)
	}
}

func TestManager_Register_UsesTemporaryCooldownWhenQuotaResetIsKnown(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	resetAt := time.Now().Add(time.Hour).UTC().Truncate(time.Second)

	auth := &Auth{
		ID:       "quota-reset.json",
		Provider: "codex",
		Status:   StatusActive,
		Metadata: map[string]any{
			"quota": map[string]any{
				"5hrs": map[string]any{
					"remaining_percent": 0,
					"reset_at":          resetAt.Format(time.RFC3339),
				},
				"7days": map[string]any{
					"remaining_percent": 50,
				},
			},
		},
	}

	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	stored, ok := manager.GetByID(auth.ID)
	if !ok || stored == nil {
		t.Fatalf("expected auth %q to be registered", auth.ID)
	}
	if stored.Disabled || stored.Status == StatusDisabled {
		t.Fatalf("expected auth to stay selector-cooldown only, got disabled=%v status=%q", stored.Disabled, stored.Status)
	}
	if !stored.Unavailable || stored.Status != StatusError {
		t.Fatalf("expected auth cooldown status, got unavailable=%v status=%q", stored.Unavailable, stored.Status)
	}
	if !stored.Quota.Exceeded || stored.NextRetryAfter.IsZero() || !stored.Quota.NextRecoverAt.Equal(resetAt) {
		t.Fatalf("expected quota cooldown until %v, got quota=%+v next_retry_after=%v", resetAt, stored.Quota, stored.NextRetryAfter)
	}
	if !quotaAutoCooldownMarked(stored.Metadata) {
		t.Fatal("expected quota cooldown marker")
	}
	if quotaAutomationMarked(stored.Metadata) {
		t.Fatal("did not expect permanent auto-disable marker")
	}
}

func TestManager_Register_UsesTemporaryCooldownForCodexWhamWeeklyWindow(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	fiveHourReset := time.Now().Add(5 * time.Hour).UTC().Truncate(time.Second)
	weeklyReset := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Second)

	auth := &Auth{
		ID:       "codex-wham-weekly.json",
		Provider: "codex",
		Status:   StatusActive,
		Metadata: map[string]any{
			"quota_usage": map[string]any{
				"rate_limit": map[string]any{
					"allowed":       false,
					"limit_reached": true,
					"primary_window": map[string]any{
						"limit_window_seconds": 18000,
						"used_percent":         0,
						"reset_at":             fiveHourReset.Unix(),
					},
					"secondary_window": map[string]any{
						"limit_window_seconds": 604800,
						"used_percent":         100,
						"reset_at":             weeklyReset.Unix(),
					},
				},
			},
		},
	}

	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	stored, ok := manager.GetByID(auth.ID)
	if !ok || stored == nil {
		t.Fatalf("expected auth %q to be registered", auth.ID)
	}
	if stored.Disabled || stored.Status == StatusDisabled {
		t.Fatalf("expected temporary cooldown, got disabled=%v status=%q", stored.Disabled, stored.Status)
	}
	if !stored.Unavailable || !stored.Quota.Exceeded || !stored.NextRetryAfter.Equal(weeklyReset) {
		t.Fatalf("expected weekly cooldown until %v, got unavailable=%v quota=%+v next_retry_after=%v", weeklyReset, stored.Unavailable, stored.Quota, stored.NextRetryAfter)
	}
	if reason := quotaAutoCooldownReason(stored.Metadata); !strings.Contains(reason, "7days quota exhausted") {
		t.Fatalf("expected weekly cooldown reason, got %q", reason)
	}
}

func TestManager_Register_UsesTemporaryCooldownForCodexWhamFiveHourWindow(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	resetAt := time.Now().Add(5 * time.Hour).UTC().Truncate(time.Second)

	auth := &Auth{
		ID:       "codex-wham-five-hour.json",
		Provider: "codex",
		Status:   StatusActive,
		Metadata: map[string]any{
			"quota_usage": map[string]any{
				"rate_limit": map[string]any{
					"primary_window": map[string]any{
						"limit_window_seconds": 18000,
						"used_percent":         100,
						"reset_at":             resetAt.Unix(),
					},
					"secondary_window": map[string]any{
						"limit_window_seconds": 604800,
						"used_percent":         50,
					},
				},
			},
		},
	}

	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	stored, ok := manager.GetByID(auth.ID)
	if !ok || stored == nil {
		t.Fatalf("expected auth %q to be registered", auth.ID)
	}
	if !stored.Unavailable || !stored.Quota.Exceeded || !stored.NextRetryAfter.Equal(resetAt) {
		t.Fatalf("expected 5hrs cooldown until %v, got unavailable=%v quota=%+v next_retry_after=%v", resetAt, stored.Unavailable, stored.Quota, stored.NextRetryAfter)
	}
	if reason := quotaAutoCooldownReason(stored.Metadata); !strings.Contains(reason, "5hrs quota exhausted") {
		t.Fatalf("expected 5hrs cooldown reason, got %q", reason)
	}
}

func TestQuotaAutomationSnapshot_PrefersDefaultRateLimitBeforeAdditionalLimits(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	defaultWeeklyReset := now.Add(7 * 24 * time.Hour)

	metadata := map[string]any{
		"quota_usage": map[string]any{
			"rate_limit": map[string]any{
				"allowed":       false,
				"limit_reached": true,
				"primary_window": map[string]any{
					"limit_window_seconds": 18000,
					"used_percent":         20,
				},
				"secondary_window": map[string]any{
					"limit_window_seconds": 604800,
					"used_percent":         100,
					"reset_at":             defaultWeeklyReset.Unix(),
				},
			},
			"additional_rate_limits": []any{
				map[string]any{
					"limit_name": "GPT-5.3-Codex-Spark",
					"rate_limit": map[string]any{
						"allowed": true,
						"primary_window": map[string]any{
							"limit_window_seconds": 18000,
							"used_percent":         8,
						},
						"secondary_window": map[string]any{
							"limit_window_seconds": 604800,
							"used_percent":         60,
						},
					},
				},
			},
		},
	}

	snapshot := readQuotaAutomationSnapshotAt(metadata, now)
	reason, resetAt, ok := quotaExhaustionCooldown(snapshot, now)
	if !ok {
		t.Fatal("expected default rate_limit weekly cooldown")
	}
	if !resetAt.Equal(defaultWeeklyReset) || !strings.Contains(reason, "7days quota exhausted") {
		t.Fatalf("expected default weekly cooldown until %v, got reason=%q reset=%v", defaultWeeklyReset, reason, resetAt)
	}
}

func TestManager_Register_DoesNotCooldownAfterKnownResetPassed(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	resetAt := time.Now().Add(-time.Minute).UTC().Truncate(time.Second)

	auth := &Auth{
		ID:       "quota-reset-passed.json",
		Provider: "codex",
		Status:   StatusActive,
		Metadata: map[string]any{
			"quota": map[string]any{
				"5hrs": map[string]any{
					"remaining_percent": 0,
					"reset_at":          resetAt.Format(time.RFC3339),
				},
				"7days": map[string]any{
					"remaining_percent": 50,
				},
			},
		},
	}

	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	stored, ok := manager.GetByID(auth.ID)
	if !ok || stored == nil {
		t.Fatalf("expected auth %q to be registered", auth.ID)
	}
	if stored.Disabled || stored.Unavailable || stored.Status != StatusActive {
		t.Fatalf("expected auth ready after reset passed, got disabled=%v unavailable=%v status=%q", stored.Disabled, stored.Unavailable, stored.Status)
	}
	if quotaAutoCooldownMarked(stored.Metadata) || quotaAutomationMarked(stored.Metadata) {
		t.Fatalf("did not expect quota automation marker after reset passed: %+v", stored.Metadata)
	}
}

func TestManager_Update_DoesNotReenableWhenSevenDayQuotaRemainsAtThreshold(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	ctx := context.Background()

	if _, err := manager.Register(ctx, &Auth{
		ID:       "quota-7days.json",
		Provider: "codex",
		Status:   StatusActive,
		Metadata: map[string]any{
			"quota": map[string]any{
				"5hrs": map[string]any{
					"remaining_percent": 100,
				},
				"7days": map[string]any{
					"remaining_percent": 2.5,
				},
			},
		},
	}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	if _, err := manager.Update(ctx, &Auth{
		ID:       "quota-7days.json",
		Provider: "codex",
		Status:   StatusActive,
		Metadata: map[string]any{
			"quota": map[string]any{
				"5hrs": map[string]any{
					"remaining_percent": 100,
				},
				"7days": map[string]any{
					"remaining_percent": 3,
				},
			},
		},
	}); err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	stored, ok := manager.GetByID("quota-7days.json")
	if !ok || stored == nil {
		t.Fatal("expected auth to remain present after update")
	}
	if !stored.Disabled || stored.Status != StatusDisabled {
		t.Fatalf("expected auth to remain auto-disabled, got disabled=%v status=%q", stored.Disabled, stored.Status)
	}
	if !quotaAutomationMarked(stored.Metadata) {
		t.Fatal("expected auto-disable marker to remain while 7days quota is still at threshold")
	}
	if reason := quotaAutoDisabledReason(stored.Metadata); !strings.Contains(reason, "7days remaining quota 3% <= 3%") {
		t.Fatalf("unexpected auto-disable reason after update: %q", reason)
	}
}

func TestManager_Update_ReenablesOnlyWhenBothQuotaWindowsRecover(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	ctx := context.Background()

	if _, err := manager.Register(ctx, &Auth{
		ID:       "quota-reenable.json",
		Provider: "codex",
		Status:   StatusActive,
		Metadata: map[string]any{
			"quota": map[string]any{
				"5hrs": map[string]any{
					"remaining_percent": 4.9,
				},
				"7days": map[string]any{
					"remaining_percent": 20,
				},
			},
		},
	}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	if _, err := manager.Update(ctx, &Auth{
		ID:       "quota-reenable.json",
		Provider: "codex",
		Status:   StatusActive,
		Metadata: map[string]any{
			"quota": map[string]any{
				"5hrs": map[string]any{
					"remaining_percent": 100,
				},
			},
		},
	}); err != nil {
		t.Fatalf("Update() with missing 7days quota error = %v", err)
	}

	stored, ok := manager.GetByID("quota-reenable.json")
	if !ok || stored == nil {
		t.Fatal("expected auth after partial recovery update")
	}
	if !stored.Disabled {
		t.Fatal("expected auth to remain disabled when 7days quota is missing")
	}

	if _, err := manager.Update(ctx, &Auth{
		ID:       "quota-reenable.json",
		Provider: "codex",
		Status:   StatusActive,
		Metadata: map[string]any{
			"quota": map[string]any{
				"5hrs": map[string]any{
					"remaining_percent": 6,
				},
				"7days": map[string]any{
					"remaining_percent": 3.1,
				},
			},
		},
	}); err != nil {
		t.Fatalf("Update() with recovered quotas error = %v", err)
	}

	stored, ok = manager.GetByID("quota-reenable.json")
	if !ok || stored == nil {
		t.Fatal("expected auth after recovery update")
	}
	if stored.Disabled || stored.Status != StatusActive {
		t.Fatalf("expected auth to be re-enabled, got disabled=%v status=%q", stored.Disabled, stored.Status)
	}
	if quotaAutomationMarked(stored.Metadata) {
		t.Fatal("expected auto-disable marker to be cleared after recovery")
	}
	if reason := quotaAutoDisabledReason(stored.Metadata); reason != "" {
		t.Fatalf("expected auto-disable reason to be cleared, got %q", reason)
	}
	if stored.StatusMessage != "" {
		t.Fatalf("expected status message to be cleared after recovery, got %q", stored.StatusMessage)
	}
}
