package auth

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/kiro"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// kiroRefreshLead is the duration before token expiry when a refresh should occur.
var kiroRefreshLead = 5 * time.Minute

// KiroAuthenticator implements the AWS SSO OIDC device flow (and refresh-token import)
// login for Amazon Kiro (AWS CodeWhisperer).
type KiroAuthenticator struct{}

// NewKiroAuthenticator constructs a new Kiro authenticator.
func NewKiroAuthenticator() Authenticator {
	return &KiroAuthenticator{}
}

// Provider returns the provider key for kiro.
func (KiroAuthenticator) Provider() string {
	return "kiro"
}

// RefreshLead returns the duration before token expiry when refresh should occur.
func (KiroAuthenticator) RefreshLead() *time.Duration {
	return &kiroRefreshLead
}

// Login runs the selected Kiro authentication method. The method is read from
// opts.Metadata["method"] (builder-id|idc|import); builder-id is the default.
func (a KiroAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy auth: configuration is required")
	}
	if opts == nil {
		opts = &LoginOptions{}
	}

	method := strings.TrimSpace(opts.Metadata["method"])
	if method == "" {
		method = "builder-id"
	}

	authSvc := kiro.NewKiroAuth(cfg)

	var bundle *kiro.KiroAuthBundle
	var err error
	switch method {
	case "import":
		bundle, err = a.loginImport(ctx, authSvc, opts)
	case "idc":
		bundle, err = a.loginDevice(ctx, authSvc, opts, method)
	default:
		method = "builder-id"
		bundle, err = a.loginDevice(ctx, authSvc, opts, method)
	}
	if err != nil {
		return nil, err
	}

	return buildKiroAuth(bundle), nil
}

// loginDevice runs the Builder ID / IDC device authorization flow.
func (a KiroAuthenticator) loginDevice(ctx context.Context, authSvc *kiro.KiroAuth, opts *LoginOptions, method string) (*kiro.KiroAuthBundle, error) {
	region := strings.TrimSpace(opts.Metadata["region"])
	if region == "" {
		region = kiro.DefaultRegion
	}
	startURL := kiro.BuilderIDStartURL
	if method == "idc" {
		startURL = strings.TrimSpace(opts.Metadata["idc_start_url"])
		if startURL == "" {
			return nil, fmt.Errorf("kiro: IDC login requires a start URL")
		}
	}

	fmt.Println("Starting Kiro authentication...")
	clientID, clientSecret, err := authSvc.RegisterClient(ctx, region)
	if err != nil {
		return nil, fmt.Errorf("kiro: failed to register client: %w", err)
	}

	deviceCode, err := authSvc.StartDeviceAuthorization(ctx, clientID, clientSecret, startURL, region)
	if err != nil {
		return nil, fmt.Errorf("kiro: failed to start device authorization: %w", err)
	}

	// Builder ID opens the AWS-returned verification URL, which embeds the user_code for
	// one-click approval (e.g. https://view.awsapps.com/start/#/device?user_code=XXXX).
	verificationURL := deviceCode.VerificationURIComplete
	if verificationURL == "" {
		verificationURL = deviceCode.VerificationURI
	}
	// IDC instead opens the literal Identity Center start URL with region + auth_method
	// appended (per the documented IDC flow); the user_code is not embedded there, so it
	// is printed separately below for the user to enter manually.
	browserURL := verificationURL
	if method == "idc" {
		browserURL = kiro.IDCBrowserURL(startURL, region)
	}
	fmt.Printf("\nTo authenticate, please visit:\n%s\n\n", browserURL)
	if deviceCode.UserCode != "" {
		fmt.Printf("User code: %s\n\n", deviceCode.UserCode)
	}
	if !opts.NoBrowser && browser.IsAvailable() {
		if errOpen := browser.OpenURL(browserURL); errOpen != nil {
			log.Warnf("Failed to open browser automatically: %v", errOpen)
		} else {
			fmt.Println("Browser opened automatically.")
		}
	}

	fmt.Println("Waiting for authorization...")
	tokenData, err := authSvc.PollForToken(ctx, clientID, clientSecret, deviceCode.DeviceCode, region, deviceCode)
	if err != nil {
		return nil, fmt.Errorf("kiro: %w", err)
	}

	return &kiro.KiroAuthBundle{
		TokenData:    tokenData,
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Region:       region,
		AuthMethod:   method,
		StartURL:     startURL,
		ProfileArn:   tokenData.ProfileArn,
		Email:        kiro.ExtractEmailFromJWT(tokenData.AccessToken),
	}, nil
}

