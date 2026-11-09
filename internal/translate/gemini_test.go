package translate

import (
	"strings"
	"testing"
)

const geminiReq = `{"model":"m","stream":true,
"systemInstruction":{"parts":[{"text":"be brief"},{"text":"in French"}]},
"generationConfig":{"maxOutputTokens":100,"temperature":0.5,"topP":0.9,"stopSequences":["END"]},
"contents":[
 {"role":"user","parts":[{"text":"hi"},{"inlineData":{"mimeType":"image/png","data":"QUJD"}}]},
 {"role":"model","parts":[{"thought":true,"text":"hmm"},{"text":"checking"},{"functionCall":{"name":"get_weather","args":{"city":"Paris"}}}]},
 {"role":"user","parts":[{"functionResponse":{"name":"get_weather","response":{"result":"sunny"}}}]},
 {"role":"user","parts":[{"text":"thanks"},{"inlineData":{"mimeType":"audio/mpeg","data":"AAAA"}}]}],
"tools":[{"functionDeclarations":[{"name":"get_weather","description":"d","parameters":{"type":"OBJECT","properties":{"city":{"type":"STRING"},"type":{"type":"STRING"}}}}]}],
"toolConfig":{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["get_weather"]}}}`

func TestRequestGeminiToChat(t *testing.T) {
	out, err := Request(Gemini, Chat, js(t, geminiReq))
	if err != nil {
		t.Fatal(err)
	}
	eq(t, out, "model", "m")
	eq(t, out, "max_tokens", 100)
	eq(t, out, "temperature", "0.5")
	eq(t, out, "top_p", "0.9")
	eq(t, out, "stop.0", "END")
	eq(t, out, "stream_options.include_usage", true)
	eq(t, out, "messages.0.role", "system")
	eq(t, out, "messages.0.content", "be brief\nin French")
	eq(t, out, "messages.1.content.0.text", "hi")
	eq(t, out, "messages.1.content.1.image_url.url", "data:image/png;base64,QUJD")
	eq(t, out, "messages.2.role", "assistant")
	eq(t, out, "messages.2.content", "checking") // the thought part is dropped
	eq(t, out, "messages.2.tool_calls.0.id", "call_get_weather")
	eq(t, out, "messages.2.tool_calls.0.function.arguments", `{"city":"Paris"}`)
	eq(t, out, "messages.3.role", "tool")
	eq(t, out, "messages.3.tool_call_id", "call_get_weather")
	eq(t, out, "messages.3.content", `{"result":"sunny"}`)
	eq(t, out, "messages.4.content.1.input_audio.format", "mp3")
	eq(t, out, "tools.0.function.parameters.type", "object") // OBJECT -> object
	eq(t, out, "tools.0.function.parameters.properties.city.type", "string")
	eq(t, out, "tools.0.function.parameters.properties.type.type", "string") // a property named "type" survives
	eq(t, out, "tool_choice.function.name", "get_weather")

	// through the hub to Anthropic
	msg, err := Request(Gemini, Messages, js(t, geminiReq))
	if err != nil {
		t.Fatal(err)
	}
	eq(t, msg, "system", "be brief\nin French")
	eq(t, msg, "messages.1.content.1.type", "tool_use")
	eq(t, msg, "tool_choice.name", "get_weather")

	if _, err := Request(Chat, Gemini, M{}); err == nil {
		t.Error("gemini is not an upstream format")
	}
	if _, err := Response(Gemini, Chat, M{}); err == nil {
		t.Error("gemini is not an upstream response format")
	}
	_, _ = Request(Gemini, Chat, M{"contents": []any{nil, 1, M{"parts": "x"}, M{"parts": []any{nil, 2}}}, "tools": 3, "systemInstruction": "s"})
}

func TestResponseToGemini(t *testing.T) {
	for from, src := range nativeResp {
		out, err := Response(from, Gemini, js(t, src))
		if err != nil {
			t.Fatal(err)
		}
		eq(t, out, "candidates.0.content.role", "model")
		eq(t, out, "candidates.0.content.parts.0.thought", true)
		eq(t, out, "candidates.0.content.parts.1.text", "Hello")
		eq(t, out, "candidates.0.content.parts.2.functionCall.name", "f")
		eq(t, out, "candidates.0.content.parts.2.functionCall.args.a", 1)
		eq(t, out, "candidates.0.finishReason", "STOP")
		eq(t, out, "usageMetadata.promptTokenCount", 10)
		eq(t, out, "usageMetadata.candidatesTokenCount", 5)
		eq(t, out, "usageMetadata.totalTokenCount", 15)
		eq(t, out, "modelVersion", "m")
	}
	out, _ := Response(Chat, Gemini, js(t, `{"choices":[{"message":{"content":"x"},"finish_reason":"length"}],"usage":{"prompt_tokens":10,"completion_tokens":1,"prompt_tokens_details":{"cached_tokens":4}}}`))
	eq(t, out, "candidates.0.finishReason", "MAX_TOKENS")
	eq(t, out, "usageMetadata.cachedContentTokenCount", 4)
	out, _ = Response(Chat, Gemini, js(t, `{"choices":[{"message":{"content":""},"finish_reason":"content_filter"}]}`))
	eq(t, out, "candidates.0.finishReason", "SAFETY")
	eq(t, out, "candidates.0.content.parts.0.text", "")
	if _, ok := out["usageMetadata"]; ok {
		t.Error("no usage upstream: no usageMetadata")
	}
}

func TestStreamToGemini(t *testing.T) {
	for from, evs := range upstream {
		s := NewStream(from, Gemini, "client-model")
		var raw strings.Builder
		for _, e := range evs {
			raw.Write(s.Feed(e.event, e.data))
		}
		if c := s.Close(); c != nil {
			t.Errorf("%s: Close after terminal event returned %q", from, c)
		}
		var text, args string
		var finish string
		var last M
		var calls int
		for _, fr := range strings.Split(strings.TrimSpace(raw.String()), "\n\n") {
			data, ok := strings.CutPrefix(fr, "data: ")
			if !ok {
				t.Fatalf("%s: bad frame %q", from, fr)
			}
			m := js(t, data)
			last = m
			c := asMap(asList(m["candidates"])[0])
			for _, p := range asList(asMap(c["content"])["parts"]) {
				pm := asMap(p)
				text += str(pm["text"])
				if fc := asMap(pm["functionCall"]); fc != nil {
					calls++
					args = jsonStr(fc["args"])
					eq(t, fc, "name", "get_weather")
				}
			}
			finish = str(c["finishReason"])
		}
		if text != "Hello world" || args != `{"city":"Paris"}` || calls != 1 || finish != "STOP" {
			t.Errorf("%s: text=%q args=%q calls=%d finish=%q", from, text, args, calls, finish)
		}
		eq(t, last, "usageMetadata.promptTokenCount", 10)
		eq(t, last, "usageMetadata.candidatesTokenCount", 5)
		eq(t, last, "modelVersion", "client-model")
	}
	// a stream cut short still ends with one finishing chunk
	s := NewStream(Chat, Gemini, "m")
	out := string(s.Feed("", chatChunk(`{"reasoning_content":"hmm"}`, ""))) + string(s.Feed("", chatChunk(`{"content":"ok"}`, ""))) + string(s.Close())
	if strings.Count(out, `"finishReason":"STOP"`) != 1 || !strings.Contains(out, `"thought":true`) || strings.Contains(out, "[DONE]") {
		t.Errorf("close: %s", out)
	}
}
