package translate

import (
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func js(t *testing.T, s string) M {
	t.Helper()
	m := decode(s)
	if m == nil {
		t.Fatalf("bad json: %s", s)
	}
	return m
}

func get(t *testing.T, v any, path string) any {
	t.Helper()
	for _, k := range strings.Split(path, ".") {
		switch c := v.(type) {
		case map[string]any:
			v = c[k]
		case []any:
			i := toInt(json.Number(k))
			if i >= len(c) {
				t.Fatalf("path %s: index %s out of range", path, k)
			}
			v = c[i]
		default:
			t.Fatalf("path %s: cannot descend at %s (%T)", path, k, v)
		}
	}
	return v
}

func eq(t *testing.T, v any, path string, want any) {
	t.Helper()
	got := get(t, v, path)
	switch n := got.(type) {
	case json.Number:
		got = n.String()
	case int:
		got = strconv.Itoa(n)
	}
	if n, ok := want.(int); ok {
		want = strconv.Itoa(n)
	}
	if !reflect.DeepEqual(got, want) {
		b, _ := json.Marshal(v)
		t.Errorf("%s = %#v, want %#v\nin %s", path, got, want, b)
	}
}

const chatReq = `{"model":"m","stream":true,"max_tokens":100,"temperature":0.5,"stop":"END","user":"u1",
"messages":[
 {"role":"system","content":"be brief"},
 {"role":"user","content":[{"type":"text","text":"hi"},{"type":"image_url","image_url":{"url":"data:image/png;base64,QUJD"}}]},
 {"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Paris\"}"}}]},
 {"role":"tool","tool_call_id":"call_1","content":"sunny"},
 {"role":"user","content":"thanks"}],
"tools":[{"type":"function","function":{"name":"get_weather","description":"d","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}}],
"tool_choice":{"type":"function","function":{"name":"get_weather"}}}`

func TestRequestChatToMessages(t *testing.T) {
	out, err := Request(Chat, Messages, js(t, chatReq))
	if err != nil {
		t.Fatal(err)
	}
	eq(t, out, "system", "be brief")
	eq(t, out, "max_tokens", "100")
	eq(t, out, "stop_sequences.0", "END")
	eq(t, out, "metadata.user_id", "u1")
	eq(t, out, "messages.0.content.1.source.media_type", "image/png")
	eq(t, out, "messages.0.content.1.source.data", "QUJD")
	eq(t, out, "messages.1.content.0.type", "tool_use")
	eq(t, out, "messages.1.content.0.input.city", "Paris")
	eq(t, out, "messages.2.role", "user") // tool_result + following user text merged
	eq(t, out, "messages.2.content.0.type", "tool_result")
	eq(t, out, "messages.2.content.0.tool_use_id", "call_1")
	eq(t, out, "messages.2.content.1.text", "thanks")
	eq(t, out, "tools.0.input_schema.type", "object")
	eq(t, out, "tool_choice.type", "tool")
	eq(t, out, "tool_choice.name", "get_weather")

	// default max_tokens
	out, _ = Request(Chat, Messages, js(t, `{"model":"m","messages":[{"role":"user","content":"x"}]}`))
	eq(t, out, "max_tokens", "4096")
}

func TestRequestChatToResponses(t *testing.T) {
	out, _ := Request(Chat, Responses, js(t, chatReq))
	eq(t, out, "instructions", "be brief")
	eq(t, out, "max_output_tokens", "100")
	eq(t, out, "input.0.content.1.type", "input_image")
	eq(t, out, "input.0.content.1.image_url", "data:image/png;base64,QUJD")
	eq(t, out, "input.1.type", "function_call")
	eq(t, out, "input.1.call_id", "call_1")
	eq(t, out, "input.2.type", "function_call_output")
	eq(t, out, "input.2.output", "sunny")
	eq(t, out, "tools.0.name", "get_weather")
	eq(t, out, "tool_choice.name", "get_weather")
	if _, ok := out["stop"]; ok {
		t.Error("stop must be dropped")
	}
}

// roundTrip checks chat -> X -> chat keeps the conversation structure.
func TestRequestRoundTrip(t *testing.T) {
	for _, mid := range []Format{Messages, Responses} {
		in := js(t, chatReq)
		x, _ := Request(Chat, mid, in)
		back, err := Request(mid, Chat, x)
		if err != nil {
			t.Fatal(err)
		}
		eq(t, back, "stream_options.include_usage", true)
		eq(t, back, "messages.0.role", "system")
		eq(t, back, "messages.0.content", "be brief")
		eq(t, back, "messages.1.content.1.image_url.url", "data:image/png;base64,QUJD")
		eq(t, back, "messages.2.tool_calls.0.id", "call_1")
		eq(t, back, "messages.2.tool_calls.0.function.name", "get_weather")
		args := decode(get(t, back, "messages.2.tool_calls.0.function.arguments").(string))
		eq(t, args, "city", "Paris")
		eq(t, back, "messages.3.role", "tool")
		eq(t, back, "messages.3.tool_call_id", "call_1")
		eq(t, back, "messages.3.content", "sunny")
		eq(t, back, "messages.4.content", "thanks")
		eq(t, back, "tools.0.function.name", "get_weather")
		eq(t, back, "tool_choice.function.name", "get_weather")
		eq(t, back, "max_tokens", "100")
		if in["messages"].([]any)[1].(M)["content"].([]any)[1].(M)["image_url"].(M)["url"] != "data:image/png;base64,QUJD" {
			t.Error("input mutated")
		}
	}
}

func TestRequestNativeToChat(t *testing.T) {
	out, _ := Request(Messages, Chat, js(t, `{"model":"c","max_tokens":9,"system":[{"type":"text","text":"s1"},{"type":"text","text":"s2"}],
	"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"text","text":"ok"}]},{"type":"text","text":"go"}]}],
	"tools":[{"name":"f","input_schema":{"type":"object"}}],"tool_choice":{"type":"any"}}`))
	eq(t, out, "messages.0.content", "s1\ns2")
	eq(t, out, "messages.1.role", "tool")
	eq(t, out, "messages.1.content", "ok")
	eq(t, out, "messages.2.content", "go")
	eq(t, out, "tool_choice", "required")
	eq(t, out, "tools.0.function.parameters.type", "object")

	out, _ = Request(Responses, Chat, js(t, `{"model":"r","instructions":"sys","max_output_tokens":7,"input":[
	{"role":"developer","content":"dev"},{"role":"user","content":[{"type":"input_text","text":"q"}]},
	{"type":"function_call","call_id":"c1","name":"f","arguments":"{}"},{"type":"function_call_output","call_id":"c1","output":"done"}],
	"tools":[{"type":"function","name":"f","parameters":{"type":"object"}},{"type":"web_search"}],"tool_choice":"auto"}`))
	eq(t, out, "messages.1.role", "system")
	eq(t, out, "messages.2.content", "q")
	eq(t, out, "messages.3.tool_calls.0.id", "c1")
	eq(t, out, "messages.4.role", "tool")
	eq(t, out, "max_tokens", "7")
	eq(t, out, "tools.0.function.name", "f")
	if n := len(out["tools"].([]any)); n != 1 {
		t.Errorf("tools = %d", n)
	}

	// responses -> messages through the hub, string input
	out, _ = Request(Responses, Messages, js(t, `{"model":"r","input":"hello","instructions":"i"}`))
	eq(t, out, "system", "i")
	eq(t, out, "messages.0.content.0.text", "hello")
	eq(t, out, "max_tokens", "4096")
	// messages -> responses
	out, _ = Request(Messages, Responses, js(t, `{"model":"m","max_tokens":5,"system":"i","messages":[{"role":"user","content":"hello"}]}`))
	eq(t, out, "instructions", "i")
	eq(t, out, "input.0.content.0.text", "hello")
	eq(t, out, "max_output_tokens", "5")
}

func TestRequestErrors(t *testing.T) {
	if _, err := Request(Chat, Messages, nil); err == nil {
		t.Error("nil body must fail")
	}
	if _, err := Request("x", Chat, M{}); err == nil {
		t.Error("unknown format must fail")
	}
	// malformed shapes must not panic
	for _, f := range []Format{Chat, Messages, Responses} {
		for _, g := range []Format{Chat, Messages, Responses} {
			_, _ = Request(f, g, M{"messages": "x", "input": 5, "tools": []any{1, "a"}, "system": 3})
			_, _ = Request(f, g, M{"messages": []any{nil, 1, M{"content": []any{nil, 2}}}, "input": []any{nil, M{"type": "message"}}})
			_, _ = Response(f, g, M{"choices": "x", "content": 1, "output": []any{nil}})
		}
	}
}

var nativeResp = map[Format]string{
	Chat:      `{"id":"chatcmpl-1","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"Hello","reasoning_content":"think","tool_calls":[{"id":"call_1","type":"function","function":{"name":"f","arguments":"{\"a\":1}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`,
	Messages:  `{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[{"type":"thinking","thinking":"think","signature":"s"},{"type":"text","text":"Hello"},{"type":"tool_use","id":"call_1","name":"f","input":{"a":1}}],"stop_reason":"tool_use","usage":{"input_tokens":10,"output_tokens":5}}`,
	Responses: `{"id":"resp_1","object":"response","status":"completed","model":"m","output":[{"id":"rs_1","type":"reasoning","summary":[{"type":"summary_text","text":"think"}]},{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"Hello"}]},{"id":"fc_1","type":"function_call","call_id":"call_1","name":"f","arguments":"{\"a\":1}"}],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}`,
}

func TestResponsePairs(t *testing.T) {
	want := result{text: "Hello", reasoning: "think", finish: "tool_calls", calls: []call{{id: "call_1", name: "f", args: `{"a":1}`}}, u: usage{set: true, prompt: 10, completion: 5}}
	for from, src := range nativeResp {
		for to := range nativeResp {
			t.Run(string(from)+"->"+string(to), func(t *testing.T) {
				out, err := Response(from, to, js(t, src))
				if err != nil {
					t.Fatal(err)
				}
				got := parsers[to](out)
				got.id, got.model = "", ""
				if !reflect.DeepEqual(got, want) {
					b, _ := json.Marshal(out)
					t.Errorf("got %+v\nwant %+v\nbody %s", got, want, b)
				}
			})
		}
	}
	out, _ := Response(Messages, Chat, js(t, nativeResp[Messages]))
	eq(t, out, "object", "chat.completion")
	eq(t, out, "choices.0.finish_reason", "tool_calls")
	eq(t, out, "usage.total_tokens", 15)
	// length/cache mapping
	out, _ = Response(Chat, Messages, js(t, `{"choices":[{"message":{"content":"x"},"finish_reason":"length"}],"usage":{"prompt_tokens":10,"completion_tokens":1,"prompt_tokens_details":{"cached_tokens":4}}}`))
	eq(t, out, "stop_reason", "max_tokens")
	eq(t, out, "usage.input_tokens", 6)
	eq(t, out, "usage.cache_read_input_tokens", 4)
	out, _ = Response(Chat, Responses, js(t, `{"choices":[{"message":{"content":"x"},"finish_reason":"length"}]}`))
	eq(t, out, "status", "incomplete")
	eq(t, out, "incomplete_details.reason", "max_output_tokens")
	if !strings.HasPrefix(str(out["id"]), "resp_") {
		t.Errorf("id %v", out["id"])
	}
}

func TestErrorBody(t *testing.T) {
	eq(t, ErrorBody(Chat, 429, "slow"), "error.type", "rate_limit_error")
	eq(t, ErrorBody(Responses, 503, "x"), "error.type", "server_error")
	eq(t, ErrorBody(Messages, 503, "x"), "error.type", "api_error")
	eq(t, ErrorBody(Messages, 401, "x"), "type", "error")
	eq(t, ErrorBody(Messages, 400, "bad"), "error.type", "invalid_request_error")
}

// ---- streams ----

type sse struct{ event, data string }

var chatChunk = func(delta, extra string) string {
	return `{"id":"chatcmpl-1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":` + delta + extra + `}]}`
}

var upstream = map[Format][]sse{
	Chat: {
		{"", chatChunk(`{"role":"assistant","content":""}`, "")},
		{"", chatChunk(`{"content":"Hello"}`, "")},
		{"", chatChunk(`{"content":" world"}`, "")},
		{"", chatChunk(`{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"get_weather","arguments":""}}]}`, "")},
		{"", chatChunk(`{"tool_calls":[{"index":0,"function":{"arguments":"{\"city\":"}}]}`, "")},
		{"", chatChunk(`{"tool_calls":[{"index":0,"function":{"arguments":"\"Paris\"}"}}]}`, "")},
		{"", chatChunk(`{}`, `,"finish_reason":"tool_calls"`)},
		{"", `{"id":"chatcmpl-1","model":"m","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`},
		{"", "[DONE]"},
	},
	Messages: {
		{"message_start", `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[],"usage":{"input_tokens":10,"output_tokens":1}}}`},
		{"ping", `{"type":"ping"}`},
		{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`},
		{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`},
		{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" world"}}`},
		{"content_block_stop", `{"type":"content_block_stop","index":0}`},
		{"content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{}}}`},
		{"content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"city\":"}}`},
		{"content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\"Paris\"}"}}`},
		{"content_block_stop", `{"type":"content_block_stop","index":1}`},
		{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":5}}`},
		{"message_stop", `{"type":"message_stop"}`},
	},
	Responses: {
		{"response.created", `{"type":"response.created","response":{"id":"resp_1","model":"m","status":"in_progress","output":[]}}`},
		{"response.in_progress", `{"type":"response.in_progress","response":{"id":"resp_1"}}`},
		{"response.output_item.added", `{"type":"response.output_item.added","output_index":0,"item":{"id":"msg_1","type":"message","content":[]}}`},
		{"response.content_part.added", `{"type":"response.content_part.added","output_index":0,"part":{"type":"output_text","text":""}}`},
		{"response.output_text.delta", `{"type":"response.output_text.delta","output_index":0,"delta":"Hello"}`},
		{"response.output_text.delta", `{"type":"response.output_text.delta","output_index":0,"delta":" world"}`},
		{"response.output_text.done", `{"type":"response.output_text.done","output_index":0,"text":"Hello world"}`},
		{"response.output_item.done", `{"type":"response.output_item.done","output_index":0,"item":{"type":"message"}}`},
		{"response.output_item.added", `{"type":"response.output_item.added","output_index":1,"item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"get_weather","arguments":""}}`},
		{"response.function_call_arguments.delta", `{"type":"response.function_call_arguments.delta","output_index":1,"delta":"{\"city\":"}`},
		{"response.function_call_arguments.delta", `{"type":"response.function_call_arguments.delta","output_index":1,"delta":"\"Paris\"}"}`},
		{"response.function_call_arguments.done", `{"type":"response.function_call_arguments.done","output_index":1,"arguments":"{\"city\":\"Paris\"}"}`},
		{"response.output_item.done", `{"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","arguments":"{\"city\":\"Paris\"}"}}`},
		{"response.completed", `{"type":"response.completed","response":{"id":"resp_1","status":"completed","usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}}`},
	},
}

