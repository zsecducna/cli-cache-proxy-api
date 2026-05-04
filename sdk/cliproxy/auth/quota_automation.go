package auth

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const (
	quotaAutoDisable5HoursThreshold = 5.0
	quotaAutoDisable7DaysThreshold  = 3.0

	quotaAutoDisabledMetadataKey       = "quota_auto_disabled"
	quotaAutoDisabledAtMetadataKey     = "quota_auto_disabled_at"
	quotaAutoDisabledReasonMetadataKey = "quota_auto_disabled_reason"

	quotaAutoCooldownMetadataKey       = "quota_auto_cooldown"
	quotaAutoCooldownUntilMetadataKey  = "quota_auto_cooldown_until"
	quotaAutoCooldownReasonMetadataKey = "quota_auto_cooldown_reason"

	quotaAutoDisabledStatusPrefix = "disabled automatically due to low remaining quota"
	quotaAutoCooldownStatusPrefix = "temporarily disabled due to exhausted quota"
)

var (
	fiveHourQuotaWindowAliases = normalizedQuotaKeySet(
		"5hrs",
		"5hr",
		"5h",
		"5hours",
		"5hour",
		"5_hours",
		"5_hour",
		"fivehours",
		"fivehour",
		"five_hours",
		"five_hour",
	)
	sevenDayQuotaWindowAliases = normalizedQuotaKeySet(
		"7days",
		"7day",
		"7d",
		"7_days",
		"7_day",
		"sevendays",
		"sevenday",
		"seven_days",
		"seven_day",
	)
	quotaRemainingPercentKeys = normalizedQuotaKeySet(
		"remaining_percent",
		"remaining_pct",
		"remaining_percentage",
		"remainingPercent",
		"remainingPct",
		"remainingPercentage",
		"percent_remaining",
		"percentRemaining",
		"quota_remaining_percent",
		"quotaRemainingPercent",
		"quota_remaining_pct",
		"quotaRemainingPct",
	)
	quotaRemainingRatioKeys = normalizedQuotaKeySet(
		"remaining_ratio",
		"remainingRatio",
		"remaining_fraction",
		"remainingFraction",
	)
	quotaRemainingContainerKeys = normalizedQuotaKeySet(
		"remaining",
		"quota_remaining",
		"quotaRemaining",
	)
	quotaBarePercentKeys = normalizedQuotaKeySet(
		"percent",
		"pct",
		"percentage",
		"value",
	)
	quotaBareRatioKeys = normalizedQuotaKeySet(
		"ratio",
		"fraction",
	)
	quotaResetTimeKeys = normalizedQuotaKeySet(
		"reset_at",
		"resetAt",
		"resets_at",
		"resetsAt",
		"reset_time",
		"resetTime",
		"next_reset_at",
		"nextResetAt",
		"quota_reset_at",
		"quotaResetAt",
	)
	quotaResetSecondsKeys = normalizedQuotaKeySet(
		"reset_seconds",
		"resetSeconds",
		"resets_in_seconds",
		"resetsInSeconds",
		"reset_in_seconds",
		"resetInSeconds",
		"seconds_until_reset",
		"secondsUntilReset",
		"retry_after_seconds",
		"retryAfterSeconds",
	)
	quotaWindowTagKeys = normalizedQuotaKeySet(
		"window",
		"period",
		"range",
		"name",
		"label",
		"bucket",
	)
)

type quotaAutomationSnapshot struct {
	fiveHoursRemainingPercent *float64
	sevenDaysRemainingPercent *float64
	fiveHoursResetAt          *time.Time
	sevenDaysResetAt          *time.Time
}

func applyQuotaAutomation(auth *Auth, existing *Auth) {
	if auth == nil {
		return
	}

	autoDisabled := quotaAutomationMarked(auth.Metadata)
	if !autoDisabled && existing != nil {
		autoDisabled = quotaAutomationMarked(existing.Metadata)
	}
	autoCooldown := quotaAutoCooldownMarked(auth.Metadata)
	if !autoCooldown && existing != nil {
		autoCooldown = quotaAutoCooldownMarked(existing.Metadata)
	}
	if auth.Disabled && !autoDisabled && !autoCooldown {
		return
	}

	now := time.Now().UTC()
	snapshot := readQuotaAutomationSnapshotAt(auth.Metadata, now)
	if reason, resetAt, ok := quotaExhaustionCooldown(snapshot, now); ok {
		markQuotaAutoCooldown(auth, reason, resetAt)
		return
	}
	if autoCooldown {
		clearQuotaAutoCooldown(auth)
	}
	if autoDisabled && quotaSnapshotHasReset(snapshot) {
		clearQuotaAutoDisabled(auth)
		return
	}
	shouldDisable, reason := shouldAutoDisableFromQuota(auth, snapshot, autoDisabled)
	if shouldDisable {
		markQuotaAutoDisabled(auth, existing, reason)
		return
	}

	if !autoDisabled {
		return
	}

	if quotaWindowsRecovered(snapshot) {
		clearQuotaAutoDisabled(auth)
		return
	}

	markQuotaAutoDisabled(auth, existing, quotaAutoDisableReason(snapshot))
}

