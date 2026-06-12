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
// opts.Metadata["method"] (builder-id|idc|import|social); builder-id is the default.
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
	case "social", "sso":
		bundle, err = a.loginSocial(ctx, authSvc, opts)
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
	username := strings.TrimSpace(opts.Metadata["username"])
	startURL := kiro.BuilderIDStartURL
	if method == "idc" {
		startURL = strings.TrimSpace(opts.Metadata["idc_start_url"])
		if startURL == "" {
			return nil, fmt.Errorf("kiro: IDC login requires a start URL")
		}
		// The IDC access token is an opaque AWS blob with no derivable identity, so the
		// account label that names the auth file (kiro-<username>-<directoryID>.json)
		// cannot be inferred and must be supplied by the operator.
		if username == "" {
			return nil, fmt.Errorf("kiro: IDC login requires a username")
		}
		// Reject a username that carries no filename-safe characters; it would collapse
		// to "" during filename generation and silently fall back to a timestamp name,
		// breaking the kiro-<username>-<directoryID>.json contract.
		if kiro.SanitizeFileComponent(username) == "" {
			return nil, fmt.Errorf("kiro: IDC username has no filename-safe characters")
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

	// AWS returns the verification URL with the user_code embedded for one-click approval.
	// This holds for BOTH methods: Builder ID -> https://view.awsapps.com/start/#/device?user_code=XXXX
	// and IDC -> {startURL}/#/device?user_code=XXXX. Prefer the complete form; fall back to
	// the bare verification URI (user enters the code, printed below) only if AWS omits it.
	browserURL := deviceCode.VerificationURIComplete
	if browserURL == "" {
		browserURL = deviceCode.VerificationURI
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

	// The runtime generate endpoint requires a profileArn. The device-code token exchange
	// does not return one (only the social refresh path does), so resolve it once here and
	// persist it in the bundle. Without this the executor must re-resolve it on every
	// request (it only mutates a per-request auth clone, so its resolution never persists).
	profileArn := tokenData.ProfileArn
	if profileArn == "" {
		resolvedTokenData, resolvedProfileArn, errResolve := authSvc.ResolveProfileArn(ctx, tokenData, kiro.RefreshParams{
			RefreshToken: tokenData.RefreshToken,
			ClientID:     clientID,
			ClientSecret: clientSecret,
			Region:       region,
		})
		if resolvedTokenData != nil {
			tokenData = resolvedTokenData
		}
		if errResolve != nil {
			log.Warnf("kiro: failed to resolve profile ARN at login: %v", errResolve)
		} else {
			profileArn = resolvedProfileArn
		}
	}
	// Some IDC tenants return an empty profile list even after the OIDC refresh retry.
	// Reuse a previously resolved profileArn from the same start URL and region when one
	// already exists in the configured auth directory.
	if profileArn == "" {
		if cachedProfileArn := authSvc.CachedProfileArnForStartURL(startURL, region); cachedProfileArn != "" {
			profileArn = cachedProfileArn
		}
	}
	// Reject a credential that still has no profileArn: the runtime generate endpoint
	// will hard-fail every request, so persisting it only creates a broken auth record.
	if errProfile := kiro.RequireProfileArn(profileArn, "kiro login"); errProfile != nil {
		return nil, errProfile
	}

	return &kiro.KiroAuthBundle{
		TokenData:    tokenData,
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Region:       region,
		AuthMethod:   method,
		StartURL:     startURL,
		Username:     username,
		ProfileArn:   profileArn,
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
	// Imported credentials must already carry the runtime profileArn because there is no
	// separate login-time recovery step after the file is persisted.
	if errProfile := kiro.RequireProfileArn(tokenData.ProfileArn, "kiro import"); errProfile != nil {
		return nil, errProfile
	}

	return &kiro.KiroAuthBundle{
		TokenData:  tokenData,
		Region:     kiro.DefaultRegion,
		AuthMethod: "import",
		ProfileArn: tokenData.ProfileArn,
		Email:      kiro.ExtractEmailFromJWT(tokenData.AccessToken),
	}, nil
}

// loginSocial runs the Kiro social / enterprise SSO sign-in (the flow the Kiro IDE uses).
// It performs a PKCE authorization-code flow against the Kiro-hosted portal — which
// federates Google, GitHub, and enterprise IdPs (e.g. an Azure AD tenant) — capturing the
// authorization code on a transient loopback listener and exchanging it for Kiro tokens.
func (a KiroAuthenticator) loginSocial(ctx context.Context, authSvc *kiro.KiroAuth, opts *LoginOptions) (*kiro.KiroAuthBundle, error) {
	region := strings.TrimSpace(opts.Metadata["region"])
	if region == "" {
		region = kiro.DefaultRegion
	}

	pkce, err := kiro.GenerateSocialPKCE()
	if err != nil {
		return nil, err
	}

	// Bind the loopback listener BEFORE opening the browser so the redirect cannot race
	// ahead of a ready listener. The port is fixed because the portal validates the
	// redirect URI; cleanup releases it once the flow completes or aborts.
	resultCh, cleanup, err := kiro.StartSocialCallbackListener(pkce.State)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	signInURL := kiro.SocialSignInURL(pkce)
	fmt.Printf("\nTo authenticate with Kiro SSO, please visit:\n%s\n\n", signInURL)
	if !opts.NoBrowser && browser.IsAvailable() {
		if errOpen := browser.OpenURL(signInURL); errOpen != nil {
			log.Warnf("Failed to open browser automatically: %v", errOpen)
		} else {
			fmt.Println("Browser opened automatically.")
		}
	}
	fmt.Println("Waiting for SSO authorization...")

	var code string
	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("kiro: %w", ctx.Err())
	case res := <-resultCh:
		if res.Err != nil {
			return nil, res.Err
		}
		code = res.Code
	case <-time.After(kiro.SocialLoginTimeout):
		return nil, fmt.Errorf("kiro: SSO login timed out after %s", kiro.SocialLoginTimeout)
	}

	tokenData, err := authSvc.ExchangeSocialCode(ctx, code, pkce.Verifier)
	if err != nil {
		return nil, fmt.Errorf("kiro: SSO token exchange failed: %w", err)
	}

	// The runtime generate endpoint requires a profileArn. The social exchange usually
	// returns one; if not, resolve it via ListAvailableProfiles using the access token.
	profileArn := tokenData.ProfileArn
	if profileArn == "" {
		resolvedTokenData, resolvedProfileArn, errResolve := authSvc.ResolveProfileArn(ctx, tokenData, kiro.RefreshParams{
			RefreshToken: tokenData.RefreshToken,
			Region:       region,
		})
		if resolvedTokenData != nil {
			tokenData = resolvedTokenData
		}
		if errResolve != nil {
			log.Warnf("kiro: failed to resolve profile ARN at social login: %v", errResolve)
		} else {
			profileArn = resolvedProfileArn
		}
	}
	// Refuse to persist a credential with no profileArn: every runtime request would fail.
	if errProfile := kiro.RequireProfileArn(profileArn, "kiro social login"); errProfile != nil {
		return nil, errProfile
	}

	return &kiro.KiroAuthBundle{
		TokenData:  tokenData,
		Region:     region,
		AuthMethod: "social",
		ProfileArn: profileArn,
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
		Username:     bundle.Username,
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
	if bundle.Username != "" {
		metadata["username"] = bundle.Username
	}
	if bundle.Email != "" {
		metadata["email"] = bundle.Email
	}

	label := "Kiro User"
	if bundle.Username != "" {
		label = bundle.Username
	} else if bundle.Email != "" {
		label = bundle.Email
	}

	fileName := kiro.CredentialFileName(bundle.AuthMethod, bundle.Username, bundle.StartURL, bundle.Email, time.Now().UnixMilli())
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
