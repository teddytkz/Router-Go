package gateway

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func decode(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestInjectSystemAllFormatsIdempotent(t *testing.T) {
	const p = "BE LAZY"
	cases := map[string]struct {
		body   string
		format string
		check  func(m map[string]any) string // the text that must contain the prompt exactly once
	}{
		"chat without system": {`{"messages":[{"role":"user","content":"hi"}]}`, "chat", func(m map[string]any) string { return m["messages"].([]any)[0].(map[string]any)["content"].(string) }},
		"chat with system":    {`{"messages":[{"role":"system","content":"base"},{"role":"user","content":"hi"}]}`, "chat", func(m map[string]any) string { return m["messages"].([]any)[0].(map[string]any)["content"].(string) }},
		"chat system parts": {`{"messages":[{"role":"system","content":[{"type":"text","text":"base"}]}]}`, "chat", func(m map[string]any) string {
			return contentText(m["messages"].([]any)[0].(map[string]any)["content"])
		}},
		"messages string":      {`{"system":"base","messages":[]}`, "messages", func(m map[string]any) string { return m["system"].(string) }},
		"messages none":        {`{"messages":[]}`, "messages", func(m map[string]any) string { return m["system"].(string) }},
		"messages blocks":      {`{"system":[{"type":"text","text":"base","cache_control":{"type":"ephemeral"}}],"messages":[]}`, "messages", func(m map[string]any) string { return contentText(m["system"]) }},
		"responses none":       {`{"input":"hi"}`, "responses", func(m map[string]any) string { return m["instructions"].(string) }},
		"responses with instr": {`{"instructions":"base","input":[]}`, "responses", func(m map[string]any) string { return m["instructions"].(string) }},
	}
	for name, c := range cases {
		m := decode(t, c.body)
		injectSystem(m, c.format, p)
		injectSystem(m, c.format, p) // twice: must not duplicate
		if got := c.check(m); strings.Count(got, p) != 1 {
			t.Errorf("%s: prompt count in %q = %d", name, got, strings.Count(got, p))
		}
	}
	// cache breakpoint stays last so the cached prefix keeps matching
	m := decode(t, cases["messages blocks"].body)
	injectSystem(m, "messages", p)
	blocks := m["system"].([]any)
	if blocks[0].(map[string]any)["text"] != p || blocks[1].(map[string]any)["cache_control"] == nil {
		t.Errorf("system blocks = %v", blocks)
	}
}

func TestTransformsOrderAndLevels(t *testing.T) {
	tf := transforms{ponytail: ponytailPrompt("ultra"), watermarks: true, custom: "CUSTOM"}
	m := decode(t, `{"messages":[{"role":"user","content":"hi"}]}`)
	tf.apply(m, "chat")
	sys := m["messages"].([]any)[0].(map[string]any)["content"].(string)
	i1, i2, i3 := strings.Index(sys, "YAGNI extremist"), strings.Index(sys, "Text Hygiene"), strings.Index(sys, "CUSTOM")
	if !(i1 >= 0 && i1 < i2 && i2 < i3) {
		t.Fatalf("order ponytail<watermarks<custom broken: %d %d %d", i1, i2, i3)
	}
	if ponytailPrompt("lite") == ponytailPrompt("full") || ponytailPrompt("nope") != "" {
		t.Fatal("levels must differ and unknown level must be empty")
	}
}

func TestLoopGuard(t *testing.T) {
	call := func(name, args string) string {
		return `{"role":"assistant","tool_calls":[{"id":"1","function":{"name":"` + name + `","arguments":"` + strings.ReplaceAll(args, `"`, `\"`) + `"}}]},{"role":"tool","content":"same result"}`
	}
	same := `{"messages":[{"role":"user","content":"go"},` + strings.Join([]string{call("read", `{"a":1,"b":2}`), call("read", `{"b":2,"a":1}`), call("read", `{"a":1,"b":2}`)}, ",") + `]}`
	if h := detectLoop(decode(t, same), "chat"); h != hintSingle {
		t.Errorf("identical calls (key order differs): %q", h)
	}
	seq := `{"messages":[` + strings.Join([]string{call("a", `{}`), call("b", `{}`), call("a", `{}`), call("b", `{}`)}, ",") + `]}`
	if h := detectLoop(decode(t, seq), "chat"); h != hintSequence {
		t.Errorf("A,B,A,B: %q", h)
	}
	text := `{"messages":[{"role":"assistant","content":"I need to read the key files first."},{"role":"user","content":"ok"},{"role":"assistant","content":"I need to read the key files first!"},{"role":"user","content":"ok"},{"role":"assistant","content":"i need to read the key files first"}]}`
	if h := detectLoop(decode(t, text), "chat"); h != hintTextMessage {
		t.Errorf("text loop: %q", h)
	}
	anthropic := `{"messages":[` + strings.Repeat(`{"role":"assistant","content":[{"type":"tool_use","name":"x","input":{"k":1}}]},{"role":"user","content":[{"type":"tool_result","content":"r"}]},`, 3) + `{"role":"user","content":"next"}]}`
	if h := detectLoop(decode(t, anthropic), "messages"); h != hintSingle {
		t.Errorf("anthropic tool_use loop: %q", h)
	}
	healthy := `{"messages":[` + strings.Join([]string{call("a", `{}`), call("b", `{}`), call("c", `{}`)}, ",") + `]}`
	if h := detectLoop(decode(t, healthy), "chat"); h != "" {
		t.Errorf("healthy history flagged: %q", h)
	}
	// hint lands on the last user message, once
	m := decode(t, same)
	tf := transforms{loopGuard: true}
	tf.apply(m, "chat")
	tf.apply(m, "chat")
	msgs := m["messages"].([]any)
	last := msgs[len(msgs)-1].(map[string]any)["content"].(string)
	if strings.Count(last, routerNote) != 1 || !strings.Contains(msgs[0].(map[string]any)["content"].(string), "STOP calling tools") {
		t.Errorf("hint/termination prompt missing or duplicated: last=%q first=%v", last, msgs[0])
	}
}

func TestBreakerOpensProbesAndCloses(t *testing.T) {
	b := &breaker{}
	for range breakerThreshold - 1 {
		b.record(true)
	}
	if ok, _ := b.allow(); !ok {
		t.Fatal("must stay closed below the threshold")
	}
	b.record(false) // a success clears the streak
	for range breakerThreshold {
		b.record(true)
	}
	if ok, wait := b.allow(); ok || wait <= 0 {
		t.Fatalf("must be open: ok=%v wait=%v", ok, wait)
	}
	b.openedAt = time.Now().Add(-breakerReset - time.Second) // reset timeout elapsed → one probe
	if ok, _ := b.allow(); !ok {
		t.Fatal("half-open must let a probe through")
	}
	if ok, _ := b.allow(); ok {
		t.Fatal("only one probe at a time")
	}
	b.record(true) // probe failed → open again, longer wait counter
	if ok, _ := b.allow(); ok || b.cycles != 1 {
		t.Fatalf("failed probe must re-open: cycles=%d", b.cycles)
	}
	b.openedAt = time.Now().Add(-breakerReset - time.Second)
	b.allow()
	b.record(false)
	if ok, _ := b.allow(); !ok || b.open {
		t.Fatal("successful probe must close the breaker")
	}
}

func TestLimiterCapQueueAndTimeout(t *testing.T) {
	l := &limiter{gates: map[string]*gate{}}
	ctx := context.Background()
	r1, _ := l.acquire(ctx, "a", 1)
	if l.free("a", 1) {
		t.Fatal("slot should be taken")
	}
	got := make(chan func(), 1)
	go func() { r, _ := l.acquire(ctx, "a", 1); got <- r }()
	select {
	case <-got:
		t.Fatal("second request must wait for the first")
	case <-time.After(50 * time.Millisecond):
	}
	r1()
	r1() // idempotent
	select {
	case r2 := <-got:
		r2()
	case <-time.After(time.Second):
		t.Fatal("queued request was not woken")
	}
	if len(l.gates) != 0 {
		t.Fatalf("idle gates must be cleaned up: %v", l.gates)
	}
	// a cancelled waiter leaves the queue and does not leak a slot
	r1, _ = l.acquire(ctx, "b", 1)
	cctx, cancel := context.WithCancel(ctx)
	errc := make(chan error, 1)
	go func() { _, err := l.acquire(cctx, "b", 1); errc <- err }()
	time.Sleep(30 * time.Millisecond)
	cancel()
	if err := <-errc; err == nil {
		t.Fatal("cancelled wait must error")
	}
	r1()
	if len(l.gates) != 0 {
		t.Fatal("slot leaked after cancelled waiter")
	}
}

func TestBreakerStatusForDashboard(t *testing.T) {
	g := &Gateway{breakers: map[string]*breaker{}}
	if s, _, f := g.BreakerStatus("n"); s != "CLOSED" || f != 0 {
		t.Fatalf("unknown node: %s %d", s, f)
	}
	b := g.breakerFor("n")
	for range breakerThreshold {
		b.record(true)
	}
	if s, retry, f := g.BreakerStatus("n"); s != "OPEN" || retry <= 0 || f != breakerThreshold {
		t.Fatalf("open: %s %v %d", s, retry, f)
	}
	b.openedAt = time.Now().Add(-breakerReset - time.Second)
	if s, _, _ := g.BreakerStatus("n"); s != "HALF_OPEN" {
		t.Fatalf("after the reset timeout: %s", s)
	}
}