// seen is what a client reconstructs from the produced frames.
type seen struct {
	events           []string
	text, args       string
	toolID, toolName string
	in, out          int
	finish           string
	terminal         int
	respOutput       int // number of items in the final response object
}

func parseFrames(t *testing.T, to Format, raw string) seen {
	t.Helper()
	var s seen
	for _, fr := range strings.Split(strings.TrimSuffix(raw, "\n\n"), "\n\n") {
		if fr == "" {
			continue
		}
		var ev, data string
		for _, l := range strings.Split(fr, "\n") {
			if v, ok := strings.CutPrefix(l, "event: "); ok {
				ev = v
			} else if v, ok := strings.CutPrefix(l, "data: "); ok {
				data = v
			} else {
				t.Fatalf("bad frame line %q", l)
			}
		}
		if to == Chat {
			if ev != "" {
				t.Fatalf("chat frame has event line: %q", fr)
			}
			if data == "[DONE]" {
				s.terminal++
				s.events = append(s.events, "[DONE]")
				continue
			}
		} else if ev == "" {
			t.Fatalf("missing event line: %q", fr)
		}
		m := js(t, data)
		if to != Chat && str(m["type"]) != ev {
			t.Fatalf("event %q != type %q", ev, m["type"])
		}
		s.events = append(s.events, ev)
		switch to {
		case Chat:
			if u := asMap(m["usage"]); u != nil {
				s.in, s.out = toInt(u["prompt_tokens"]), toInt(u["completion_tokens"])
			}
			ch := firstChoice(m)
			d := asMap(ch["delta"])
			s.text += str(d["content"])
			for _, tc := range asList(d["tool_calls"]) {
				tm := asMap(tc)
				fn := asMap(tm["function"])
				if id := str(tm["id"]); id != "" {
					s.toolID, s.toolName = id, str(fn["name"])
				}
				s.args += str(fn["arguments"])
			}
			if f := str(ch["finish_reason"]); f != "" {
				s.finish = f
			}
		case Messages:
			switch ev {
			case "content_block_start":
				if cb := asMap(m["content_block"]); str(cb["type"]) == "tool_use" {
					s.toolID, s.toolName = str(cb["id"]), str(cb["name"])
				}
			case "content_block_delta":
				d := asMap(m["delta"])
				s.text += str(d["text"])
				s.args += str(d["partial_json"])
			case "message_delta":
				s.finish = str(asMap(m["delta"])["stop_reason"])
				s.in += toInt(asMap(m["usage"])["input_tokens"])
				s.out = toInt(asMap(m["usage"])["output_tokens"])
			case "message_start":
				s.in = toInt(asMap(asMap(m["message"])["usage"])["input_tokens"])
			case "message_stop":
				s.terminal++
			}
		case Responses:
			switch ev {
			case "response.output_text.delta":
				s.text += str(m["delta"])
			case "response.function_call_arguments.delta":
				s.args += str(m["delta"])
			case "response.output_item.added":
				if it := asMap(m["item"]); str(it["type"]) == "function_call" {
					s.toolID, s.toolName = str(it["call_id"]), str(it["name"])
				}
			case "response.completed", "response.incomplete":
				s.terminal++
				r := asMap(m["response"])
				s.in, s.out = toInt(asMap(r["usage"])["input_tokens"]), toInt(asMap(r["usage"])["output_tokens"])
				s.finish = str(r["status"])
				s.respOutput = len(asList(r["output"]))
			}
		}
	}
	return s
}