func shouldAutoDisableFromQuota(auth *Auth, snapshot quotaAutomationSnapshot, autoDisabled bool) (bool, string) {
	if auth == nil {
		return false, ""
	}
	if auth.Disabled && !autoDisabled {
		return false, ""
	}
	reason := quotaAutoDisableReason(snapshot)
	return reason != "", reason
}

func quotaWindowsRecovered(snapshot quotaAutomationSnapshot) bool {
	if snapshot.fiveHoursRemainingPercent == nil || snapshot.sevenDaysRemainingPercent == nil {
		return false
	}
	return *snapshot.fiveHoursRemainingPercent > quotaAutoDisable5HoursThreshold &&
		*snapshot.sevenDaysRemainingPercent > quotaAutoDisable7DaysThreshold
}

func quotaAutoDisableReason(snapshot quotaAutomationSnapshot) string {
	reasons := make([]string, 0, 2)
	if snapshot.fiveHoursResetAt == nil && snapshot.fiveHoursRemainingPercent != nil && *snapshot.fiveHoursRemainingPercent <= quotaAutoDisable5HoursThreshold {
		reasons = append(reasons, fmt.Sprintf("5hrs remaining quota %s%% <= %s%%", formatQuotaPercent(*snapshot.fiveHoursRemainingPercent), formatQuotaPercent(quotaAutoDisable5HoursThreshold)))
	}
	if snapshot.sevenDaysResetAt == nil && snapshot.sevenDaysRemainingPercent != nil && *snapshot.sevenDaysRemainingPercent <= quotaAutoDisable7DaysThreshold {
		reasons = append(reasons, fmt.Sprintf("7days remaining quota %s%% <= %s%%", formatQuotaPercent(*snapshot.sevenDaysRemainingPercent), formatQuotaPercent(quotaAutoDisable7DaysThreshold)))
	}
	return strings.Join(reasons, "; ")
}

func quotaExhaustionCooldown(snapshot quotaAutomationSnapshot, now time.Time) (string, time.Time, bool) {
	if reason, resetAt, ok := quotaWindowExhaustionCooldown("7days", snapshot.sevenDaysRemainingPercent, snapshot.sevenDaysResetAt, now); ok {
		return reason, resetAt, true
	}
	return quotaWindowExhaustionCooldown("5hrs", snapshot.fiveHoursRemainingPercent, snapshot.fiveHoursResetAt, now)
}

func quotaWindowExhaustionCooldown(label string, remaining *float64, resetAt *time.Time, now time.Time) (string, time.Time, bool) {
	if remaining == nil || *remaining > 0 || resetAt == nil {
		return "", time.Time{}, false
	}
	reset := resetAt.UTC()
	if !reset.After(now) {
		return "", time.Time{}, false
	}
	reason := fmt.Sprintf("%s quota exhausted; reset at %s", label, reset.Format(time.RFC3339))
	return reason, reset, true
}

func quotaSnapshotHasReset(snapshot quotaAutomationSnapshot) bool {
	return snapshot.fiveHoursResetAt != nil || snapshot.sevenDaysResetAt != nil
}

func quotaAutomationCooldown(auth *Auth, now time.Time) (string, time.Time, bool) {
	if auth == nil {
		return "", time.Time{}, false
	}
	snapshot := readQuotaAutomationSnapshotAt(auth.Metadata, now)
	if reason, resetAt, ok := quotaExhaustionCooldown(snapshot, now); ok {
		return reason, resetAt, true
	}
	if resetAt, ok := quotaAutoCooldownUntil(auth.Metadata); ok && resetAt.After(now) {
		reason := quotaAutoCooldownReason(auth.Metadata)
		if reason == "" {
			reason = "quota exhausted"
		}
		return reason, resetAt, true
	}
	return "", time.Time{}, false
}

