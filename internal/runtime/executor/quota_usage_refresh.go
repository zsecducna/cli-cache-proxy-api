package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

const (
	codexQuotaUsageURL  = "https://chatgpt.com/backend-api/wham/usage"
	claudeQuotaUsageURL = "https://api.anthropic.com/api/oauth/usage"
)

func (e *CodexExecutor) RefreshQuotaUsage(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if auth == nil {
		return nil, statusErr{code: http.StatusUnauthorized, msg: "missing auth"}
	}
	apiKey, _ := codexCreds(auth)
	if strings.TrimSpace(apiKey) == "" {
		return auth, nil
	}

	httpReq, errReq := http.NewRequestWithContext(ctx, http.MethodPost, codexQuotaUsageURL, bytes.NewReader([]byte("{}")))
	if errReq != nil {
		return auth, errReq
	}
	applyCodexHeaders(httpReq, auth, apiKey, false, e.cfg)

	bodyBytes, statusCode, errDo := doQuotaUsageRequest(ctx, e.cfg, auth, httpReq)
	if errDo != nil {
		return auth, errDo
	}
	return withQuotaUsageMetadata(auth, "codex", statusCode, bodyBytes), nil
}

func (e *CodexAutoExecutor) RefreshQuotaUsage(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if e == nil || e.httpExec == nil {
		return nil, fmt.Errorf("codex auto executor: http executor is nil")
	}
	return e.httpExec.RefreshQuotaUsage(ctx, auth)
}

func (e *ClaudeExecutor) RefreshQuotaUsage(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if auth == nil {
		return nil, statusErr{code: http.StatusUnauthorized, msg: "missing auth"}
	}
	apiKey, _ := claudeCreds(auth)
	if strings.TrimSpace(apiKey) == "" {
		return auth, nil
	}

	httpReq, errReq := http.NewRequestWithContext(ctx, http.MethodPost, claudeQuotaUsageURL, bytes.NewReader([]byte("{}")))
	if errReq != nil {
		return auth, errReq
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if errPrepare := e.PrepareRequest(httpReq, auth); errPrepare != nil {
		return auth, errPrepare
	}

	bodyBytes, statusCode, errDo := doQuotaUsageRequest(ctx, e.cfg, auth, httpReq)
	if errDo != nil {
		return auth, errDo
	}
	return withQuotaUsageMetadata(auth, "claude", statusCode, bodyBytes), nil
}

func (e *AntigravityExecutor) RefreshQuotaUsage(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if auth == nil {
		return nil, statusErr{code: http.StatusUnauthorized, msg: "missing auth"}
	}
	token := strings.TrimSpace(metaStringValue(auth.Metadata, "access_token"))
	if token == "" {
		return auth, nil
	}

	userAgent := resolveLoadCodeAssistUserAgent(auth)
	loadReqBody, errMarshal := json.Marshal(map[string]any{
		"metadata": map[string]string{
			"ide_type":    "ANTIGRAVITY",
			"ide_version": misc.AntigravityVersionFromUserAgent(userAgent),
			"ide_name":    "antigravity",
		},
	})
	if errMarshal != nil {
		return auth, errMarshal
	}

	endpointURL := strings.TrimSuffix(buildBaseURL(auth), "/") + "/v1internal:loadCodeAssist"
	httpReq, errReq := http.NewRequestWithContext(ctx, http.MethodPost, endpointURL, bytes.NewReader(loadReqBody))
	if errReq != nil {
		return auth, errReq
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("User-Agent", userAgent)
	httpReq.Header.Set("X-Goog-Api-Client", misc.AntigravityGoogAPIClientUA)

	httpClient := newAntigravityHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, errDo := httpClient.Do(httpReq)
	if errDo != nil {
		return auth, errDo
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			logQuotaUsageCloseError("antigravity", errClose)
		}
	}()
	bodyBytes, errRead := io.ReadAll(httpResp.Body)
	if errRead != nil {
		return auth, errRead
	}
	if httpResp.StatusCode < http.StatusOK || httpResp.StatusCode >= http.StatusMultipleChoices {
		return auth, statusErr{code: httpResp.StatusCode, msg: string(bodyBytes)}
	}

	updated := withQuotaUsageMetadata(auth, "antigravity", httpResp.StatusCode, bodyBytes)
	applyAntigravityCreditsMetadata(updated, bodyBytes)
	return updated, nil
}

func doQuotaUsageRequest(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, req *http.Request) ([]byte, int, error) {
	httpClient := helps.NewProxyAwareHTTPClient(ctx, cfg, auth, 0)
	httpResp, errDo := httpClient.Do(req)
	if errDo != nil {
		return nil, 0, errDo
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			logQuotaUsageCloseError(providerFromAuth(auth), errClose)
		}
	}()
	bodyBytes, errRead := io.ReadAll(httpResp.Body)
	if errRead != nil {
		return nil, httpResp.StatusCode, errRead
	}
	if httpResp.StatusCode < http.StatusOK || httpResp.StatusCode >= http.StatusMultipleChoices {
		return bodyBytes, httpResp.StatusCode, statusErr{code: httpResp.StatusCode, msg: string(bodyBytes)}
	}
	return bodyBytes, httpResp.StatusCode, nil
}