func TestStreamPairs(t *testing.T) {
	for from, evs := range upstream {
		for to := range upstream {
			if from == to {
				continue
			}
			t.Run(string(from)+"->"+string(to), func(t *testing.T) {
				s := NewStream(from, to, "client-model")
				var raw strings.Builder
				for _, e := range evs {
					raw.Write(s.Feed(e.event, e.data))
				}
				raw.Write(s.Feed("", "garbage")) // ignored, and after the terminal event
				if c := s.Close(); c != nil {
					t.Errorf("Close after terminal event returned %q", c)
				}
				g := parseFrames(t, to, raw.String())
				if g.text != "Hello world" || g.args != `{"city":"Paris"}` || g.toolName != "get_weather" || g.toolID == "" {
					t.Errorf("content: %+v", g)
				}
				if g.in != 10 || g.out != 5 || g.terminal != 1 {
					t.Errorf("usage/terminal: %+v", g)
				}
				if p, c := s.Usage(); p != 10 || c != 5 {
					t.Errorf("Usage() = %d,%d", p, c)
				}
				want := map[Format]string{Chat: "tool_calls", Messages: "tool_use", Responses: "completed"}[to]
				if g.finish != want {
					t.Errorf("finish %q want %q", g.finish, want)
				}
				last := g.events[len(g.events)-1]
				if last != map[Format]string{Chat: "[DONE]", Messages: "message_stop", Responses: "response.completed"}[to] {
					t.Errorf("last event %s (%v)", last, g.events)
				}
				if to == Responses && g.respOutput != 2 {
					t.Errorf("final output items = %d", g.respOutput)
				}
				if to == Messages {
					checkMsgSequence(t, g.events)
				}
				if to == Responses {
					checkRespSequence(t, g.events)
				}
			})
		}
	}
}