func markQuotaAutoDisabled(auth *Auth, existing *Auth, reason string) {
	if auth == nil {
		return
	}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}

	if reason == "" {
		reason = quotaAutoDisableReason(readQuotaAutomationSnapshot(auth.Metadata))
	}
	if reason == "" && existing != nil {
		reason = quotaAutoDisabledReason(existing.Metadata)
	}

	auth.Metadata["disabled"] = true
	auth.Metadata[quotaAutoDisabledMetadataKey] = true
	if reason != "" {
		auth.Metadata[quotaAutoDisabledReasonMetadataKey] = reason
	} else {
		delete(auth.Metadata, quotaAutoDisabledReasonMetadataKey)
	}

	if existingAt := quotaAutoDisabledAt(auth.Metadata); existingAt != "" {
		auth.Metadata[quotaAutoDisabledAtMetadataKey] = existingAt
	} else if existing != nil {
		if priorAt := quotaAutoDisabledAt(existing.Metadata); priorAt != "" {
			auth.Metadata[quotaAutoDisabledAtMetadataKey] = priorAt
		} else {
			auth.Metadata[quotaAutoDisabledAtMetadataKey] = time.Now().UTC().Format(time.RFC3339Nano)
		}
	} else {
		auth.Metadata[quotaAutoDisabledAtMetadataKey] = time.Now().UTC().Format(time.RFC3339Nano)
	}

	auth.Disabled = true
	auth.Status = StatusDisabled
	auth.StatusMessage = quotaAutoDisabledStatusMessage(reason)
}

func clearQuotaAutoDisabled(auth *Auth) {
	if auth == nil {
		return
	}
	if auth.Metadata != nil {
		auth.Metadata["disabled"] = false
		delete(auth.Metadata, quotaAutoDisabledMetadataKey)
		delete(auth.Metadata, quotaAutoDisabledAtMetadataKey)
		delete(auth.Metadata, quotaAutoDisabledReasonMetadataKey)
	}
	auth.Disabled = false
	auth.Status = StatusActive
	auth.StatusMessage = ""
}

func markQuotaAutoCooldown(auth *Auth, reason string, resetAt time.Time) {
	if auth == nil {
		return
	}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	resetAt = resetAt.UTC()
	auth.Metadata["disabled"] = false
	auth.Metadata[quotaAutoCooldownMetadataKey] = true
	auth.Metadata[quotaAutoCooldownUntilMetadataKey] = resetAt.Format(time.RFC3339Nano)
	if reason != "" {
		auth.Metadata[quotaAutoCooldownReasonMetadataKey] = reason
	} else {
		delete(auth.Metadata, quotaAutoCooldownReasonMetadataKey)
	}
	delete(auth.Metadata, quotaAutoDisabledMetadataKey)
	delete(auth.Metadata, quotaAutoDisabledAtMetadataKey)
	delete(auth.Metadata, quotaAutoDisabledReasonMetadataKey)

	auth.Disabled = false
	auth.Unavailable = true
	auth.Status = StatusError
	auth.StatusMessage = quotaAutoCooldownStatusMessage(reason, resetAt)
	auth.NextRetryAfter = resetAt
	auth.Quota = QuotaState{
		Exceeded:      true,
		Reason:        reason,
		NextRecoverAt: resetAt,
	}
}

func clearQuotaAutoCooldown(auth *Auth) {
	if auth == nil || !quotaAutoCooldownMarked(auth.Metadata) {
		return
	}
	delete(auth.Metadata, quotaAutoCooldownMetadataKey)
	delete(auth.Metadata, quotaAutoCooldownUntilMetadataKey)
	delete(auth.Metadata, quotaAutoCooldownReasonMetadataKey)
	if auth.Unavailable && auth.Quota.Exceeded {
		auth.Unavailable = false
		auth.NextRetryAfter = time.Time{}
		auth.Quota = QuotaState{}
		auth.Status = StatusActive
		auth.StatusMessage = ""
	}
}

func quotaAutoDisabledStatusMessage(reason string) string {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return quotaAutoDisabledStatusPrefix
	}
	return quotaAutoDisabledStatusPrefix + ": " + reason
}

