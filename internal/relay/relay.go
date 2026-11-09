// Package relay deploys the small forwarding worker behind a Vercel / Cloudflare proxy pool.
// API tokens are used for the deploy call only; they are never stored.
package relay

import (
	"bytes"
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"regexp"
	"strings"
	"time"
)

//go:embed worker.js
var core string

const keyPlaceholder = "__RELAY_KEY__"

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,50}$`)

// Deployer talks to the two hosting APIs; the base URLs are fields so tests can point them at a fake.
type Deployer struct {
	HTTP      *http.Client
	VercelAPI string
	CFAPI     string
	PollEvery time.Duration
	PollFor   time.Duration
}

func New() *Deployer {
	return &Deployer{HTTP: &http.Client{Timeout: 60 * time.Second}, VercelAPI: "https://api.vercel.com", CFAPI: "https://api.cloudflare.com/client/v4", PollEvery: 3 * time.Second, PollFor: 2 * time.Minute}
}

// Result is what a deploy produces: the worker's public URL and its shared key.
type Result struct{ URL, Key string }

func NewKey() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func code(key string) string { return strings.Replace(core, keyPlaceholder, key, 1) }

// ValidName checks a project / worker name (lowercase letters, digits, dashes).
func ValidName(n string) error {
	if !nameRe.MatchString(n) {
		return errors.New("name must be lowercase letters, digits or dashes (max 51 characters)")
	}
	return nil
}

func (d *Deployer) call(ctx context.Context, method, url, token string, body io.Reader, ctype string, out any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	resp, err := d.HTTP.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if out != nil {
		_ = json.Unmarshal(raw, out)
	}
	if resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("%s", apiMessage(raw, resp.StatusCode))
	}
	return resp.StatusCode, nil
}

// apiMessage digs the human message out of a Vercel ({error:{message}}) or Cloudflare ({errors:[{message}]}) error.
func apiMessage(raw []byte, status int) string {
	var e struct {
		Error  struct{ Message string }   `json:"error"`
		Errors []struct{ Message string } `json:"errors"`
	}
	_ = json.Unmarshal(raw, &e)
	switch {
	case e.Error.Message != "":
		return e.Error.Message
	case len(e.Errors) > 0 && e.Errors[0].Message != "":
		return e.Errors[0].Message
	}
	return fmt.Sprintf("HTTP %d", status)
}

func jsonBody(v any) io.Reader { b, _ := json.Marshal(v); return bytes.NewReader(b) }

// Vercel deploys the relay as an Edge function behind a catch-all rewrite and waits until it is live.
func (d *Deployer) Vercel(ctx context.Context, token, name string) (Result, error) {
	key := NewKey()
	js := "export const config = { runtime: \"edge\" };\n" + code(key) + "\nexport default relay;\n"
	var dep struct {
		ID, UID, ProjectID, URL, ReadyState string
		Alias                               []string
	}
	_, err := d.call(ctx, "POST", d.VercelAPI+"/v13/deployments", token, jsonBody(map[string]any{
		"name": name, "target": "production", "projectSettings": map[string]any{"framework": nil},
		"files": []map[string]string{
			{"file": "api/relay.js", "data": js},
			{"file": "package.json", "data": `{"name":"` + name + `","version":"1.0.0"}`},
			{"file": "vercel.json", "data": `{"rewrites":[{"source":"/(.*)","destination":"/api/relay"}]}`},
		}}), "application/json", &dep)
	if err != nil {
		return Result{}, fmt.Errorf("Vercel: %w", err)
	}
	id := dep.ID
	if id == "" {
		id = dep.UID
	}
	// the relay is protected by its own key, so Vercel Authentication (which would answer 401) is switched off
	if _, err := d.call(ctx, "PATCH", d.VercelAPI+"/v9/projects/"+firstNonEmpty(dep.ProjectID, name), token, jsonBody(map[string]any{"ssoProtection": nil}), "application/json", nil); err != nil {
		return Result{}, fmt.Errorf("Vercel: could not disable deployment protection: %w", err)
	}
	deadline := time.Now().Add(d.PollFor)
	for {
		var st struct {
			ReadyState, URL string
			Alias           []string
		}
		if _, err := d.call(ctx, "GET", d.VercelAPI+"/v13/deployments/"+id, token, nil, "", &st); err != nil {
			return Result{}, fmt.Errorf("Vercel: %w", err)
		}
		switch st.ReadyState {
		case "READY":
			host := st.URL
			if len(st.Alias) > 0 { // the stable production alias survives redeploys
				host = st.Alias[0]
			}
			return Result{URL: "https://" + host, Key: key}, nil
		case "ERROR", "CANCELED":
			return Result{}, fmt.Errorf("Vercel: deployment %s", strings.ToLower(st.ReadyState))
		}
		if time.Now().After(deadline) {
			return Result{}, errors.New("Vercel: deployment timed out")
		}
		select {
		case <-ctx.Done():
			return Result{}, ctx.Err()
		case <-time.After(d.PollEvery):
		}
	}
}

func firstNonEmpty(a ...string) string {
	for _, s := range a {
		if s != "" {
			return s
		}
	}
	return ""
}

// Cloudflare uploads the relay as a module Worker and enables its workers.dev route.
func (d *Deployer) Cloudflare(ctx context.Context, accountID, token, name string) (Result, error) {
	key := NewKey()
	script := code(key) + "\nexport default { fetch: (req) => relay(req) };\n"
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part := func(field, file, ctype, content string) {
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`, field, file))
		h.Set("Content-Type", ctype)
		w, _ := mw.CreatePart(h)
		_, _ = w.Write([]byte(content))
	}
	part("index.js", "index.js", "application/javascript+module", script)
	part("metadata", "metadata.json", "application/json", `{"main_module":"index.js","compatibility_date":"2024-03-20"}`)
	_ = mw.Close()

	base := d.CFAPI + "/accounts/" + accountID
	if _, err := d.call(ctx, "PUT", base+"/workers/scripts/"+name, token, &buf, mw.FormDataContentType(), nil); err != nil {
		return Result{}, fmt.Errorf("Cloudflare: %w", err)
	}
	if _, err := d.call(ctx, "POST", base+"/workers/scripts/"+name+"/subdomain", token, jsonBody(map[string]any{"enabled": true}), "application/json", nil); err != nil {
		return Result{}, fmt.Errorf("Cloudflare: could not enable the workers.dev route: %w", err)
	}
	var sub struct{ Result struct{ Subdomain string } }
	if _, err := d.call(ctx, "GET", base+"/workers/subdomain", token, nil, "", &sub); err != nil || sub.Result.Subdomain == "" {
		return Result{}, errors.New("Cloudflare: the Worker is deployed but your account has no workers.dev subdomain — create one in the Cloudflare dashboard (Workers & Pages)")
	}
	return Result{URL: "https://" + name + "." + sub.Result.Subdomain + ".workers.dev", Key: key}, nil
}
