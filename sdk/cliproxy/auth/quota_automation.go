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

	quotaAutoDisabledStatusPrefix = "disabled automatically due to low remaining quota"
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
}

func applyQuotaAutomation(auth *Auth, existing *Auth) {
	if auth == nil {
		return
	}

	autoDisabled := quotaAutomationMarked(auth.Metadata)
	if !autoDisabled && existing != nil {
		autoDisabled = quotaAutomationMarked(existing.Metadata)
	}

	snapshot := readQuotaAutomationSnapshot(auth.Metadata)
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
	if snapshot.fiveHoursRemainingPercent != nil && *snapshot.fiveHoursRemainingPercent <= quotaAutoDisable5HoursThreshold {
		reasons = append(reasons, fmt.Sprintf("5hrs remaining quota %s%% <= %s%%", formatQuotaPercent(*snapshot.fiveHoursRemainingPercent), formatQuotaPercent(quotaAutoDisable5HoursThreshold)))
	}
	if snapshot.sevenDaysRemainingPercent != nil && *snapshot.sevenDaysRemainingPercent <= quotaAutoDisable7DaysThreshold {
		reasons = append(reasons, fmt.Sprintf("7days remaining quota %s%% <= %s%%", formatQuotaPercent(*snapshot.sevenDaysRemainingPercent), formatQuotaPercent(quotaAutoDisable7DaysThreshold)))
	}
	return strings.Join(reasons, "; ")
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

func quotaAutoDisabledStatusMessage(reason string) string {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return quotaAutoDisabledStatusPrefix
	}
	return quotaAutoDisabledStatusPrefix + ": " + reason
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
	return quotaAutomationSnapshot{
		fiveHoursRemainingPercent: findQuotaWindowRemainingPercent(metadata, fiveHourQuotaWindowAliases),
		sevenDaysRemainingPercent: findQuotaWindowRemainingPercent(metadata, sevenDayQuotaWindowAliases),
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
