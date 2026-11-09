package kiro

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"router-go/internal/provider"
)

// aws fakes Kiro's auth endpoints and every host the sign-in code talks to: a Doer redirects all requests to it and
// tells the handler which host was meant (X-Orig-Host), so the test can see which refresh variant went where.
type aws struct {
	t     *testing.T
	srv   *httptest.Server
	calls []string // "host path"
	last  map[string]any
	form  url.Values
	reply func(host, path string) (int, string)
}

func newAWS(t *testing.T, reply func(host, path string) (int, string)) *aws {
	a := &aws{t: t, reply: reply}
	a.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Header.Get("X-Orig-Host")
		a.calls = append(a.calls, host+r.URL.Path)
		raw, _ := io.ReadAll(r.Body)
		a.last, a.form = nil, nil
		if strings.Contains(r.Header.Get("Content-Type"), "x-www-form-urlencoded") {
			a.form, _ = url.ParseQuery(string(raw))
		} else {
			json.Unmarshal(raw, &a.last)
		}
		status, body := a.reply(host, r.URL.Path)
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
	t.Cleanup(a.srv.Close)
	return a
}

func (a *aws) Do(req *http.Request) (*http.Response, error) {
	req.Header.Set("X-Orig-Host", req.URL.Host)
	u, _ := url.Parse(a.srv.URL)
	req.URL.Scheme, req.URL.Host = u.Scheme, u.Host
	return http.DefaultClient.Do(req)
}

func TestRefreshSocial(t *testing.T) {
	a := newAWS(t, func(host, path string) (int, string) {
		return 200, `{"accessToken":"at2","refreshToken":"rt2","expiresIn":1800,"profileArn":"arn:aws:codewhisperer:us-east-1:1:profile/P"}`
	})
	tk, err := refresh(t.Context(), a, "rt1", map[string]any{"authMethod": "google"})
	if err != nil {
		t.Fatal(err)
	}
	if a.calls[0] != "prod.us-east-1.auth.desktop.kiro.dev/refreshToken" || a.last["refreshToken"] != "rt1" {
		t.Errorf("call = %v %v", a.calls, a.last)
	}
	if tk.AccessToken != "at2" || tk.RefreshToken != "rt2" || tk.Extra["profileArn"] != "arn:aws:codewhisperer:us-east-1:1:profile/P" ||
		time.Until(tk.ExpiresAt) < 29*time.Minute || time.Until(tk.ExpiresAt) > 31*time.Minute {
		t.Errorf("tokens = %+v", tk)
	}
	// a connection that already knows its ARN gets no Extra, and keeps its refresh token when none is returned
	a.reply = func(string, string) (int, string) { return 200, `{"accessToken":"at3"}` }
	tk, err = refresh(t.Context(), a, "rt2", map[string]any{"profileArn": "arn:own"})
	if err != nil || tk.RefreshToken != "rt2" || tk.Extra != nil || len(a.calls) != 2 {
		t.Errorf("second refresh = %+v %v calls=%v", tk, err, a.calls)
	}
	// rejected
	a.reply = func(string, string) (int, string) { return 400, `{"message":"Invalid refresh token"}` }
	if _, err = refresh(t.Context(), a, "bad", nil); err == nil || !strings.Contains(err.Error(), "Invalid refresh token") {
		t.Errorf("error = %v", err)
	}
}

func TestRefreshAWSOIDCAndProfileLookup(t *testing.T) {
	a := newAWS(t, func(host, path string) (int, string) {
		if strings.HasPrefix(host, "codewhisperer.") {
			return 200, `{"profiles":[{"arn":" "},{"arn":"arn:looked-up"}]}`
		}
		return 200, `{"accessToken":"at","refreshToken":"rt-new","expiresIn":3600}`
	})
	psd := map[string]any{"authMethod": "idc", "clientId": "cid", "clientSecret": "sec", "region": "eu-west-1"}
	tk, err := refresh(t.Context(), a, "rt", psd)
	if err != nil {
		t.Fatal(err)
	}
	if a.calls[0] != "oidc.eu-west-1.amazonaws.com/token" || len(a.calls) != 2 || a.calls[1] != "codewhisperer.us-east-1.amazonaws.com/ListAvailableProfiles" {
		t.Errorf("calls = %v", a.calls)
	}
	if tk.RefreshToken != "rt-new" || tk.Extra["profileArn"] != "arn:looked-up" {
		t.Errorf("tokens = %+v", tk)
	}
	// the OIDC call carried the client and the grant
	a.calls = nil
	a.reply = func(string, string) (int, string) { return 200, `{"accessToken":"at"}` }
	if _, err = refresh(t.Context(), a, "rt", map[string]any{"clientId": "cid", "clientSecret": "sec", "profileArn": "x"}); err != nil {
		t.Fatal(err)
	}
	if a.calls[0] != "oidc.us-east-1.amazonaws.com/token" || a.last["grantType"] != "refresh_token" || a.last["clientId"] != "cid" || a.last["clientSecret"] != "sec" || a.last["refreshToken"] != "rt" {
		t.Errorf("oidc call = %v %v", a.calls, a.last)
	}
	// a region is spliced into a host name: anything odd is refused before a request is made
	a.calls = nil
	if _, err = refresh(t.Context(), a, "rt", map[string]any{"clientId": "c", "clientSecret": "s", "region": "evil.com/x"}); err == nil || len(a.calls) != 0 {
		t.Errorf("bad region: err=%v calls=%v", err, a.calls)
	}
}

