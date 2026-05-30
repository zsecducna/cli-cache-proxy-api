// Package kiro provides authentication, token management, and protocol
// constants for Amazon Kiro (AWS CodeWhisperer). It implements the AWS SSO OIDC
// device authorization flow (Builder ID and IAM Identity Center) plus refresh
// token import, and exposes the request-shaping helpers needed by the executor.
package kiro

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

const (
	// OAuthClientName is the client name registered with AWS SSO OIDC.
	OAuthClientName = "kiro-oauth-client"
	// IssuerURL identifies the Kiro IAM Identity Center instance used during client registration.
	IssuerURL = "https://identitycenter.amazonaws.com/ssoins-722374e8c3c8e6c6"
	// BuilderIDStartURL is the AWS Builder ID portal start URL used for the default login method.
	BuilderIDStartURL = "https://view.awsapps.com/start"
	// DefaultRegion is the AWS region used when none is supplied or derivable.
	DefaultRegion = "us-east-1"

	// DeviceCodeGrantType is the RFC 8628 device authorization grant type.
	DeviceCodeGrantType = "urn:ietf:params:oauth:grant-type:device_code"
	// RefreshGrantType is the OAuth2 refresh token grant type.
	RefreshGrantType = "refresh_token"

	// KiroIDEVersion is the Kiro IDE version embedded in the User-Agent so that
	// CodeWhisperer accepts the request (it rejects non-Kiro looking agents).
	KiroIDEVersion = "0.10.32"

	// GenerateTarget is the X-Amz-Target header value for the streaming generate call.
	GenerateTarget = "AmazonCodeWhispererStreamingService.GenerateAssistantResponse"

	// ListProfilesTarget is the X-Amz-Target for the non-streaming ListAvailableProfiles
	// call (matches what the Kiro/Amazon Q Developer CLI uses to resolve the profileArn).
	ListProfilesTarget = "AmazonCodeWhispererService.ListAvailableProfiles"

	// ImportTokenPrefix is the prefix every valid Kiro refresh token starts with.
	ImportTokenPrefix = "aorAAAAAG"

	// DefaultThinkingBudget is the fallback thinking budget (in tokens) when thinking
	// is enabled without an explicit budget.
	DefaultThinkingBudget = 16000
	// MinThinkingBudget and MaxThinkingBudget bound the injected thinking budget.
	MinThinkingBudget = 1
	MaxThinkingBudget = 32000
)

// OAuthScopes are the CodeWhisperer scopes requested during client registration.
var OAuthScopes = []string{
	"codewhisperer:completions",
	"codewhisperer:analysis",
	"codewhisperer:conversations",
}

// normalizeRegion returns a usable AWS region, defaulting when empty.
func normalizeRegion(region string) string {
	region = strings.TrimSpace(region)
	if region == "" {
		return DefaultRegion
	}
	return region
}

// OIDCRegisterURL returns the AWS SSO OIDC client registration endpoint for a region.
func OIDCRegisterURL(region string) string {
	return fmt.Sprintf("https://oidc.%s.amazonaws.com/client/register", normalizeRegion(region))
}

// OIDCDeviceAuthURL returns the AWS SSO OIDC device authorization endpoint for a region.
func OIDCDeviceAuthURL(region string) string {
	return fmt.Sprintf("https://oidc.%s.amazonaws.com/device_authorization", normalizeRegion(region))
}

// OIDCTokenURL returns the AWS SSO OIDC token endpoint (poll + refresh) for a region.
func OIDCTokenURL(region string) string {
	return fmt.Sprintf("https://oidc.%s.amazonaws.com/token", normalizeRegion(region))
}

// GenerateEndpoint returns the Kiro runtime streaming generate endpoint for a region.
// Current Kiro models are served from runtime.{region}.kiro.dev (the legacy Amazon Q
// codewhisperer host is decommissioned for recent models); this endpoint requires a
// profileArn in the request body.
func GenerateEndpoint(region string) string {
	return fmt.Sprintf("https://runtime.%s.kiro.dev/generateAssistantResponse", normalizeRegion(region))
}

// ListProfilesEndpoint returns the CodeWhisperer service root that serves the
// non-streaming ListAvailableProfiles operation (used to resolve the profileArn).
func ListProfilesEndpoint(region string) string {
	return fmt.Sprintf("https://codewhisperer.%s.amazonaws.com/", normalizeRegion(region))
}

// RegionFromProfileArn extracts the region from an ARN shaped like
// "arn:aws:codewhisperer:{region}:...". It returns "" when no region is present.
func RegionFromProfileArn(profileArn string) string {
	parts := strings.Split(strings.TrimSpace(profileArn), ":")
	// arn:aws:codewhisperer:REGION:account:... => index 3 holds the region.
	if len(parts) >= 4 {
		return strings.TrimSpace(parts[3])
	}
	return ""
}