func checkMsgSequence(t *testing.T, ev []string) {
	want := []string{"message_start", "content_block_start", "content_block_delta", "content_block_delta", "content_block_stop",
		"content_block_start", "content_block_delta", "content_block_delta", "content_block_stop", "message_delta", "message_stop"}
	if !reflect.DeepEqual(ev, want) {
		t.Errorf("messages sequence %v", ev)
	}
}

func checkRespSequence(t *testing.T, ev []string) {
	want := []string{"response.created", "response.in_progress",
		"response.output_item.added", "response.content_part.added", "response.output_text.delta", "response.output_text.delta",
		"response.output_text.done", "response.content_part.done", "response.output_item.done",
		"response.output_item.added", "response.function_call_arguments.delta", "response.function_call_arguments.delta",
		"response.function_call_arguments.done", "response.output_item.done", "response.completed"}
	if !reflect.DeepEqual(ev, want) {
		t.Errorf("responses sequence %v", ev)
	}
}

func TestStreamCloseWithoutTerminal(t *testing.T) {
	for _, to := range []Format{Chat, Messages, Responses} {
		from := Messages
		if to == Messages {
			from = Chat
		}
		s := NewStream(from, to, "m")
		var raw string
		for _, e := range upstream[from][:5] { // truncated mid-text
			raw += string(s.Feed(e.event, e.data))
		}
		raw += string(s.Close())
		if s.Close() != nil || s.Feed("", "[DONE]") != nil {
			t.Errorf("%s: second Close/Feed must emit nothing", to)
		}
		g := parseFrames(t, to, raw)
		if g.terminal != 1 || !strings.HasPrefix(g.text, "Hello") {
			t.Errorf("%s: %+v", to, g)
		}
	}
	// empty stream still yields a valid envelope
	if g := parseFrames(t, Messages, string(NewStream(Chat, Messages, "m").Close())); g.terminal != 1 {
		t.Errorf("empty: %+v", g)
	}
}