func quotaAutoCooldownStatusMessage(reason string, resetAt time.Time) string {
	parts := []string{quotaAutoCooldownStatusPrefix}
	if reason != "" {
		parts = append(parts, reason)
	}
	if !resetAt.IsZero() {
		parts = append(parts, "reset at "+resetAt.UTC().Format(time.RFC3339))
	}
	return strings.Join(parts, ": ")
}

func quotaAutomationMarked(metadata map[string]any) bool {
	if len(metadata) == 0 {
		return false
	}
	switch v := metadata[quotaAutoDisabledMetadataKey].(type) {
	case bool:
		return v
	case string:
		return strings.EqualFold(strings.TrimSpace(v), "true")
	default:
		return false
	}
}

func quotaAutoCooldownMarked(metadata map[string]any) bool {
	if len(metadata) == 0 {
		return false
	}
	switch v := metadata[quotaAutoCooldownMetadataKey].(type) {
	case bool:
		return v
	case string:
		return strings.EqualFold(strings.TrimSpace(v), "true")
	default:
		return false
	}
}

func quotaAutoDisabledReason(metadata map[string]any) string {
	if len(metadata) == 0 {
		return ""
	}
	if raw, ok := metadata[quotaAutoDisabledReasonMetadataKey].(string); ok {
		return strings.TrimSpace(raw)
	}
	return ""
}

func quotaAutoDisabledAt(metadata map[string]any) string {
	if len(metadata) == 0 {
		return ""
	}
	if raw, ok := metadata[quotaAutoDisabledAtMetadataKey].(string); ok {
		return strings.TrimSpace(raw)
	}
	return ""
}

func readQuotaAutomationSnapshot(metadata map[string]any) quotaAutomationSnapshot {
	return readQuotaAutomationSnapshotAt(metadata, time.Now().UTC())
}

func readQuotaAutomationSnapshotAt(metadata map[string]any, now time.Time) quotaAutomationSnapshot {
	return quotaAutomationSnapshot{
		fiveHoursRemainingPercent: findQuotaWindowRemainingPercent(metadata, fiveHourQuotaWindowAliases),
		sevenDaysRemainingPercent: findQuotaWindowRemainingPercent(metadata, sevenDayQuotaWindowAliases),
		fiveHoursResetAt:          findQuotaWindowResetAt(metadata, fiveHourQuotaWindowAliases, now),
		sevenDaysResetAt:          findQuotaWindowResetAt(metadata, sevenDayQuotaWindowAliases, now),
	}
}

func findQuotaWindowRemainingPercent(node any, aliases map[string]struct{}) *float64 {
	switch typed := node.(type) {
	case map[string]any:
		if value, ok := quotaWindowPercentFromTaggedMap(typed, aliases); ok {
			return float64Ptr(value)
		}
		for key, rawValue := range typed {
			normalizedKey := normalizeQuotaKey(key)
			if _, ok := aliases[normalizedKey]; ok {
				if value, found := findRemainingPercentForWindowValue(rawValue); found {
					return float64Ptr(value)
				}
			}
			if value, found := combinedQuotaWindowPercent(normalizedKey, rawValue, aliases); found {
				return float64Ptr(value)
			}
			if nested := findQuotaWindowRemainingPercent(rawValue, aliases); nested != nil {
				return nested
			}
		}
	case []any:
		for _, item := range typed {
			if nested := findQuotaWindowRemainingPercent(item, aliases); nested != nil {
				return nested
			}
		}
	}
	return nil
}

func findQuotaWindowResetAt(node any, aliases map[string]struct{}, now time.Time) *time.Time {
	switch typed := node.(type) {
	case map[string]any:
		if value, ok := quotaWindowResetFromTaggedMap(typed, aliases, now); ok {
			return timePtr(value)
		}
		for key, rawValue := range typed {
			normalizedKey := normalizeQuotaKey(key)
			if _, ok := aliases[normalizedKey]; ok {
				if value, found := findResetAtForWindowValue(rawValue, now); found {
					return timePtr(value)
				}
			}
			if value, found := combinedQuotaWindowResetAt(normalizedKey, rawValue, aliases, now); found {
				return timePtr(value)
			}
			if nested := findQuotaWindowResetAt(rawValue, aliases, now); nested != nil {
				return nested
			}
		}
	case []any:
		for _, item := range typed {
			if nested := findQuotaWindowResetAt(item, aliases, now); nested != nil {
				return nested
			}
		}
	}
	return nil
}