func TestRefreshExternalIdP(t *testing.T) {
	a := newAWS(t, func(string, string) (int, string) {
		return 200, `{"access_token":"idp-at","refresh_token":"idp-rt","expires_in":3599}`
	})
	psd := map[string]any{"authMethod": "external_idp", "clientId": "app", "scope": "api://x/.default offline_access", "tokenEndpoint": "https://login.microsoftonline.com/tenant/oauth2/v2.0/token"}
	tk, err := refresh(t.Context(), a, "rt", psd)
	if err != nil {
		t.Fatal(err)
	}
	if a.calls[0] != "login.microsoftonline.com/tenant/oauth2/v2.0/token" || a.form.Get("grant_type") != "refresh_token" || a.form.Get("client_id") != "app" ||
		a.form.Get("refresh_token") != "rt" || a.form.Get("scope") != "api://x/.default offline_access" {
		t.Errorf("form = %v at %v", a.form, a.calls)
	}
	if tk.AccessToken != "idp-at" || tk.RefreshToken != "idp-rt" || len(a.calls) != 1 { // no profile lookup: external_idp accounts bring their own ARN
		t.Errorf("tokens = %+v calls=%v", tk, a.calls)
	}
	// the refresh token only ever goes to a Microsoft login host over https
	a.calls = nil
	for _, ep := range []string{"https://evil.example/token", "http://login.microsoftonline.com/token", "", "https://login.microsoftonline.com.evil.example/token"} {
		psd["tokenEndpoint"] = ep
		if _, err := refresh(t.Context(), a, "rt", psd); err == nil || len(a.calls) != 0 {
			t.Errorf("endpoint %q accepted: err=%v calls=%v", ep, err, a.calls)
		}
	}
}

