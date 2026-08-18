package canonical

import (
	"encoding/json"
	"sort"
	"strings"
)

// Extensions carries the fields a codec did not recognise.
//
// Protocols are not just the fields Polyglot models. An OpenAI-compatible
// upstream is a family: OpenRouter takes `provider` and `transforms`, vLLM
// takes `guided_json` and `top_k`, DeepSeek takes a `prefix` for completion,
// Groq takes `service_tier`. None of those have a canonical equivalent, and a
// hub that only carries what it can name would drop every one of them —
// silently, because `encoding/json` discards unknown members without a word.
//
// So the decoder keeps them and the encoder puts them back, but **only when
// the request leaves through the protocol it arrived on**. Handing OpenAI's
// `guided_json` to Gemini would at best be ignored and at worst be a 400: the
// field is meaningful to a dialect, not to the world. Cross-protocol they are
// reported as unsupported, which is the outcome the fidelity rules already
// demand and which today does not happen at all.
//
// This is deliberately not a passthrough mode. There is still one code path:
// every request is decoded to canonical and encoded from it, the Inspector
// still has a canonical form to show, and token usage, TTFT and TPS are still
// measured. The only thing that changes is that unknown fields ride along
// instead of falling on the floor.
type Extensions struct {
	// Protocol is the wire format these fields were read from. An encoder
	// re-emits them only if it speaks the same one.
	Protocol string `json:"protocol"`
	// Items are the unrecognised members, in a stable order.
	Items []Extension `json:"items"`
	// Truncated reports that a pathological body carried more unknown fields
	// than MaxExtensions and the rest were discarded.
	Truncated bool `json:"truncated,omitempty"`
}

// Extension is one unrecognised member of a request or response body.
type Extension struct {
	// Path is the object that held it: "" for the top level, or a member name
	// such as "generationConfig" for a nested one. Only one level of nesting
	// is captured, which is all these four protocols use for parameters.
	Path string `json:"path,omitempty"`
	// Name is the member name within Path.
	Name string `json:"name"`
	// Value is the member's raw JSON, untouched.
	Value json.RawMessage `json:"value"`
}

// MaxExtensions bounds how many unknown members one body may carry. The body
// size limit already bounds the bytes; this bounds the count, so a request
// built to allocate is answered with a note rather than with memory.
const MaxExtensions = 64

// NativeTools are tools the provider runs itself: Gemini's googleSearch and
// codeExecution, Anthropic's web_search and computer, OpenAI's file_search.
//
// They are not function tools, so there is nothing for Polyglot to relay — the
// model calls them inside the provider and the caller never sees a round trip.
// That also means they cannot be translated: Gemini's `{"googleSearch":{}}`
// and OpenAI's `{"type":"web_search"}` are similar in spirit and nothing alike
// on the wire, and inventing a mapping would be guessing on the user's behalf.
//
// So they follow the same rule as Extensions: replayed verbatim when the
// request leaves through the protocol it arrived on, reported as unsupported
// when it does not. Before this they were dropped on every route, which meant
// asking Gemini for a web search through Polyglot quietly got you an answer
// with no web search in it.
type NativeTools struct {
	// Protocol is the wire format these entries were read from.
	Protocol string       `json:"protocol"`
	Items    []NativeTool `json:"items"`
}

// NativeTool is one provider-executed tool, kept exactly as it arrived.
type NativeTool struct {
	// Name identifies it in a fidelity note — "googleSearch", "web_search".
	Name string `json:"name"`
	// Raw is the tool entry as the client wrote it, replayed byte for byte.
	Raw json.RawMessage `json:"raw"`
}

// Len is nil-safe.
func (n *NativeTools) Len() int {
	if n == nil {
		return 0
	}
	return len(n.Items)
}

// From reports whether these came from the named protocol.
func (n *NativeTools) From(protocol string) bool {
	return n != nil && len(n.Items) > 0 && n.Protocol == protocol
}

// Names lists the tools for a note.
func (n *NativeTools) Names() []string {
	if n == nil {
		return nil
	}
	out := make([]string, 0, len(n.Items))
	for _, it := range n.Items {
		out = append(out, it.Name)
	}
	sort.Strings(out)
	return out
}

// Add appends a tool, creating the set on first use.
func (n *NativeTools) Add(protocol, name string, raw json.RawMessage) *NativeTools {
	if n == nil {
		n = &NativeTools{Protocol: protocol}
	}
	if len(n.Items) >= MaxExtensions {
		return n
	}
	n.Items = append(n.Items, NativeTool{Name: name, Raw: raw})
	return n
}

// Len reports how many extensions are held. It is nil-safe.
func (e *Extensions) Len() int {
	if e == nil {
		return 0
	}
	return len(e.Items)
}

// From reports whether these extensions came from the named protocol, which is
// the condition for re-emitting them.
func (e *Extensions) From(protocol string) bool {
	return e != nil && len(e.Items) > 0 && e.Protocol == protocol
}

// Names lists the qualified member names, sorted, for a fidelity note.
func (e *Extensions) Names() []string {
	if e == nil {
		return nil
	}
	out := make([]string, 0, len(e.Items))
	for _, it := range e.Items {
		if it.Path == "" {
			out = append(out, it.Name)
		} else {
			out = append(out, it.Path+"."+it.Name)
		}
	}
	sort.Strings(out)
	return out
}

// Summary renders the member names for a note, capped so one strange request
// cannot write a paragraph into every log row.
func (e *Extensions) Summary() string {
	names := e.Names()
	const show = 8
	if len(names) > show {
		return strings.Join(names[:show], ", ") + ", …"
	}
	return strings.Join(names, ", ")
}
