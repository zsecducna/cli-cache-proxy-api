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

// loginSocial runs the Kiro browser sign-in (the flow the Kiro IDE uses). It performs a
// PKCE authorization-code flow against the Kiro-hosted portal, which branches by account
// type: Google/GitHub social accounts return a Cognito code directly, while enterprise
// accounts (an external IdP such as an Azure AD tenant) trigger a second OIDC leg that the
// loopback listener drives. Both legs land on the transient loopback listener; this method
// then exchanges the captured code and resolves the profile ARN.
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
	// ahead of a ready listener. The listener also drives the enterprise leg by 302-redirecting
	// the browser on to the IdP and capturing the returned code; cleanup releases the port.
	resultCh, cleanup, err := authSvc.StartKiroLoginListener(pkce.State)
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

	var res kiro.KiroLoginResult
	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("kiro: %w", ctx.Err())
	case r := <-resultCh:
		if r.Err != nil {
			return nil, r.Err
		}
		res = r
	case <-time.After(kiro.SocialLoginTimeout):
		return nil, fmt.Errorf("kiro: SSO login timed out after %s", kiro.SocialLoginTimeout)
	}

	var tokenData *kiro.KiroTokenData
	bundle := &kiro.KiroAuthBundle{Region: region}
	switch res.Kind {
	case kiro.KiroLoginExternalIdp:
		bundle.AuthMethod = "external_idp"
		bundle.ClientID = res.ClientID
		bundle.TokenEndpoint = res.TokenEndpoint
		bundle.IssuerURL = res.IssuerURL
		bundle.Scopes = res.Scopes
		tokenData, err = authSvc.ExchangeExternalIdpCode(ctx, res.TokenEndpoint, res.ClientID, res.Code, res.CodeVerifier, res.RedirectURI, res.Scopes)
		if err != nil {
			return nil, fmt.Errorf("kiro: enterprise SSO token exchange failed: %w", err)
		}
	default:
		bundle.AuthMethod = "social"
		tokenData, err = authSvc.ExchangeSocialCode(ctx, res.Code, pkce.Verifier)
		if err != nil {
			return nil, fmt.Errorf("kiro: SSO token exchange failed: %w", err)
		}
	}

	// The runtime generate endpoint requires a profileArn. Social exchange may return one;
	// otherwise (and always for external IdP) resolve it via ListAvailableProfiles.
	profileArn := tokenData.ProfileArn
	if profileArn == "" {
		resolvedTokenData, resolvedProfileArn, errResolve := authSvc.ResolveProfileArn(ctx, tokenData, kiro.RefreshParams{
			RefreshToken:  tokenData.RefreshToken,
			Region:        region,
			ClientID:      bundle.ClientID,
			TokenEndpoint: bundle.TokenEndpoint,
			Scopes:        bundle.Scopes,
		})
		if resolvedTokenData != nil {
			tokenData = resolvedTokenData
		}
		if errResolve != nil {
			log.Warnf("kiro: failed to resolve profile ARN at %s login: %v", bundle.AuthMethod, errResolve)
		} else {
			profileArn = resolvedProfileArn
		}
	}
	// The runtime generate endpoint requires a profileArn for every method, so refuse to
	// persist a credential without one rather than create a record that 400s on every request.
	if errProfile := kiro.RequireProfileArn(profileArn, "kiro "+bundle.AuthMethod+" login"); errProfile != nil {
		return nil, errProfile
	}

	bundle.TokenData = tokenData
	bundle.ProfileArn = profileArn
	bundle.Email = kiro.ExtractEmailFromJWT(tokenData.AccessToken)
	return bundle, nil
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
		AuthMethod:    bundle.AuthMethod,
		StartURL:      bundle.StartURL,
		Username:      bundle.Username,
		Email:         bundle.Email,
		TokenEndpoint: bundle.TokenEndpoint,
		IssuerURL:     bundle.IssuerURL,
		Scopes:        bundle.Scopes,
		Type:          "kiro",
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
	// External IdP (enterprise) refresh material: the executor refreshes against the IdP
	// token endpoint using the IdP client id (stored as client_id) and these scopes.
	if bundle.TokenEndpoint != "" {
		metadata["token_endpoint"] = bundle.TokenEndpoint
	}
	if bundle.IssuerURL != "" {
		metadata["issuer_url"] = bundle.IssuerURL
	}
	if bundle.Scopes != "" {
		metadata["scopes"] = bundle.Scopes
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
