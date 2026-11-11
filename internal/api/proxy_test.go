package api_test

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"router-go/internal/api"
	"router-go/internal/db"
	"router-go/internal/provider"
	"router-go/internal/relay"
)

func (e *env) json(method, path, body string) (int, map[string]any) {
	e.t.Helper()
	req, _ := http.NewRequest(method, e.srv.URL+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestProxyPoolCRUDAndBinding(t *testing.T) {
	e := newEnv(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"data":[{"id":"m"}]}`) }, "chat")
	e.store.UpdateSettings(map[string]any{"requireLogin": false})
	c := e.addKey("k", "good")

	if code, out := e.json("POST", "/api/proxy-pools", `{"name":"","proxyUrl":"http://p:1"}`); code != 400 {
		t.Errorf("empty name = %d %v", code, out)
	}
	if code, _ := e.json("POST", "/api/proxy-pools", `{"name":"x","proxyUrl":"ftp://p:1"}`); code != 400 {
		t.Errorf("bad scheme = %d", code)
	}
	code, out := e.json("POST", "/api/proxy-pools", `{"name":"Office","proxyUrl":"http://user:secret@127.0.0.1:3128","strictProxy":true}`)
	pool := out["proxyPool"].(map[string]any)
	if code != 201 || pool["type"] != "http" || strings.Contains(pool["proxyUrl"].(string), "secret") || pool["strictProxy"] != true {
		t.Fatalf("create = %d %v", code, out)
	}
	id := pool["id"].(string)

	// the masked URL round-trips without clobbering the stored password
	e.json("PUT", "/api/proxy-pools/"+id, `{"name":"Office 2","proxyUrl":"`+pool["proxyUrl"].(string)+`"}`)
	if p, _ := e.store.ProxyPool(id); p.Name != "Office 2" || !strings.Contains(p.ProxyURL, "secret") {
		t.Errorf("stored pool = %+v", p)
	}

	if code, _ := e.json("PATCH", "/api/providers/"+c.ID, `{"proxyPoolId":"nope"}`); code != 400 {
		t.Errorf("unknown pool = %d", code)
	}
	if code, _ := e.json("PATCH", "/api/providers/"+c.ID, `{"proxyPoolId":"`+id+`"}`); code != 200 {
		t.Fatalf("bind = %d", code)
	}
	_, list := e.json("GET", "/api/proxy-pools", "")
	if n := list["proxyPools"].([]any)[0].(map[string]any)["boundConnectionCount"]; n != float64(1) {
		t.Errorf("usage = %v", n)
	}
	e.json("DELETE", "/api/proxy-pools/"+id, "")
	if got, _ := e.store.Connection(c.ID); got.Data["providerSpecificData"].(map[string]any)["proxyPoolId"] != nil {
		t.Errorf("binding left behind: %v", got.Data["providerSpecificData"])
	}
	if code, _ := e.json("DELETE", "/api/proxy-pools/"+id, ""); code != 404 {
		t.Errorf("second delete = %d", code)
	}
}

// A connection bound to a relay pool sends its chat request to the relay, which gets the real target in headers.
func TestChatThroughRelayAndHTTPProxy(t *testing.T) {
	var mu sync.Mutex
	var direct int
	e := newEnv(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		direct++
		mu.Unlock()
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"hi"}}]}`)
	}, "chat")
	e.store.UpdateSettings(map[string]any{"requireLogin": false})
	c := e.addKey("k", "good")

	var seen http.Header
	var seenURL string
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { // both a relay and a plain proxy
		seen, seenURL = r.Header.Clone(), r.URL.String()
		if r.URL.Path == "/health" {
			if r.Header.Get("x-relay-key") != "k1" {
				w.WriteHeader(403)
				return
			}
			io.WriteString(w, "ok")
			return
		}
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"via"}}]}`)
	}))
	defer fake.Close()
	chat := func() string {
		resp := e.chat(`{"model":"mock/m","messages":[{"role":"user","content":"x"}]}`, e.apiKey)
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return string(b)
	}

	relayPool := &db.ProxyPool{Name: "cf", Type: db.PoolCloudflare, ProxyURL: fake.URL, RelayKey: "k1"}
	if err := e.store.CreateProxyPool(relayPool); err != nil {
		t.Fatal(err)
	}
	e.store.SetConnectionProxy(c.ID, relayPool.ID)
	if body := chat(); !strings.Contains(body, "via") || seen.Get("x-relay-target") != e.node.BaseURL()[:strings.Index(e.node.BaseURL(), "/v1")] || seen.Get("x-relay-path") != "/v1/chat/completions" || seen.Get("x-relay-key") != "k1" {
		t.Errorf("relay not used: body=%s target=%q path=%q", body, seen.Get("x-relay-target"), seen.Get("x-relay-path"))
	}
	if direct != 0 {
		t.Errorf("upstream hit directly %d times", direct)
	}
	// pool test: right key passes, wrong key reports a refusal
	if _, out := e.json("POST", "/api/proxy-pools/"+relayPool.ID+"/test", ""); out["ok"] != true {
		t.Errorf("relay test = %v", out)
	}
	e.store.UpdateProxyPool(relayPool.ID, func(p *db.ProxyPool) { p.RelayKey = "wrong" })
	if _, out := e.json("POST", "/api/proxy-pools/"+relayPool.ID+"/test", ""); out["ok"] != false {
		t.Errorf("wrong key test = %v", out)
	}
	if p, _ := e.store.ProxyPool(relayPool.ID); p.TestStatus != "error" || p.LastError == "" {
		t.Errorf("status not recorded: %+v", p)
	}

	// plain HTTP proxy: the upstream is http://, so the proxy sees the absolute URL
	hp := &db.ProxyPool{Name: "px", Type: db.PoolHTTP, ProxyURL: fake.URL}
	e.store.CreateProxyPool(hp)
	e.store.SetConnectionProxy(c.ID, hp.ID)
	seenURL = ""
	if body := chat(); !strings.Contains(body, "via") || !strings.HasPrefix(seenURL, e.node.BaseURL()) {
		t.Errorf("http proxy not used: %s url=%q", body, seenURL)
	}

	// strict + disabled pool refuses instead of going direct; non-strict falls back to direct
	e.store.UpdateProxyPool(hp.ID, func(p *db.ProxyPool) { p.IsActive, p.StrictProxy = false, true })
	if body := chat(); strings.Contains(body, "hi") || direct != 0 {
		t.Errorf("strict pool leaked direct: %s direct=%d", body, direct)
	}
	e.store.UpdateProxyPool(hp.ID, func(p *db.ProxyPool) { p.StrictProxy = false })
	e.store.MarkConnection(c.ID, 0, "", 0, 0) // lift the cooldown the refused request caused
	if body := chat(); !strings.Contains(body, "hi") || direct != 1 {
		t.Errorf("non-strict fallback: %s direct=%d", body, direct)
	}
}

func TestDeployRelays(t *testing.T) {
	var cfScript string
	var polls int
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer tok") {
			w.WriteHeader(403)
			io.WriteString(w, `{"error":{"message":"Not authorized"}}`)
			return
		}
		switch {
		case r.URL.Path == "/v13/deployments" && r.Method == "POST":
			var in struct {
				Name  string
				Files []struct{ File, Data string }
			}
			json.NewDecoder(r.Body).Decode(&in)
			if len(in.Files) != 3 || !strings.Contains(in.Files[0].Data, `runtime: "edge"`) || strings.Contains(in.Files[0].Data, "__RELAY_KEY__") {
				t.Errorf("vercel files = %+v", in.Files)
			}
			io.WriteString(w, `{"id":"d1","projectId":"p1"}`)
		case r.URL.Path == "/v9/projects/p1":
			io.WriteString(w, `{}`)
		case r.URL.Path == "/v13/deployments/d1":
			if polls++; polls < 2 {
				io.WriteString(w, `{"readyState":"BUILDING"}`)
				return
			}
			io.WriteString(w, `{"readyState":"READY","url":"relay-abc.vercel.app"}`)
		case strings.HasPrefix(r.URL.Path, "/accounts/acc/workers/scripts/") && strings.HasSuffix(r.URL.Path, "/subdomain"):
			io.WriteString(w, `{"success":true}`)
		case strings.HasPrefix(r.URL.Path, "/accounts/acc/workers/scripts/") && r.Method == "PUT":
			b, _ := io.ReadAll(r.Body)
			cfScript = string(b)
			io.WriteString(w, `{"success":true}`)
		case r.URL.Path == "/accounts/acc/workers/subdomain":
			io.WriteString(w, `{"result":{"subdomain":"me"}}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer fake.Close()
	api.SetDeployer(func() *relay.Deployer {
		d := relay.New()
		d.VercelAPI, d.CFAPI, d.PollEvery = fake.URL, fake.URL, time.Millisecond
		return d
	})
	defer api.SetDeployer(relay.New)
	e := newEnv(t, func(w http.ResponseWriter, r *http.Request) {}, "chat")
	e.store.UpdateSettings(map[string]any{"requireLogin": false})

	if code, _ := e.json("POST", "/api/proxy-pools/vercel-deploy", `{"projectName":"Bad Name","vercelToken":"tok"}`); code != 400 {
		t.Errorf("bad name = %d", code)
	}
	if code, _ := e.json("POST", "/api/proxy-pools/vercel-deploy", `{"vercelToken":""}`); code != 400 {
		t.Errorf("missing token = %d", code)
	}
	if code, out := e.json("POST", "/api/proxy-pools/vercel-deploy", `{"projectName":"my-relay","vercelToken":"nope"}`); code != 502 || !strings.Contains(out["error"].(string), "Not authorized") {
		t.Errorf("bad token = %d %v", code, out)
	}
	code, out := e.json("POST", "/api/proxy-pools/vercel-deploy", `{"projectName":"my-relay","vercelToken":"tok"}`)
	pool := out["proxyPool"].(map[string]any)
	if code != 201 || pool["type"] != "vercel" || pool["proxyUrl"] != "https://relay-abc.vercel.app" || pool["relayKey"] != nil {
		t.Fatalf("vercel deploy = %d %v", code, out)
	}
	if p, _ := e.store.ProxyPool(pool["id"].(string)); len(p.RelayKey) != 48 {
		t.Errorf("relay key not stored: %q", p.RelayKey)
	}

	code, out = e.json("POST", "/api/proxy-pools/cloudflare-deploy", `{"projectName":"cf-relay","accountId":"acc","apiToken":"tok"}`)
	pool = out["proxyPool"].(map[string]any)
	if code != 201 || pool["type"] != "cloudflare" || pool["proxyUrl"] != "https://cf-relay.me.workers.dev" {
		t.Fatalf("cloudflare deploy = %d %v", code, out)
	}
	if !strings.Contains(cfScript, "export default { fetch") || strings.Contains(cfScript, "__RELAY_KEY__") {
		t.Errorf("worker script = %.200s", cfScript)
	}
}

