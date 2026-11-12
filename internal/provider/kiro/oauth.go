package kiro

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"router-go/internal/provider"
)

// Kiro has several kinds of account, told apart by providerSpecificData.authMethod:
//
//	builder-id   AWS Builder ID, signed in here by device code (poll flow). Refreshed at AWS OIDC with the client the
//	             sign-in registered (clientId + clientSecret).
//	idc          IAM Identity Center (enterprise): imported; refreshed at AWS OIDC in the account's region.
//	social       Google/GitHub: imported refresh token, refreshed at Kiro's own auth service.
//	external_idp enterprise IdP (Microsoft Entra): imported; refreshed at the IdP's token endpoint.
//
// Imports go through the add-key dialog (OAuth.Import): paste a refresh token or a JSON blob.
// Skipped on purpose: browser sign-in for Google/GitHub (kiro:// redirect, needs a login-method choice in the modal)
// and device sign-in for IDC (needs a start URL input); both come in through Import instead.

const (
	startURL   = "https://view.awsapps.com/start"
	issuerURL  = "https://identitycenter.amazonaws.com/ssoins-722374e8c3c8e6c6"
	clientName = "router-go"
)

var (
	scopes   = []string{"codewhisperer:completions", "codewhisperer:analysis", "codewhisperer:conversations"}
	regionRe = regexp.MustCompile(`^[a-z]{2}-[a-z]+-\d{1,2}$`)
	// A refresh token is sent to token_endpoint, so only Microsoft's login hosts are accepted there.
	idpHosts = map[string]bool{"login.microsoftonline.com": true, "login.microsoft.com": true, "login.windows.net": true}
)

var oauth = &provider.OAuth{Kind: provider.OAuthPoll, Start: start, Poll: poll, RefreshConn: refresh, Import: importToken}

func socialBase() string { p, _ := provider.ByID("kiro"); return p.OAuthBase }

// oidc is the AWS SSO OIDC base URL of a region; the region is checked because it becomes part of a host name.
func oidc(region string) (string, error) {
	region = cmp.Or(strings.TrimSpace(region), "us-east-1")
	if !regionRe.MatchString(region) {
		return "", errors.New("invalid AWS region " + region)
	}
	return "https://oidc." + region + ".amazonaws.com", nil
}

func str(m map[string]any, k string) string { s, _ := m[k].(string); return strings.TrimSpace(s) }

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

// post sends a JSON call and decodes the answer into out; a status >= 300 is an error carrying the body.
func post(ctx context.Context, d provider.Doer, what, url string, headers map[string]string, body, out any) error {
	raw, status, err := provider.DoJSON(ctx, d, "POST", url, headers, body)
	if err != nil {
		return err
	}
	if status >= 300 {
		return fmt.Errorf("%s failed (%d): %s", what, status, snippet(raw))
	}
	return json.Unmarshal(raw, out)
}

// ---- Builder ID: device code ----

type deviceState struct {
	DeviceCode, ClientID, ClientSecret string
}

// start registers a public OIDC client, asks for a device code and hands the verification link to the dashboard.
// The state it returns carries the client and device code (the dashboard is the only party that sees it).
func start(ctx context.Context, d provider.Doer, _ provider.StartParams) (provider.StartResult, error) {
	base, _ := oidc("")
	var client struct{ ClientID, ClientSecret string }
	err := post(ctx, d, "Kiro client registration", base+"/client/register", nil, map[string]any{
		"clientName": clientName, "clientType": "public", "scopes": scopes, "issuerUrl": issuerURL,
		"grantTypes": []string{"urn:ietf:params:oauth:grant-type:device_code", "refresh_token"},
	}, &client)
	if err != nil {
		return provider.StartResult{}, err
	}
	var dev struct {
		DeviceCode, VerificationURI, VerificationURIComplete string
		ExpiresIn, Interval                                  int
	}
	err = post(ctx, d, "Kiro device authorization", base+"/device_authorization", nil,
		map[string]any{"clientId": client.ClientID, "clientSecret": client.ClientSecret, "startUrl": startURL}, &dev)
	if err != nil {
		return provider.StartResult{}, err
	}
	st, _ := json.Marshal(deviceState{dev.DeviceCode, client.ClientID, client.ClientSecret})
	return provider.StartResult{AuthURL: cmp.Or(dev.VerificationURIComplete, dev.VerificationURI), State: base64.RawURLEncoding.EncodeToString(st),
		Interval: cmp.Or(dev.Interval, 5), ExpiresIn: cmp.Or(dev.ExpiresIn, 600)}, nil
}