func TestStreamPassthrough(t *testing.T) {
	s := NewStream(Messages, Messages, "m")
	var raw strings.Builder
	for _, e := range upstream[Messages] {
		raw.Write(s.Feed(e.event, e.data))
	}
	if !strings.Contains(raw.String(), "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n") || s.Close() != nil {
		t.Errorf("passthrough: %q", raw.String())
	}
	if p, c := s.Usage(); p != 10 || c != 5 {
		t.Errorf("usage %d %d", p, c)
	}
	c := NewStream(Chat, Chat, "m")
	if got := string(c.Feed("", "[DONE]")); got != "data: [DONE]\n\n" || c.Close() != nil {
		t.Errorf("chat passthrough %q", got)
	}
}

func TestStreamReasoning(t *testing.T) {
	s := NewStream(Chat, Messages, "m")
	out := string(s.Feed("", chatChunk(`{"reasoning_content":"hmm"}`, ""))) + string(s.Feed("", chatChunk(`{"content":"ok"}`, ""))) + string(s.Close())
	for _, w := range []string{`"type":"thinking"`, `"thinking_delta"`, `"type":"text"`, `"text_delta"`} {
		if !strings.Contains(out, w) {
			t.Errorf("missing %s in %s", w, out)
		}
	}
	s = NewStream(Chat, Responses, "m")
	out = string(s.Feed("", chatChunk(`{"reasoning_content":"hmm"}`, ""))) + string(s.Close())
	if !strings.Contains(out, "response.reasoning_summary_text.delta") || !strings.Contains(out, `"type":"reasoning"`) {
		t.Errorf("responses reasoning: %s", out)
	}
}