func TestBuilderIDDeviceFlow(t *testing.T) {
	polls := 0
	a := newAWS(t, func(host, path string) (int, string) {
		switch path {
		case "/client/register":
			return 200, `{"clientId":"cid","clientSecret":"csec"}`
		case "/device_authorization":
			return 200, `{"deviceCode":"dev","userCode":"ABCD-EFGH","verificationUri":"https://view.awsapps.com/start/#/device","verificationUriComplete":"https://view.awsapps.com/start/#/device?user_code=ABCD-EFGH","expiresIn":600,"interval":7}`
		case "/token":
			if polls++; polls == 1 {
				return 400, `{"error":"authorization_pending"}`
			}
			if polls == 2 {
				return 400, `{"error":"slow_down"}`
			}
			return 200, `{"accessToken":"at","refreshToken":"rt","expiresIn":3600}`
		}
		return 200, `{"profiles":[{"arn":"arn:builder"}]}`
	})
	res, err := start(t.Context(), a, provider.StartParams{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.AuthURL, "user_code=ABCD-EFGH") || res.Interval != 7 || res.ExpiresIn != 600 || res.State == "" {
		t.Errorf("start = %+v", res)
	}
	if a.calls[0] != "oidc.us-east-1.amazonaws.com/client/register" || a.calls[1] != "oidc.us-east-1.amazonaws.com/device_authorization" || a.last["clientId"] != "cid" || a.last["startUrl"] != startURL {
		t.Errorf("calls = %v %v", a.calls, a.last)
	}
	for range 2 {
		if _, pending, err := poll(t.Context(), a, res.State); !pending || err != nil {
			t.Fatalf("expected pending, got %v %v", pending, err)
		}
	}
	if a.last["deviceCode"] != "dev" || a.last["grantType"] != "urn:ietf:params:oauth:grant-type:device_code" || a.last["clientSecret"] != "csec" {
		t.Errorf("poll body = %v", a.last)
	}
	tk, pending, err := poll(t.Context(), a, res.State)
	if pending || err != nil || tk.AccessToken != "at" || tk.RefreshToken != "rt" || !strings.HasPrefix(tk.Name, "Kiro Builder ID ") {
		t.Fatalf("poll = %+v %v %v", tk, pending, err)
	}
	// what refresh needs later is kept on the connection
	if tk.Extra["authMethod"] != "builder-id" || tk.Extra["clientId"] != "cid" || tk.Extra["clientSecret"] != "csec" || tk.Extra["profileArn"] != "arn:builder" {
		t.Errorf("extra = %v", tk.Extra)
	}
	// denial and garbage
	a.reply = func(string, string) (int, string) {
		return 400, `{"error":"access_denied","error_description":"User denied"}`
	}
	if _, pending, err := poll(t.Context(), a, res.State); pending || err == nil || !strings.Contains(err.Error(), "User denied") {
		t.Errorf("denied: %v %v", pending, err)
	}
	if _, _, err := poll(t.Context(), a, "not-a-state"); err == nil {
		t.Error("garbage state accepted")
	}
}

func TestImport(t *testing.T) {
	jwt := "h." + base64.RawURLEncoding.EncodeToString([]byte(`{"email":"me@corp.test"}`)) + ".s"
	a := newAWS(t, func(host, path string) (int, string) {
		if strings.HasPrefix(host, "codewhisperer.") {
			return 200, `{"profiles":[]}`
		}
		return 200, `{"accessToken":"` + jwt + `","refreshToken":"rt-new","expiresIn":3600,"profileArn":"arn:resp"}`
	})
	// bare refresh token: a social/imported account
	tk, err := importToken(t.Context(), a, "  aorAAAAAGtoken  ")
	if err != nil || tk.Email != "me@corp.test" || tk.RefreshToken != "rt-new" || a.last["refreshToken"] != "aorAAAAAGtoken" || a.calls[0] != "prod.us-east-1.auth.desktop.kiro.dev/refreshToken" {
		t.Fatalf("bare token: %+v %v %v", tk, err, a.calls)
	}
	if tk.Extra["authMethod"] != "imported" || tk.Extra["profileArn"] != "arn:resp" {
		t.Errorf("extra = %v", tk.Extra)
	}
	// IDC JSON (snake_case keys work too)
	a.calls = nil
	tk, err = importToken(t.Context(), a, `{"refresh_token":"rt","client_id":"c","client_secret":"s","region":"eu-central-1"}`)
	if err != nil || a.calls[0] != "oidc.eu-central-1.amazonaws.com/token" || tk.Extra["authMethod"] != "idc" || tk.Extra["clientSecret"] != "s" || tk.Extra["region"] != "eu-central-1" {
		t.Fatalf("idc: %+v %v %v", tk, err, a.calls)
	}
	// external IdP JSON, scopes as a list
	a.calls = nil
	a.reply = func(string, string) (int, string) { return 200, `{"access_token":"x","expires_in":60}` }
	tk, err = importToken(t.Context(), a, `{"authMethod":"external_idp","refreshToken":"rt","clientId":"app","tokenEndpoint":"https://login.microsoftonline.com/t/oauth2/v2.0/token","scopes":["a","b"],"profileArn":"arn:idp"}`)
	if err != nil || a.form.Get("scope") != "a b" || tk.Extra["authMethod"] != "external_idp" || tk.Extra["profileArn"] != "arn:idp" || tk.Extra["scope"] != "a b" {
		t.Fatalf("external_idp: %+v %v form=%v", tk, err, a.form)
	}
	// rejected input
	for name, in := range map[string]string{
		"empty":            "",
		"json without rt":  `{"clientId":"c"}`,
		"broken json":      `{"refreshToken":`,
		"idp no profile":   `{"authMethod":"external_idp","refreshToken":"rt","clientId":"a","tokenEndpoint":"https://login.microsoftonline.com/t","scope":"s"}`,
		"idp foreign host": `{"authMethod":"external_idp","refreshToken":"rt","clientId":"a","tokenEndpoint":"https://evil.test/t","scope":"s","profileArn":"x"}`,
	} {
		if _, err := importToken(t.Context(), a, in); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	// the refresh failing means the token is bad: nothing is imported
	a.reply = func(string, string) (int, string) { return 401, `{"message":"expired"}` }
	if _, err := importToken(t.Context(), a, "rt"); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Errorf("bad token: %v", err)
	}
}
