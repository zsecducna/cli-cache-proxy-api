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
func DoKiroLogin(cfg *config.Config, options *LoginOptions, idcStartURL, region, importToken string) {
	if options == nil {
		options = &LoginOptions{}
	}

	// Derive the login method. An import token implies "import" and an explicit IDC start
	// URL implies "idc"; both bypass the interactive menu so scripted use keeps working.
	// When neither flag is supplied, prompt the user to choose the authentication method
	// (Builder ID vs IAM Identity Center) and, for IDC, collect the start URL and region.
	idcStartURL = strings.TrimSpace(idcStartURL)
	region = strings.TrimSpace(region)
	importToken = strings.TrimSpace(importToken)

	method := "builder-id"
	switch {
	case importToken != "":
		method = "import"
	case idcStartURL != "":
		method = "idc"
	default:
		// No method-selecting flag provided: ask interactively.
		promptFn := options.Prompt
		if promptFn == nil {
			promptFn = defaultProjectPrompt()
		}
		var err error
		method, idcStartURL, region, err = promptKiroMethod(promptFn, region)
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
// for IAM Identity Center, collects the start URL and AWS region. It is only invoked when
// no method-selecting flag was supplied. The returned method is one of "builder-id" or
// "idc". defaultRegion pre-fills the region prompt (falling back to kiro.DefaultRegion when
// blank). It returns an error only when required IDC input is missing.
func promptKiroMethod(promptFn func(string) (string, error), defaultRegion string) (method, idcStartURL, region string, err error) {
	// Present the two supported interactive methods; Builder ID is the default on empty input.
	fmt.Println("Select Kiro authentication method:")
	fmt.Println("  1) AWS Builder ID (default)")
	fmt.Println("  2) AWS IAM Identity Center")
	choice, err := promptFn("Enter choice [1-2] (default 1): ")
	if err != nil {
		return "", "", "", fmt.Errorf("failed to read authentication method: %w", err)
	}

	switch strings.TrimSpace(choice) {
	case "2", "idc", "IDC":
		// IAM Identity Center requires a start URL; the region defaults to us-east-1.
		idcStartURL, err = promptFn("IDC Start URL (e.g. https://d-90660ceab3.awsapps.com/start): ")
		if err != nil {
			return "", "", "", fmt.Errorf("failed to read IDC start URL: %w", err)
		}
		idcStartURL = strings.TrimSpace(idcStartURL)
		if idcStartURL == "" {
			return "", "", "", fmt.Errorf("kiro: IDC login requires a start URL")
		}

		regionDefault := strings.TrimSpace(defaultRegion)
		if regionDefault == "" {
			regionDefault = kiro.DefaultRegion
		}
		region, err = promptFn(fmt.Sprintf("AWS Region (default %s): ", regionDefault))
		if err != nil {
			return "", "", "", fmt.Errorf("failed to read AWS region: %w", err)
		}
		region = strings.TrimSpace(region)
		if region == "" {
			region = regionDefault
		}
		return "idc", idcStartURL, region, nil
	default:
		// Any other input (including empty) selects the Builder ID device flow.
		return "builder-id", "", strings.TrimSpace(defaultRegion), nil
	}
}