// loginImport validates a pasted or auto-detected refresh token by performing one refresh.
func (a KiroAuthenticator) loginImport(ctx context.Context, authSvc *kiro.KiroAuth, opts *LoginOptions) (*kiro.KiroAuthBundle, error) {
	refreshToken := strings.TrimSpace(opts.Metadata["import_token"])
	if refreshToken == "" {
		// Fall back to auto-detection from ~/.aws/sso/cache.
		detected, err := authSvc.AutoDetectToken()
		if err != nil {
			return nil, fmt.Errorf("kiro: no import token provided and auto-detect failed: %w", err)
		}
		refreshToken = detected
		fmt.Println("Auto-detected Kiro refresh token from ~/.aws/sso/cache.")
	}

	fmt.Println("Validating Kiro refresh token...")
	tokenData, err := authSvc.ValidateImportToken(ctx, refreshToken)
	if err != nil {
		return nil, fmt.Errorf("kiro: import validation failed: %w", err)
	}
	if tokenData.RefreshToken == "" {
		tokenData.RefreshToken = refreshToken
	}

	return &kiro.KiroAuthBundle{
		TokenData:  tokenData,
		Region:     kiro.DefaultRegion,
		AuthMethod: "import",
		ProfileArn: tokenData.ProfileArn,
		Email:      kiro.ExtractEmailFromJWT(tokenData.AccessToken),
	}, nil
}

// buildKiroAuth converts a successful login bundle into a coreauth.Auth record with
// both the persisted token storage and the runtime metadata the executor reads.
func buildKiroAuth(bundle *kiro.KiroAuthBundle) *coreauth.Auth {
	expired := ""
	if bundle.TokenData.ExpiresAt > 0 {
		expired = time.Unix(bundle.TokenData.ExpiresAt, 0).UTC().Format(time.RFC3339)
	}

	storage := &kiro.KiroTokenStorage{
		AccessToken:  bundle.TokenData.AccessToken,
		RefreshToken: bundle.TokenData.RefreshToken,
		Expired:      expired,
		ProfileArn:   bundle.ProfileArn,
		ClientID:     bundle.ClientID,
		ClientSecret: bundle.ClientSecret,
		Region:       bundle.Region,
		AuthMethod:   bundle.AuthMethod,
		StartURL:     bundle.StartURL,
		Email:        bundle.Email,
		Type:         "kiro",
	}

	// Metadata mirrors the storage fields; the executor reads credentials from here.
	metadata := map[string]any{
		"type":          "kiro",
		"access_token":  bundle.TokenData.AccessToken,
		"refresh_token": bundle.TokenData.RefreshToken,
		"region":        bundle.Region,
		"auth_method":   bundle.AuthMethod,
		"timestamp":     time.Now().UnixMilli(),
	}
	if expired != "" {
		metadata["expired"] = expired
	}
	if bundle.ProfileArn != "" {
		metadata["profile_arn"] = bundle.ProfileArn
	}
	if bundle.ClientID != "" {
		metadata["client_id"] = bundle.ClientID
	}
	if bundle.ClientSecret != "" {
		metadata["client_secret"] = bundle.ClientSecret
	}
	if bundle.StartURL != "" {
		metadata["start_url"] = bundle.StartURL
	}
	if bundle.Email != "" {
		metadata["email"] = bundle.Email
	}

	label := "Kiro User"
	if bundle.Email != "" {
		label = bundle.Email
	}

	fileName := fmt.Sprintf("kiro-%d.json", time.Now().UnixMilli())
	fmt.Println("\nKiro authentication successful!")

	return &coreauth.Auth{
		ID:       fileName,
		Provider: "kiro",
		FileName: fileName,
		Label:    label,
		Storage:  storage,
		Metadata: metadata,
	}
}