func quotaWindowPercentFromTaggedMap(node map[string]any, aliases map[string]struct{}) (float64, bool) {
	for key, rawValue := range node {
		if _, ok := quotaWindowTagKeys[normalizeQuotaKey(key)]; !ok {
			continue
		}
		if !quotaWindowAliasMatches(rawValue, aliases) {
			continue
		}
		return findRemainingPercentInMap(node, true)
	}
	return 0, false
}

func quotaWindowResetFromTaggedMap(node map[string]any, aliases map[string]struct{}, now time.Time) (time.Time, bool) {
	for key, rawValue := range node {
		if _, ok := quotaWindowTagKeys[normalizeQuotaKey(key)]; !ok {
			continue
		}
		if !quotaWindowAliasMatches(rawValue, aliases) {
			continue
		}
		return findResetAtInMap(node, now)
	}
	return time.Time{}, false
}

func quotaWindowAliasMatches(value any, aliases map[string]struct{}) bool {
	str, ok := value.(string)
	if !ok {
		return false
	}
	_, matched := aliases[normalizeQuotaKey(str)]
	return matched
}

func combinedQuotaWindowPercent(normalizedKey string, value any, aliases map[string]struct{}) (float64, bool) {
	if normalizedKey == "" || len(aliases) == 0 {
		return 0, false
	}
	hasAlias := false
	for alias := range aliases {
		if strings.Contains(normalizedKey, alias) {
			hasAlias = true
			break
		}
	}
	if !hasAlias {
		return 0, false
	}
	switch {
	case strings.Contains(normalizedKey, "remainingratio"), strings.Contains(normalizedKey, "remainingfraction"):
		return parseQuotaRatioValue(value)
	case strings.Contains(normalizedKey, "remainingpercent"),
		strings.Contains(normalizedKey, "remainingpct"),
		strings.Contains(normalizedKey, "remainingpercentage"),
		strings.Contains(normalizedKey, "percentremaining"),
		strings.Contains(normalizedKey, "quotaremainingpercent"),
		strings.Contains(normalizedKey, "quotaremainingpct"),
		strings.Contains(normalizedKey, "quotaremaining"),
		strings.Contains(normalizedKey, "remaining"):
		return parseQuotaPercentValue(value)
	default:
		return 0, false
	}
}

func combinedQuotaWindowResetAt(normalizedKey string, value any, aliases map[string]struct{}, now time.Time) (time.Time, bool) {
	if normalizedKey == "" || len(aliases) == 0 || !strings.Contains(normalizedKey, "reset") {
		return time.Time{}, false
	}
	hasAlias := false
	for alias := range aliases {
		if strings.Contains(normalizedKey, alias) {
			hasAlias = true
			break
		}
	}
	if !hasAlias {
		return time.Time{}, false
	}
	if strings.Contains(normalizedKey, "seconds") || strings.Contains(normalizedKey, "secs") {
		return parseResetSecondsValue(value, now)
	}
	return parseResetTimeValue(value, now)
}

func findRemainingPercentForWindowValue(value any) (float64, bool) {
	switch typed := value.(type) {
	case map[string]any:
		return findRemainingPercentInMap(typed, false)
	case []any:
		for _, item := range typed {
			if pct, ok := findRemainingPercentForWindowValue(item); ok {
				return pct, true
			}
		}
		return 0, false
	default:
		return parseQuotaPercentValue(value)
	}
}

func findResetAtForWindowValue(value any, now time.Time) (time.Time, bool) {
	switch typed := value.(type) {
	case map[string]any:
		return findResetAtInMap(typed, now)
	case []any:
		for _, item := range typed {
			if resetAt, ok := findResetAtForWindowValue(item, now); ok {
				return resetAt, true
			}
		}
		return time.Time{}, false
	default:
		return parseResetTimeValue(value, now)
	}
}

func findRemainingPercentInMap(node map[string]any, allowBarePercent bool) (float64, bool) {
	for key, rawValue := range node {
		normalizedKey := normalizeQuotaKey(key)
		switch {
		case containsNormalizedQuotaKey(quotaRemainingPercentKeys, normalizedKey):
			if value, ok := parseQuotaPercentValue(rawValue); ok {
				return value, true
			}
		case containsNormalizedQuotaKey(quotaRemainingRatioKeys, normalizedKey):
			if value, ok := parseQuotaRatioValue(rawValue); ok {
				return value, true
			}
		case containsNormalizedQuotaKey(quotaRemainingContainerKeys, normalizedKey):
			if value, ok := extractPercentFromRemainingNode(rawValue); ok {
				return value, true
			}
		case allowBarePercent && containsNormalizedQuotaKey(quotaBarePercentKeys, normalizedKey):
			if value, ok := parseQuotaPercentValue(rawValue); ok {
				return value, true
			}
		case allowBarePercent && containsNormalizedQuotaKey(quotaBareRatioKeys, normalizedKey):
			if value, ok := parseQuotaRatioValue(rawValue); ok {
				return value, true
			}
		}
	}
	return 0, false
}