// Model/key tests stream: CodeBuddy rejects non-stream chat, and other upstreams answer a stream far sooner.
func TestProbeRequestsStream(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	e := newEnv(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		if len(b) > 0 { // the key test also lists /models first
			bodies = append(bodies, string(b))
		}
		mu.Unlock()
		io.WriteString(w, "data: {}\n\n")
	}, "chat")
	e.store.UpdateSettings(map[string]any{"requireLogin": false})
	c := e.addKey("k", "good")
	e.json("POST", "/api/models/test", `{"model":"mock/m"}`)
	e.json("POST", "/api/providers/"+c.ID+"/test", `{"modelId":"m"}`)
	if len(bodies) != 2 {
		t.Fatalf("probes = %v", bodies)
	}
	for _, b := range bodies {
		if !strings.Contains(b, `"stream":true`) {
			t.Errorf("probe did not stream: %s", b)
		}
	}
}

// images / speech / transcriptions / videos are forwarded as they are, with only the model swapped for the upstream's.
func TestMediaPassthrough(t *testing.T) {
	var mu sync.Mutex
	got := map[string]string{}
	e := newEnv(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got[r.URL.Path] = string(b)
		got[r.URL.Path+"#ct"] = r.Header.Get("Content-Type")
		got[r.URL.Path+"#q"] = r.URL.RawQuery
		mu.Unlock()
		switch r.URL.Path {
		case "/v1/audio/speech":
			w.Header().Set("Content-Type", "audio/mpeg")
			w.Write([]byte{0xff, 0xfb, 0x90, 0x00, 0x01})
		case "/v1/videos/job1":
			io.WriteString(w, `{"id":"job1","status":"completed"}`)
		default:
			io.WriteString(w, `{"ok":true,"data":[{"url":"u"}]}`)
		}
	}, "chat")
	e.store.AddCustomModel("mock", "img-1", "")
	e.addKey("k", "good")

	// JSON kinds
	resp := e.post("/v1/images/generations", `{"model":"mock/img-1","prompt":"a cat","size":"256x256"}`, e.apiKey)
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(b), `"data"`) || !strings.Contains(got["/v1/images/generations"], `"model":"img-1"`) || !strings.Contains(got["/v1/images/generations"], `"prompt":"a cat"`) {
		t.Errorf("images = %d %s | upstream %s", resp.StatusCode, b, got["/v1/images/generations"])
	}
	// binary response comes back byte for byte with its content type
	resp = e.post("/v1/audio/speech", `{"model":"mock/img-1","input":"hi","voice":"alloy"}`, e.apiKey)
	b, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.Header.Get("Content-Type") != "audio/mpeg" || len(b) != 5 || b[0] != 0xff {
		t.Errorf("speech = %v %v", resp.Header.Get("Content-Type"), b)
	}
	// multipart: the file survives, the model is rewritten
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("model", "mock/img-1")
	fw, _ := mw.CreateFormFile("file", "a.wav")
	fw.Write([]byte("RIFFdata"))
	mw.WriteField("language", "id")
	mw.Close()
	req, _ := http.NewRequest("POST", e.srv.URL+"/v1/audio/transcriptions", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+e.apiKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	up := got["/v1/audio/transcriptions"]
	if resp.StatusCode != 200 || !strings.Contains(up, "RIFFdata") || !strings.Contains(up, "\r\n\r\nimg-1\r\n") || strings.Contains(up, "mock/img-1") || !strings.Contains(up, "language") || !strings.HasPrefix(got["/v1/audio/transcriptions#ct"], "multipart/form-data") {
		t.Errorf("transcription = %d upstream=%q ct=%q", resp.StatusCode, up, got["/v1/audio/transcriptions#ct"])
	}
	// video job: create, then poll with the model that created it
	resp = e.post("/v1/videos/generations", `{"model":"mock/img-1","prompt":"waves"}`, e.apiKey)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(got["/v1/videos/generations"], `"model":"img-1"`) {
		t.Errorf("video create = %d %s", resp.StatusCode, got["/v1/videos/generations"])
	}
	req, _ = http.NewRequest("GET", e.srv.URL+"/v1/videos/job1?model=mock/img-1&x=1", nil)
	req.Header.Set("Authorization", "Bearer "+e.apiKey)
	resp, _ = http.DefaultClient.Do(req)
	b, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(b), "completed") || got["/v1/videos/job1#q"] != "x=1" {
		t.Errorf("video poll = %d %s q=%q", resp.StatusCode, b, got["/v1/videos/job1#q"])
	}
	// no model / not signed in / unsupported provider
	if resp = e.post("/v1/images/generations", `{"prompt":"x"}`, e.apiKey); resp.StatusCode != 400 {
		t.Errorf("no model = %d", resp.StatusCode)
	}
	if resp = e.post("/v1/images/generations", `{"model":"mock/img-1"}`, ""); resp.StatusCode != 401 {
		t.Errorf("no key = %d", resp.StatusCode)
	}
	anth, _ := e.store.CreateNode(db.NodeTypeAnthropic, "Anth", "anth2", "", "http://127.0.0.1:1")
	e.store.AddCustomModel("anth2", "c", "")
	e.store.CreateConnection(anth, "k", "x")
	resp = e.post("/v1/images/generations", `{"model":"anth2/c","prompt":"x"}`, e.apiKey)
	b, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 400 || !strings.Contains(string(b), "does not support image generation") {
		t.Errorf("unsupported = %d %s", resp.StatusCode, b)
	}
	// usage is recorded per endpoint
	rows, _, _ := e.store.History(50, 0, "", "", "")
	seen := false
	for _, r := range rows {
		seen = seen || r.Endpoint == "/v1/images/generations"
	}
	if !seen {
		t.Errorf("image call not in usage: %+v", rows)
	}
}