func withQuotaUsageMetadata(auth *cliproxyauth.Auth, provider string, statusCode int, bodyBytes []byte) *cliproxyauth.Auth {
	updated := auth.Clone()
	if updated.Metadata == nil {
		updated.Metadata = make(map[string]any)
	}
	updated.Metadata["quota_usage_provider"] = provider
	updated.Metadata["quota_usage_status_code"] = statusCode
	updated.Metadata["quota_usage_checked_at"] = time.Now().Format(time.RFC3339)
	if parsed, ok := parseQuotaUsageBody(bodyBytes); ok {
		updated.Metadata["quota_usage"] = parsed
		delete(updated.Metadata, "quota_usage_raw")
	} else {
		updated.Metadata["quota_usage_raw"] = string(bodyBytes)
		delete(updated.Metadata, "quota_usage")
	}
	return updated
}

func parseQuotaUsageBody(bodyBytes []byte) (any, bool) {
	if len(bytes.TrimSpace(bodyBytes)) == 0 {
		return map[string]any{}, true
	}
	var parsed any
	if err := json.Unmarshal(bodyBytes, &parsed); err != nil {
		return nil, false
	}
	return parsed, true
}

func applyAntigravityCreditsMetadata(auth *cliproxyauth.Auth, bodyBytes []byte) {
	if auth == nil {
		return
	}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	authID := strings.TrimSpace(auth.ID)
	paidTierID := strings.TrimSpace(gjson.GetBytes(bodyBytes, "paidTier.id").String())
	if paidTierID != "" {
		auth.Metadata["antigravity_paid_tier_id"] = paidTierID
	}
	credits := gjson.GetBytes(bodyBytes, "paidTier.availableCredits")
	if !credits.IsArray() {
		if authID != "" {
			cliproxyauth.SetAntigravityCreditsHint(authID, cliproxyauth.AntigravityCreditsHint{
				Known:      true,
				Available:  false,
				PaidTierID: paidTierID,
				UpdatedAt:  time.Now(),
			})
		}
		return
	}
	for _, credit := range credits.Array() {
		if !strings.EqualFold(credit.Get("creditType").String(), "GOOGLE_ONE_AI") {
			continue
		}
		creditAmount := credit.Get("creditAmount").Float()
		minAmount := credit.Get("minimumCreditAmountForUsage").Float()
		auth.Metadata["antigravity_credit_type"] = "GOOGLE_ONE_AI"
		auth.Metadata["antigravity_credit_amount"] = creditAmount
		auth.Metadata["antigravity_min_credit_amount_for_usage"] = minAmount
		auth.Metadata["antigravity_credits_available"] = creditAmount >= minAmount
		if authID != "" {
			antigravityCreditsBalanceByAuth.Store(authID, antigravityCreditsBalance{
				CreditAmount:    creditAmount,
				MinCreditAmount: minAmount,
				PaidTierID:      paidTierID,
				Known:           true,
			})
			cliproxyauth.SetAntigravityCreditsHint(authID, cliproxyauth.AntigravityCreditsHint{
				Known:           true,
				Available:       creditAmount >= minAmount,
				CreditAmount:    creditAmount,
				MinCreditAmount: minAmount,
				PaidTierID:      paidTierID,
				UpdatedAt:       time.Now(),
			})
		}
		if creditAmount >= minAmount {
			clearAntigravityCreditsPermanentlyDisabled(auth)
		}
		return
	}
}

func providerFromAuth(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return "unknown"
	}
	provider := strings.TrimSpace(auth.Provider)
	if provider == "" {
		return "unknown"
	}
	return provider
}

func logQuotaUsageCloseError(provider string, err error) {
	log.Errorf("%s quota/usage refresh: close response body error: %v", provider, err)
}