func poll(ctx context.Context, d provider.Doer, state string) (provider.Tokens, bool, error) {
	var st deviceState
	raw, err := base64.RawURLEncoding.DecodeString(state)
	if err != nil || json.Unmarshal(raw, &st) != nil || st.DeviceCode == "" {
		return provider.Tokens{}, false, errors.New("invalid sign-in state: start again")
	}
	base, _ := oidc("")
	body, status, err := provider.DoJSON(ctx, d, "POST", base+"/token", nil, map[string]any{
		"clientId": st.ClientID, "clientSecret": st.ClientSecret, "deviceCode": st.DeviceCode, "grantType": "urn:ietf:params:oauth:grant-type:device_code"})
	if err != nil {
		return provider.Tokens{}, false, err
	}
	var r struct {
		AccessToken, RefreshToken, ProfileArn string
		ExpiresIn                             int
		Error                                 string
		ErrorDescription                      string `json:"error_description"`
	}
	if json.Unmarshal(body, &r) != nil {
		return provider.Tokens{}, false, fmt.Errorf("Kiro sign-in: unexpected answer (%d): %s", status, snippet(body))
	}
	switch {
	case r.AccessToken != "":
		psd := map[string]any{"authMethod": "builder-id", "clientId": st.ClientID, "clientSecret": st.ClientSecret, "region": "us-east-1", "startUrl": startURL}
		if arn := profileOf(ctx, d, r.ProfileArn, r.AccessToken); arn != "" {
			psd["profileArn"] = arn
		}
		t := tokens(r.AccessToken, r.RefreshToken, r.ExpiresIn, psd)
		t.Name = "Kiro Builder ID " + short(st.ClientID)
		return t, false, nil
	case r.Error == "authorization_pending" || r.Error == "slow_down":
		return provider.Tokens{}, true, nil
	}
	return provider.Tokens{}, false, errors.New("Kiro sign-in failed: " + cmp.Or(r.ErrorDescription, r.Error, snippet(body)))
}

// ---- refresh ----

// refresh renews a sign-in the way its account type needs (see the table at the top). Besides the tokens it
// returns the profile ARN as Extra when the connection does not have one yet.
func refresh(ctx context.Context, d provider.Doer, refreshToken string, psd map[string]any) (provider.Tokens, error) {
	if str(psd, "authMethod") == "external_idp" {
		return refreshIdP(ctx, d, refreshToken, psd)
	}
	endpoint, body := socialBase()+"/refreshToken", map[string]any{"refreshToken": refreshToken}
	what := "Kiro token refresh"
	if id, secret := str(psd, "clientId"), str(psd, "clientSecret"); id != "" && secret != "" {
		base, err := oidc(str(psd, "region"))
		if err != nil {
			return provider.Tokens{}, err
		}
		endpoint, what = base+"/token", "Kiro AWS token refresh"
		body = map[string]any{"clientId": id, "clientSecret": secret, "refreshToken": refreshToken, "grantType": "refresh_token"}
	}
	var r struct {
		AccessToken, RefreshToken, ProfileArn string
		ExpiresIn                             int
	}
	if err := post(ctx, d, what, endpoint, nil, body, &r); err != nil {
		return provider.Tokens{}, err
	}
	if r.AccessToken == "" {
		return provider.Tokens{}, errors.New(what + " returned no access token")
	}
	t := tokens(r.AccessToken, cmp.Or(r.RefreshToken, refreshToken), r.ExpiresIn, nil)
	if str(psd, "profileArn") == "" {
		if arn := profileOf(ctx, d, r.ProfileArn, r.AccessToken); arn != "" {
			t.Extra = map[string]any{"profileArn": arn}
		}
	}
	return t, nil
}

// idpParams validates the settings an enterprise-IdP refresh needs; the token endpoint must be a Microsoft login host.
func idpParams(psd map[string]any) (endpoint, clientID, scope string, err error) {
	endpoint, clientID, scope = str(psd, "tokenEndpoint"), str(psd, "clientId"), str(psd, "scope")
	u, perr := url.Parse(endpoint)
	switch {
	case perr != nil || u.Scheme != "https" || !idpHosts[strings.ToLower(u.Hostname())]:
		err = errors.New("tokenEndpoint must be an https Microsoft login endpoint")
	case clientID == "" || scope == "":
		err = errors.New("clientId and scope are required for external_idp")
	}
	return
}

