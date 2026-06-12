package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/kiro"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
	log "github.com/sirupsen/logrus"
)

// DoKiroLogin triggers a Kiro (AWS CodeWhisperer) login and saves the resulting tokens.
// The method selects the flow: "builder-id" (default), "idc" (IAM Identity Center), or
// "import" (paste/auto-detect a refresh token). Method-specific inputs are passed to the
// authenticator through the login metadata map.
//
// Parameters:
//   - cfg: The application configuration containing proxy and auth directory settings
//   - options: Login options including browser behavior settings
//   - idcStartURL: IAM Identity Center start URL (idc method only)
//   - region: AWS region for the OIDC endpoints (defaults to us-east-1 when empty)
//   - importToken: a refresh token to import (import method only; empty triggers auto-detect)
//   - username: account label used to name the IDC auth file (required for the idc method;
//     the IDC access token is opaque and carries no derivable identity)
//   - sso: when true, use the Kiro hosted SSO portal (social method) — a PKCE flow that
//     federates Google, GitHub, and enterprise IdPs (e.g. an Azure AD tenant). It needs no
//     start URL or username (the account email is parsed from the issued token).
func DoKiroLogin(cfg *config.Config, options *LoginOptions, idcStartURL, region, importToken, username string, sso bool) {
	if options == nil {
		options = &LoginOptions{}
	}

	// Derive the login method. An import token implies "import", the SSO flag implies
	// "social", and an explicit IDC start URL implies "idc"; each bypasses the interactive
	// menu so scripted use keeps working. When no method-selecting flag is supplied, prompt
	// the user to choose (Builder ID vs IAM Identity Center) and, for IDC, collect the
	// start URL and region.
	idcStartURL = strings.TrimSpace(idcStartURL)
	region = strings.TrimSpace(region)
	importToken = strings.TrimSpace(importToken)
	username = strings.TrimSpace(username)

	method := "builder-id"
	switch {
	case importToken != "":
		method = "import"
	case sso:
		method = "social"
	case idcStartURL != "":
		method = "idc"
		// IDC names the auth file kiro-<username>-<directoryID>.json. The username cannot be
		// inferred from the opaque IDC token, so in non-interactive (flag-driven) use it must
		// be supplied; prompt for it interactively when a TTY prompt is available.
		if username == "" {
			username = promptKiroUsername(options)
			if username == "" {
				log.Error("Kiro authentication failed: --kiro-username is required for IDC login")
				return
			}
		}
		// Reject a username with no filename-safe characters early (the SDK enforces this
		// too, but a CLI-side check gives a clearer message before the login round-trip).
		if kiro.SanitizeFileComponent(username) == "" {
			log.Error("Kiro authentication failed: --kiro-username must contain filename-safe characters")
			return
		}
	default:
		// No method-selecting flag provided: ask interactively.
		promptFn := options.Prompt
		if promptFn == nil {
			promptFn = defaultProjectPrompt()
		}
		var err error
		method, idcStartURL, region, username, err = promptKiroMethod(promptFn, region)
		if err != nil {
			log.Errorf("Kiro authentication failed: %v", err)
			return
		}
	}

	manager := newAuthManager()
	authOpts := &sdkAuth.LoginOptions{
		NoBrowser: options.NoBrowser,
		Metadata: map[string]string{
			"method":        method,
			"idc_start_url": idcStartURL,
			"region":        region,
			"import_token":  importToken,
			"username":      username,
		},
		Prompt: options.Prompt,
	}

	record, savedPath, err := manager.Login(context.Background(), "kiro", cfg, authOpts)
	if err != nil {
		log.Errorf("Kiro authentication failed: %v", err)
		return
	}

	if savedPath != "" {
		fmt.Printf("Authentication saved to %s\n", savedPath)
	}
	if record != nil && record.Label != "" {
		fmt.Printf("Authenticated as %s\n", record.Label)
	}
	fmt.Println("Kiro authentication successful!")
}

// promptKiroMethod interactively asks the user to choose a Kiro authentication method and,
// for IAM Identity Center, collects the start URL, AWS region, and account username. It is
// only invoked when no method-selecting flag was supplied. The returned method is one of
// "builder-id" or "idc". defaultRegion pre-fills the region prompt (falling back to
// kiro.DefaultRegion when blank). The returned username is non-empty only for the IDC method.
// It returns an error only when required IDC input is missing.
func promptKiroMethod(promptFn func(string) (string, error), defaultRegion string) (method, idcStartURL, region, username string, err error) {
	// Present the two supported interactive methods; Builder ID is the default on empty input.
	fmt.Println("Select Kiro authentication method:")
	fmt.Println("  1) AWS Builder ID (default)")
	fmt.Println("  2) AWS IAM Identity Center")
	choice, err := promptFn("Enter choice [1-2] (default 1): ")
	if err != nil {
		return "", "", "", "", fmt.Errorf("failed to read authentication method: %w", err)
	}

	switch strings.TrimSpace(choice) {
	case "2", "idc", "IDC":
		// IAM Identity Center requires a start URL; the region defaults to us-east-1.
		idcStartURL, err = promptFn("IDC Start URL (e.g. https://d-90660ceab3.awsapps.com/start): ")
		if err != nil {
			return "", "", "", "", fmt.Errorf("failed to read IDC start URL: %w", err)
		}
		idcStartURL = strings.TrimSpace(idcStartURL)
		if idcStartURL == "" {
			return "", "", "", "", fmt.Errorf("kiro: IDC login requires a start URL")
		}

		regionDefault := strings.TrimSpace(defaultRegion)
		if regionDefault == "" {
			regionDefault = kiro.DefaultRegion
		}
		region, err = promptFn(fmt.Sprintf("AWS Region (default %s): ", regionDefault))
		if err != nil {
			return "", "", "", "", fmt.Errorf("failed to read AWS region: %w", err)
		}
		region = strings.TrimSpace(region)
		if region == "" {
			region = regionDefault
		}

		// The IDC token is opaque, so the username that names the auth file must be entered.
		username, err = promptFn("Account username (used to name the auth file): ")
		if err != nil {
			return "", "", "", "", fmt.Errorf("failed to read username: %w", err)
		}
		username = strings.TrimSpace(username)
		if username == "" {
			return "", "", "", "", fmt.Errorf("kiro: IDC login requires a username")
		}
		return "idc", idcStartURL, region, username, nil
	default:
		// Any other input (including empty) selects the Builder ID device flow.
		return "builder-id", "", strings.TrimSpace(defaultRegion), "", nil
	}
}

// promptKiroUsername reads the IDC account username from the configured prompt (or the
// default project prompt). It returns "" when no prompt is available or the user enters
// nothing, letting the caller surface the "username required" error.
func promptKiroUsername(options *LoginOptions) string {
	promptFn := options.Prompt
	if promptFn == nil {
		promptFn = defaultProjectPrompt()
	}
	if promptFn == nil {
		return ""
	}
	value, err := promptFn("Account username (used to name the auth file): ")
	if err != nil {
		// Preserve the underlying cause (e.g. closed stdin) at debug level; the caller
		// surfaces the user-facing "username is required" error.
		log.Debugf("kiro: username prompt failed: %v", err)
		return ""
	}
	return strings.TrimSpace(value)
}