// A combo member that answers 200 with nothing must hand over to the next member; the last member's answer is final.
func TestEmptyResponseFailsOverInCombo(t *testing.T) {
	e := newEnv(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		stream := strings.Contains(string(b), `"stream":true`)
		empty := strings.Contains(string(b), `"model":"bad"`)
		filtered := strings.Contains(string(b), `"model":"fine"`)
		switch {
		case stream && empty:
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
		case stream:
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\ndata: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\ndata: [DONE]\n\n")
		case empty:
			io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":""},"finish_reason":"stop"}]}`)
		case filtered:
			io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":""},"finish_reason":"content_filter"}]}`)
		default:
			io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]}`)
		}
	}, "chat")
	e.addKey("k", "good")
	e.store.CreateCombo("duo", []string{"mock/bad", "mock/good"}, "", nil)
	e.store.CreateCombo("lastempty", []string{"mock/good", "mock/bad"}, "", nil)
	e.store.CreateCombo("filtered", []string{"mock/fine", "mock/good"}, "", nil)
	body := func(model string, stream bool) (int, string) {
		resp := e.chat(`{"model":"`+model+`","stream":`+strconv.FormatBool(stream)+`,"messages":[{"role":"user","content":"x"}]}`, e.apiKey)
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp.StatusCode, string(b)
	}
	for _, stream := range []bool{false, true} {
		if code, b := body("duo", stream); code != 200 || !strings.Contains(b, "hello") {
			t.Errorf("stream=%v empty first member: %d %s", stream, code, b)
		}
	}
	// the first member keeps its answer when it is empty on purpose (content filter)
	if code, b := body("filtered", false); code != 200 || strings.Contains(b, "hello") || !strings.Contains(b, "content_filter") {
		t.Errorf("content filter was retried: %d %s", code, b)
	}
	// nobody left to fall back to: the empty answer is returned as it is
	if code, b := body("lastempty", false); code != 200 || !strings.Contains(b, "hello") {
		t.Errorf("good first member = %d %s", code, b)
	}
	if code, b := body("mock/bad", false); code != 200 || strings.Contains(b, "hello") {
		t.Errorf("single model must not be touched: %d %s", code, b)
	}
}

// Tests are rationed: a repeat within the window is answered from memory, and "Test all" leaves experimental
// providers alone (CodeBuddy banned an account for repeated rejected calls).
func TestTestsAreRationed(t *testing.T) {
	var mu sync.Mutex
	hits := 0
	e := newEnv(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		io.WriteString(w, `{"data":[{"id":"m"}]}`)
	}, "chat")
	e.store.UpdateSettings(map[string]any{"requireLogin": false})
	c := e.addKey("k", "good")
	for range 3 {
		e.json("POST", "/api/providers/"+c.ID+"/test", `{"modelId":"m"}`)
	}
	first := hits
	if first == 0 || first > 2 { // one GET /models + one probe at most, for three clicks
		t.Errorf("upstream calls for 3 tests = %d", first)
	}
	for range 3 {
		e.json("POST", "/api/models/test", `{"model":"mock/m"}`)
	}
	if hits > first+1 {
		t.Errorf("model tests not rationed: %d calls", hits-first)
	}

	cb, err := e.store.Node("codebuddy-intl")
	if err != nil {
		t.Fatal(err)
	}
	e.store.CreateConnection(cb, "cb", "tok")
	_, out := e.json("POST", "/api/providers/test-batch", `{}`)
	sum := out["summary"].(map[string]any)
	if sum["skipped"] != float64(1) || sum["total"] != float64(1) {
		t.Errorf("batch summary = %v", sum)
	}
}

func TestBulkApplyProxy(t *testing.T) {
	e := newEnv(t, func(w http.ResponseWriter, r *http.Request) {}, "chat")
	e.store.UpdateSettings(map[string]any{"requireLogin": false})
	c1, c2, c3 := e.addKey("a", "1"), e.addKey("b", "2"), e.addKey("c", "3")
	p1 := &db.ProxyPool{Name: "p1", Type: db.PoolHTTP, ProxyURL: "http://127.0.0.1:1"}
	p2 := &db.ProxyPool{Name: "p2", Type: db.PoolHTTP, ProxyURL: "http://127.0.0.1:2"}
	e.store.CreateProxyPool(p1)
	e.store.CreateProxyPool(p2)
	bound := func(id string) any {
		c, _ := e.store.Connection(id)
		return c.Data["providerSpecificData"].(map[string]any)["proxyPoolId"]
	}
	// one-to-one: connections rotate over the pools
	body := `{"assignments":[{"connectionId":"` + c1.ID + `","proxyPoolId":"` + p1.ID + `"},{"connectionId":"` + c2.ID + `","proxyPoolId":"` + p2.ID + `"},{"connectionId":"` + c3.ID + `","proxyPoolId":"` + p1.ID + `"}]}`
	if code, out := e.json("POST", "/api/providers/bulk-proxy", body); code != 200 || out["updated"] != float64(3) {
		t.Fatalf("bulk = %d %v", code, out)
	}
	if bound(c1.ID) != p1.ID || bound(c2.ID) != p2.ID || bound(c3.ID) != p1.ID {
		t.Errorf("bindings: %v %v %v", bound(c1.ID), bound(c2.ID), bound(c3.ID))
	}
	// unbind all
	e.json("POST", "/api/providers/bulk-proxy", `{"assignments":[{"connectionId":"`+c1.ID+`","proxyPoolId":""},{"connectionId":"`+c2.ID+`","proxyPoolId":""}]}`)
	if bound(c1.ID) != nil || bound(c2.ID) != nil || bound(c3.ID) != p1.ID {
		t.Errorf("unbind: %v %v %v", bound(c1.ID), bound(c2.ID), bound(c3.ID))
	}
	// all or nothing: an unknown pool or connection changes nothing
	if code, _ := e.json("POST", "/api/providers/bulk-proxy", `{"assignments":[{"connectionId":"`+c1.ID+`","proxyPoolId":"nope"}]}`); code != 400 {
		t.Errorf("unknown pool = %d", code)
	}
	if code, _ := e.json("POST", "/api/providers/bulk-proxy", `{"assignments":[{"connectionId":"`+c1.ID+`","proxyPoolId":"`+p2.ID+`"},{"connectionId":"ghost","proxyPoolId":"`+p2.ID+`"}]}`); code != 404 || bound(c1.ID) != nil {
		t.Errorf("partial apply: %d %v", code, bound(c1.ID))
	}
}

// OpenCode Free needs no key: using it must not ask for one, and a connection the user disabled stays disabled.
func TestNoAuthProviderNeedsNoKey(t *testing.T) {
	e := newEnv(t, func(w http.ResponseWriter, r *http.Request) {}, "chat")
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"content":[{"type":"text","text":"hi"}]}`)
	}))
	defer mock.Close()
	t.Cleanup(provider.Override("opencode", mock.URL)) // never talk to the real service from a test
	e.store.UpdateSettings(map[string]any{"requireLogin": false})
	node, err := e.store.Node("opencode")
	if err != nil {
		t.Fatal(err)
	}
	if conns, _ := e.store.Connections(node.ID, false); len(conns) != 0 {
		t.Fatalf("fresh install has connections: %d", len(conns))
	}
	resp := e.chat(`{"model":"oc/big-pickle","messages":[{"role":"user","content":"x"}]}`, e.apiKey)
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.Contains(string(b), "Add an API key") || strings.Contains(string(b), "No active connection") {
		t.Errorf("asked for a key: %s", b)
	}
	conns, _ := e.store.Connections(node.ID, false)
	if len(conns) != 1 || conns[0].Name != "Default" {
		t.Fatalf("connections = %+v", conns)
	}
	e.store.PatchConnection(conns[0].ID, map[string]any{"isActive": false}, nil)
	e.chat(`{"model":"oc/big-pickle","messages":[{"role":"user","content":"x"}]}`, e.apiKey).Body.Close()
	if all, _ := e.store.Connections(node.ID, false); len(all) != 1 {
		t.Errorf("a disabled connection was recreated: %d", len(all))
	}
}

