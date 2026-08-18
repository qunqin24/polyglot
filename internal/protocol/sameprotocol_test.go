package protocol_test

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/qunqin24/polyglot/internal/canonical"
	"github.com/qunqin24/polyglot/internal/protocol"
)

// The same-protocol guarantee.
//
// A request that arrives in one protocol and leaves in that same protocol goes
// through canonical like every other one — Polyglot has no passthrough mode
// and must not grow one. That makes it the sharpest test of the canonical
// model there is: every difference between what came in and what went out is
// something canonical could not hold, with no protocol mismatch to excuse it.
//
// It also catches a whole class of bug the cross-protocol matrix cannot see.
// protocol.Capture only reaches top-level fields, so a vendor field nested
// inside a known member has no route home; it is re-encoded from canonical or
// not at all. That is how Anthropic's cache_control used to vanish — three
// levels down inside messages[].content[], silently switching off the prompt
// caching the caller was paying for while the request still succeeded.
//
// Every entry in `allowed` below is a deliberate normalisation with a reason.
// A new difference is a bug until someone writes down why it is not.
func TestSameProtocolRoundTrip(t *testing.T) {
	for _, tc := range sameProtocolCases() {
		t.Run(string(tc.proto), func(t *testing.T) {
			codec := protocol.MustGet(tc.proto)
			d := canonical.NewDiagnostics()

			req, err := codec.DecodeRequest([]byte(tc.body), d)
			if err != nil {
				t.Fatalf("DecodeRequest: %v", err)
			}
			// Gemini carries the model in the URL, so the gateway sets it.
			if req.Model == "" {
				req.Model = "m"
			}
			out, err := codec.EncodeRequest(req, d)
			if err != nil {
				t.Fatalf("EncodeRequest: %v", err)
			}

			var before, after any
			if err := json.Unmarshal([]byte(tc.body), &before); err != nil {
				t.Fatalf("fixture is not valid JSON: %v", err)
			}
			if err := json.Unmarshal(out, &after); err != nil {
				t.Fatalf("encoded body is not valid JSON: %v", err)
			}

			var found []string
			jsonDiff("", before, after, &found)
			for _, got := range found {
				if !allowedDiff(tc.allowed, got) {
					t.Errorf("same-protocol round trip changed the request:\n  %s\n\nout: %s", got, out)
				}
			}
		})
	}
}

type sameProtocolCase struct {
	proto protocol.Name
	body  string
	// allowed lists path prefixes that may differ, each with the reason it is
	// a normalisation rather than a loss.
	allowed map[string]string
}