func refreshIdP(ctx context.Context, d provider.Doer, refreshToken string, psd map[string]any) (provider.Tokens, error) {
	endpoint, clientID, scope, err := idpParams(psd)
	if err != nil {
		return provider.Tokens{}, err
	}
	form := url.Values{"grant_type": {"refresh_token"}, "client_id": {clientID}, "refresh_token": {refreshToken}, "scope": {scope}}
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return provider.Tokens{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := d.Do(req)
	if err != nil {
		return provider.Tokens{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var r struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if resp.StatusCode >= 300 || json.Unmarshal(raw, &r) != nil || r.AccessToken == "" {
		return provider.Tokens{}, fmt.Errorf("Kiro enterprise IdP refresh failed (%d): %s", resp.StatusCode, snippet(raw))
	}
	return tokens(r.AccessToken, cmp.Or(r.RefreshToken, refreshToken), r.ExpiresIn, nil), nil
}

// profileOf is the ARN the token endpoint returned, else the account's own as CodeWhisperer lists it.
func profileOf(ctx context.Context, d provider.Doer, arn, accessToken string) string {
	if arn = strings.TrimSpace(arn); arn != "" {
		return arn
	}
	return lookupProfile(ctx, d, accessToken)
}

// lookupProfile asks CodeWhisperer for the account's profile ARN; "" when it cannot say (the shared default applies).
func lookupProfile(ctx context.Context, d provider.Doer, accessToken string) string {
	var r struct{ Profiles []struct{ Arn string } }
	h := identity(accessToken, false)
	h["Authorization"] = "Bearer " + accessToken
	err := post(ctx, d, "Kiro profile lookup", "https://codewhisperer.us-east-1.amazonaws.com/ListAvailableProfiles", h, map[string]any{"maxResults": 10}, &r)
	if err != nil {
		return ""
	}
	for _, p := range r.Profiles {
		if arn := strings.TrimSpace(p.Arn); arn != "" {
			return arn
		}
	}
	return ""
}

// ---- import ----

// importToken turns what was pasted into the add-key dialog into a working sign-in: a bare refresh token (Google/GitHub
// account), or a JSON object (camelCase or snake_case keys) with refreshToken and, for IDC, clientId + clientSecret +
// region, or for external_idp, authMethod + clientId + tokenEndpoint + scope + profileArn. It is validated by refreshing once.
func importToken(ctx context.Context, d provider.Doer, raw string) (provider.Tokens, error) {
	raw = strings.TrimSpace(raw)
	in := map[string]any{"refreshToken": raw}
	if strings.HasPrefix(raw, "{") {
		in = map[string]any{}
		if err := json.Unmarshal([]byte(raw), &in); err != nil {
			return provider.Tokens{}, errors.New("the pasted JSON is not valid: " + err.Error())
		}
	}
	pick := func(keys ...string) string {
		for _, k := range keys {
			if v := str(in, k); v != "" {
				return v
			}
			if list, ok := in[k].([]any); ok { // scopes may be a list
				var parts []string
				for _, s := range list {
					if p, _ := s.(string); p != "" {
						parts = append(parts, p)
					}
				}
				return strings.Join(parts, " ")
			}
		}
		return ""
	}
	rt := pick("refreshToken", "refresh_token")
	if rt == "" {
		return provider.Tokens{}, errors.New("a refresh token is required")
	}
	id, secret := pick("clientId", "client_id"), pick("clientSecret", "client_secret")
	method := pick("authMethod", "auth_method")
	psd := map[string]any{}
	switch {
	case method == "external_idp":
		psd["tokenEndpoint"], psd["scope"], psd["clientId"] = pick("tokenEndpoint", "token_endpoint"), pick("scope", "scopes"), id
		if _, _, _, err := idpParams(psd); err != nil {
			return provider.Tokens{}, err
		}
		if pick("profileArn", "profile_arn") == "" {
			return provider.Tokens{}, errors.New("profileArn is required for external_idp")
		}
	case id != "" && secret != "":
		method = cmp.Or(method, "idc")
		psd["clientId"], psd["clientSecret"] = id, secret
	default:
		method = cmp.Or(method, "imported") // google | github | social also select the shared social profile
	}
	psd["authMethod"], psd["region"] = method, cmp.Or(pick("region"), "us-east-1")
	if arn := pick("profileArn", "profile_arn"); arn != "" {
		psd["profileArn"] = arn
	}
	t, err := refresh(ctx, d, rt, psd)
	if err != nil {
		return provider.Tokens{}, err
	}
	for k, v := range t.Extra {
		psd[k] = v
	}
	t.Extra, t.Name = psd, "Kiro "+method+" "+short(rt)
	return t, nil
}

// ---- helpers ----

func tokens(access, refresh string, expiresIn int, extra map[string]any) provider.Tokens {
	return provider.Tokens{AccessToken: access, RefreshToken: refresh, ExpiresAt: time.Now().Add(time.Duration(cmp.Or(expiresIn, 3600)) * time.Second),
		Email: jwtEmail(access), Extra: extra}
}

// short is a stable tag of a secret, to tell accounts apart in the connection list.
func short(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:3]) }

// jwtEmail reads the e-mail out of an access token that happens to be a JWT (AWS ones are opaque: "" then).
func jwtEmail(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ""
	}
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return ""
	}
	var c map[string]any
	if json.Unmarshal(bytes.TrimSpace(b), &c) != nil {
		return ""
	}
	return cmp.Or(str(c, "email"), str(c, "preferred_username"), str(c, "upn"))
}