// A keyless relay (XRouter-Next's) has no /health and ignores the key: it answers 400 unless x-relay-target is set.
// Test must forward a real request to it and no key may be sent.
func TestKeylessRelay(t *testing.T) {
	e := newEnv(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"data":[{"id":"m"}]}`) }, "chat")
	e.store.UpdateSettings(map[string]any{"requireLogin": false})
	var gotKey string
	old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("x-relay-key")
		if r.Header.Get("x-relay-target") == "" {
			w.WriteHeader(400)
			io.WriteString(w, `{"error":"Missing x-relay-target header"}`)
			return
		}
		io.WriteString(w, "ok")
	}))
	defer old.Close()

	_, out := e.json("POST", "/api/proxy-pools", `{"name":"old","type":"vercel","proxyUrl":"`+old.URL+`"}`)
	id := out["proxyPool"].(map[string]any)["id"].(string)
	if _, out := e.json("POST", "/api/proxy-pools/"+id+"/test", ""); out["ok"] != false {
		t.Fatalf("a keyed test of a keyless relay should fail with its 400, got %v", out)
	}
	if code, out := e.json("PATCH", "/api/proxy-pools/"+id, `{"keyless":true}`); code != 200 || out["proxyPool"].(map[string]any)["keyless"] != true {
		t.Fatalf("set keyless = %d %v", code, out)
	}
	if _, out := e.json("POST", "/api/proxy-pools/"+id+"/test", ""); out["ok"] != true {
		t.Errorf("keyless test = %v", out)
	}
	if gotKey != "" {
		t.Errorf("x-relay-key sent to a keyless relay: %q", gotKey)
	}
	if code, _ := e.json("POST", "/api/proxy-pools", `{"name":"p","type":"postcard","relayKey":"k","keyless":true,"proxyUrl":"`+old.URL+`"}`); code != 400 {
		t.Errorf("keyless postcard = %d, want 400", code)
	}
}