func findResetAtInMap(node map[string]any, now time.Time) (time.Time, bool) {
	for key, rawValue := range node {
		normalizedKey := normalizeQuotaKey(key)
		switch {
		case containsNormalizedQuotaKey(quotaResetTimeKeys, normalizedKey):
			if value, ok := parseResetTimeValue(rawValue, now); ok {
				return value, true
			}
		case containsNormalizedQuotaKey(quotaResetSecondsKeys, normalizedKey):
			if value, ok := parseResetSecondsValue(rawValue, now); ok {
				return value, true
			}
		}
	}
	return time.Time{}, false
}

func extractPercentFromRemainingNode(node any) (float64, bool) {
	switch typed := node.(type) {
	case map[string]any:
		return findRemainingPercentInMap(typed, true)
	case []any:
		for _, item := range typed {
			if value, ok := extractPercentFromRemainingNode(item); ok {
				return value, true
			}
		}
		return 0, false
	default:
		return parseQuotaPercentValue(node)
	}
}

func parseQuotaPercentValue(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int8:
		return float64(typed), true
	case int16:
		return float64(typed), true
	case int32:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case uint:
		return float64(typed), true
	case uint8:
		return float64(typed), true
	case uint16:
		return float64(typed), true
	case uint32:
		return float64(typed), true
	case uint64:
		return float64(typed), true
	case json.Number:
		parsed, err := typed.Float64()
		return parsed, err == nil
	case string:
		trimmed := strings.TrimSpace(strings.TrimSuffix(typed, "%"))
		if trimmed == "" {
			return 0, false
		}
		parsed, err := strconv.ParseFloat(trimmed, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func parseQuotaRatioValue(value any) (float64, bool) {
	parsed, ok := parseQuotaPercentValue(value)
	if !ok {
		return 0, false
	}
	return parsed * 100, true
}

func parseResetTimeValue(value any, now time.Time) (time.Time, bool) {
	if parsed, ok := parseTimeValue(value); ok && !parsed.IsZero() {
		return parsed.UTC(), true
	}
	if parsed, ok := parseQuotaPercentValue(value); ok && parsed > 0 && parsed < 10_000_000 {
		return now.Add(time.Duration(parsed) * time.Second).UTC(), true
	}
	return time.Time{}, false
}

func parseResetSecondsValue(value any, now time.Time) (time.Time, bool) {
	seconds, ok := parseQuotaPercentValue(value)
	if !ok || seconds <= 0 {
		return time.Time{}, false
	}
	return now.Add(time.Duration(seconds) * time.Second).UTC(), true
}

func quotaAutoCooldownUntil(metadata map[string]any) (time.Time, bool) {
	if len(metadata) == 0 {
		return time.Time{}, false
	}
	return parseTimeValue(metadata[quotaAutoCooldownUntilMetadataKey])
}

func quotaAutoCooldownReason(metadata map[string]any) string {
	if len(metadata) == 0 {
		return ""
	}
	if raw, ok := metadata[quotaAutoCooldownReasonMetadataKey].(string); ok {
		return strings.TrimSpace(raw)
	}
	return ""
}

func normalizedQuotaKeySet(keys ...string) map[string]struct{} {
	set := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		normalized := normalizeQuotaKey(key)
		if normalized == "" {
			continue
		}
		set[normalized] = struct{}{}
	}
	return set
}

func containsNormalizedQuotaKey(set map[string]struct{}, key string) bool {
	_, ok := set[key]
	return ok
}

func normalizeQuotaKey(key string) string {
	var builder strings.Builder
	builder.Grow(len(key))
	for _, r := range strings.ToLower(strings.TrimSpace(key)) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

func formatQuotaPercent(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}

func float64Ptr(value float64) *float64 {
	return &value
}

func timePtr(value time.Time) *time.Time {
	return &value
}
