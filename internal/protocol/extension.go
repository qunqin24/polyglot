package protocol

// Cross-protocol wire extensions.
//
// Gemini 3 binds an opaque `thoughtSignature` to every function call it makes
// and rejects a later request whose first functionCall part in a step has lost
// it. Only Gemini's own wire format has a field for it, so when the client
// speaks a different protocol the signature has to ride out to the client and
// come back, or multi-turn tool use against a Gemini upstream breaks.
//
// Google solved the same problem for its own OpenAI-compatibility layer by
// hanging an `extra_content` object off each tool call:
//
//	"tool_calls": [{
//	  "id": "...", "type": "function", "function": {...},
//	  "extra_content": {"google": {"thought_signature": "..."}}
//	}]
//
// Polyglot reuses that exact spelling rather than inventing one, so clients
// that already know how to preserve it keep working, and it stays obvious
// whose signature this is. The envelope is nested under a vendor key on
// purpose: it never looks like a standard field of the protocol carrying it.
//
// This travels on the client side of a conversation only — see the codecs.
// Polyglot never sends it to an upstream that did not define it.
type ExtraContent struct {
	Google *GoogleExtra `json:"google,omitempty"`
}

type GoogleExtra struct {
	ThoughtSignature string `json:"thought_signature,omitempty"`
}

// SignatureExtra wraps a signature for the wire, returning nil when there is
// nothing to carry so the field stays absent instead of appearing empty.
func SignatureExtra(signature string) *ExtraContent {
	if signature == "" {
		return nil
	}
	return &ExtraContent{Google: &GoogleExtra{ThoughtSignature: signature}}
}

// Signature reads a signature back off the wire, tolerating every shape a
// client might echo: missing, present-but-empty, or fully populated.
func (e *ExtraContent) Signature() string {
	if e == nil || e.Google == nil {
		return ""
	}
	return e.Google.ThoughtSignature
}