func sameProtocolCases() []sameProtocolCase {
	return []sameProtocolCase{{
		proto: protocol.OpenAI,
		body: `{
		  "model":"m","stream":false,
		  "messages":[
		    {"role":"system","content":"sys"},
		    {"role":"user","name":"alice","content":[{"type":"text","text":"hi"}]},
		    {"role":"assistant","content":null,"tool_calls":[
		       {"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}]},
		    {"role":"tool","tool_call_id":"c1","content":"ok"}
		  ],
		  "temperature":0.5,"top_p":0.9,"max_tokens":100,"stop":["x"],
		  "tools":[{"type":"function","function":{"name":"f","description":"d",
		            "parameters":{"type":"object","properties":{}},"strict":true}}],
		  "tool_choice":"auto","parallel_tool_calls":false,
		  "response_format":{"type":"json_object"},
		  "seed":7,"user":"u1","logit_bias":{"5":1},"n":1,
		  "provider":{"order":["a"]},"guided_json":{"x":1}
		}`,
		allowed: map[string]string{
			"req.max_tokens":          "re-emitted as max_completion_tokens, OpenAI's current name for it",
			"req.stream":              "false is the default and is omitted",
			"req.messages[1].content": "a lone text part is written as the equivalent bare string",
			"req.messages[2].content": "null content on a tool-call turn is omitted, which means the same",
		},
	}, {
		proto: protocol.Anthropic,
		body: `{
		  "model":"m","max_tokens":100,
		  "system":[{"type":"text","text":"sys","cache_control":{"type":"ephemeral"}}],
		  "messages":[
		    {"role":"user","content":[{"type":"text","text":"hi",
		      "cache_control":{"type":"ephemeral","ttl":"1h"}}]},
		    {"role":"assistant","content":[
		      {"type":"thinking","thinking":"t","signature":"sig"},
		      {"type":"tool_use","id":"c1","name":"f","input":{}}]},
		    {"role":"user","content":[{"type":"tool_result","tool_use_id":"c1","content":"ok"}]}
		  ],
		  "temperature":0.5,"top_k":40,"stop_sequences":["x"],
		  "tools":[{"name":"f","description":"d","input_schema":{"type":"object"},
		            "cache_control":{"type":"ephemeral"}}],
		  "tool_choice":{"type":"auto"},
		  "thinking":{"type":"enabled","budget_tokens":1024},
		  "metadata":{"user_id":"u1"},"service_tier":"auto"
		}`,
		allowed: map[string]string{
			"req.max_tokens": "raised above the thinking budget, which Anthropic requires; noted as semantic",
		},
	}, {
		proto: protocol.Gemini,
		body: `{
		  "contents":[
		    {"role":"user","parts":[{"text":"hi"}]},
		    {"role":"model","parts":[{"functionCall":{"name":"f","args":{}},
		      "thoughtSignature":"sig"}]},
		    {"role":"user","parts":[{"functionResponse":{"name":"f","response":{"ok":true,"n":2}}}]}
		  ],
		  "systemInstruction":{"parts":[{"text":"sys"}]},
		  "tools":[{"functionDeclarations":[{"name":"f","description":"d",
		            "parameters":{"type":"object"}}]},{"googleSearch":{}}],
		  "toolConfig":{"functionCallingConfig":{"mode":"AUTO"}},
		  "generationConfig":{"temperature":0.5,"topP":0.9,"topK":40,
		    "maxOutputTokens":100,"stopSequences":["x"],"seed":7,
		    "thinkingConfig":{"thinkingBudget":1024,"includeThoughts":true}},
		  "safetySettings":[{"category":"HARM_CATEGORY_HATE_SPEECH","threshold":"BLOCK_NONE"}],
		  "cachedContent":"cachedContents/abc"
		}`,
		allowed: map[string]string{},
	}, {
		proto: protocol.OpenAIResponses,
		body: `{
		  "model":"m",
		  "input":[
		    {"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]},
		    {"type":"function_call","call_id":"c1","name":"f","arguments":"{}"},
		    {"type":"function_call_output","call_id":"c1","output":"ok"}
		  ],
		  "instructions":"sys","temperature":0.5,"top_p":0.9,"max_output_tokens":100,
		  "tools":[{"type":"function","name":"f","description":"d",
		            "parameters":{"type":"object"},"strict":true}],
		  "tool_choice":"auto","parallel_tool_calls":false,
		  "reasoning":{"effort":"high","summary":"auto"},
		  "text":{"format":{"type":"json_object"}},
		  "store":false,"truncation":"auto","metadata":{"k":"v"}
		}`,
		allowed: map[string]string{},
	}, {
		proto: protocol.GeminiInteractions,
		body: `{
		  "model":"m",
		  "input":[
		    {"type":"user_input","content":[{"type":"text","text":"hi"}]},
		    {"type":"thought","summary":[{"type":"text","text":"t"}],"signature":"sig"},
		    {"type":"function_call","id":"c1","name":"f","arguments":{},"signature":"sig2"},
		    {"type":"function_result","call_id":"c1","result":{"ok":true}}
		  ],
		  "system_instruction":"sys",
		  "tools":[{"type":"function","name":"f","description":"d",
		            "parameters":{"type":"object"}},{"type":"google_search"}],
		  "generation_config":{"temperature":0.5,"top_p":0.9,
		    "max_output_tokens":100,"thinking_level":"high","stop_sequences":["x"]},
		  "response_format":[{"type":"text","mime_type":"application/json"}],
		  "safety_settings":[{"category":"X","threshold":"Y"}]
		}`,
		allowed: map[string]string{},
	}}
}

func allowedDiff(allowed map[string]string, got string) bool {
	path, _, _ := strings.Cut(got, ":")
	for prefix := range allowed {
		if path == prefix || strings.HasPrefix(path, prefix+".") ||
			strings.HasPrefix(path, prefix+"[") {
			return true
		}
	}
	return false
}

// jsonDiff reports every leaf in a that is missing from or changed in b. It
// only walks a: a field the encoder adds (store:false, a default) is not a
// loss and is not reported.
func jsonDiff(path string, a, b any, out *[]string) {
	if path == "" {
		path = "req"
	}
	switch av := a.(type) {
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok {
			*out = append(*out, fmt.Sprintf("%s: object became %T", path, b))
			return
		}
		keys := make([]string, 0, len(av))
		for k := range av {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			sub, ok := bv[k]
			if !ok {
				*out = append(*out, fmt.Sprintf("%s.%s: dropped", path, k))
				continue
			}
			jsonDiff(path+"."+k, av[k], sub, out)
		}
	case []any:
		bv, ok := b.([]any)
		if !ok {
			*out = append(*out, fmt.Sprintf("%s: array became %T", path, b))
			return
		}
		if len(av) != len(bv) {
			*out = append(*out, fmt.Sprintf("%s: %d entries became %d", path, len(av), len(bv)))
			return
		}
		for i := range av {
			jsonDiff(fmt.Sprintf("%s[%d]", path, i), av[i], bv[i], out)
		}
	default:
		aj, _ := json.Marshal(a)
		bj, _ := json.Marshal(b)
		if string(aj) != string(bj) {
			*out = append(*out, fmt.Sprintf("%s: %s became %s", path, aj, bj))
		}
	}
}