// BuildMachineID derives a stable machine identifier as the hex SHA-256 of the
// concatenated, pipe-joined seed parts (clientID | refreshToken | profileArn | accessToken).
// A stable machine ID keeps the User-Agent consistent across requests for one credential.
func BuildMachineID(parts ...string) string {
	seed := strings.Join(parts, "|")
	sum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(sum[:])
}

// BuildUserAgent returns the long-form User-Agent that mimics the Kiro IDE / AWS SDK.
// CodeWhisperer rejects requests whose User-Agent does not look like the Kiro IDE.
func BuildUserAgent(machineID string) string {
	return fmt.Sprintf("aws-sdk-js/1.0.0 ua/2.1 os/windows#10.0.26200 lang/js md/nodejs#22.21.1 api/codewhispererruntime#1.0.0 m/N,E KiroIDE-%s-%s", KiroIDEVersion, machineID)
}

// BuildXAmzUserAgent returns the short-form x-amz-user-agent header value.
func BuildXAmzUserAgent(machineID string) string {
	return fmt.Sprintf("aws-sdk-js/1.0.0 KiroIDE-%s-%s", KiroIDEVersion, machineID)
}

// ResolveKiroModel maps a requested model id to the upstream CodeWhisperer model id
// and decodes the synthetic "-agentic" / "-thinking" variant suffixes.
//   - "claude-sonnet-4.5-agentic" -> ("claude-sonnet-4.5", agentic=true, thinking=false)
//   - "claude-sonnet-4.5-thinking" -> ("claude-sonnet-4.5", agentic=false, thinking=true)
//
// The synthetic suffixes are stripped so only the real upstream id is sent as modelId.
func ResolveKiroModel(id string) (upstream string, agentic bool, thinking bool) {
	upstream = strings.TrimSpace(id)
	// Strip both synthetic suffixes regardless of their order or combination.
	for {
		switch {
		case strings.HasSuffix(upstream, "-agentic"):
			upstream = strings.TrimSuffix(upstream, "-agentic")
			agentic = true
		case strings.HasSuffix(upstream, "-thinking"):
			upstream = strings.TrimSuffix(upstream, "-thinking")
			thinking = true
		default:
			return upstream, agentic, thinking
		}
	}
}

// ClampThinkingBudget bounds the requested budget to the supported range, applying
// the default when the input is non-positive.
func ClampThinkingBudget(budget int) int {
	if budget <= 0 {
		budget = DefaultThinkingBudget
	}
	if budget < MinThinkingBudget {
		return MinThinkingBudget
	}
	if budget > MaxThinkingBudget {
		return MaxThinkingBudget
	}
	return budget
}

// BuildThinkingPrefix returns the content prefix that enables extended thinking for a
// given token budget. It is prepended to the user message content when thinking is on.
func BuildThinkingPrefix(budget int) string {
	return fmt.Sprintf("<thinking_mode>enabled</thinking_mode>\n<max_thinking_length>%d</max_thinking_length>", ClampThinkingBudget(budget))
}

// BuildContextPrefix returns the per-request context prefix carrying the current time.
func BuildContextPrefix(now time.Time) string {
	return fmt.Sprintf("[Context: Current time is %s]", now.Format(time.RFC3339))
}

// AgenticSystemPrompt is injected verbatim as a content prefix for "-agentic" model
// variants. Its key invariant is the hard 350-line cap on file writes to avoid
// upstream timeouts; it is omitted for the non-agentic ("auto") model.
const AgenticSystemPrompt = `# CRITICAL: CHUNKED WRITE PROTOCOL (MANDATORY)

You MUST follow these rules for ALL file operations. Violation causes server timeouts and task failure.

## ABSOLUTE LIMITS
- MAXIMUM 350 LINES per single write/edit operation - NO EXCEPTIONS
- RECOMMENDED 300 LINES or less per operation for safety
- If content exceeds 350 lines, you MUST split it across multiple operations

## REQUIRED STRATEGY FOR LARGE FILES
1. For a NEW large file: create it with the first chunk (<= 300 lines), then APPEND each subsequent chunk (<= 300 lines) in separate operations.
2. For an EXISTING file: prefer small, surgical edits that target only the lines that must change. NEVER rewrite an entire large file in one operation.
3. NEVER buffer an entire large file in a single write call.

## MANDATORY RULES
- Count lines before every write. If > 350, split.
- Prefer append/insert over full-file rewrites.
- Make each chunk self-consistent so partial application never corrupts the file.
- After the final chunk, verify the file is complete and well-formed.

Violating the 350-line cap WILL cause the request to fail. When in doubt, write less per operation and use more operations.`
