// Package protocol defines the contract every wire protocol implements to
// join Polyglot's hub: Protocol <-> Canonical, and nothing else. There is
// deliberately no A-to-B conversion anywhere in the system.
package protocol

import (
	"context"
	"fmt"
	"io"
	"sort"

	"github.com/qunqin24/polyglot/internal/canonical"
)

// Name identifies a wire protocol, not a vendor. OpenRouter, DeepSeek and
// SiliconFlow are all Providers speaking protocol "openai".
type Name string

const (
	OpenAI    Name = "openai"
	Anthropic Name = "anthropic"
	Gemini    Name = "gemini"
	// OpenAIResponses is OpenAI's Responses API. It is a separate protocol
	// rather than a mode of OpenAI: the request carries `input` items instead
	// of `messages`, tools are flat, and the stream is a typed event sequence.
	OpenAIResponses Name = "openai-responses"
	// GeminiInteractions is Google's Interactions API, the successor to
	// generateContent. Same reasoning as above: it carries a flat typed
	// `input` array instead of contents/parts, answers with a `steps`
	// timeline instead of candidates, declares tools flat, and streams a
	// different event set. A genuinely different wire format is a protocol,
	// not a flag on an existing one.
	GeminiInteractions Name = "gemini-interactions"
)

func (n Name) Valid() bool {
	switch n {
	case OpenAI, Anthropic, Gemini, OpenAIResponses, GeminiInteractions:
		return true
	}
	return false
}

// Display returns a human label for the WebUI.
func (n Name) Display() string {
	switch n {
	case OpenAI:
		return "OpenAI Compatible"
	case Anthropic:
		return "Anthropic"
	case Gemini:
		return "Gemini"
	case OpenAIResponses:
		return "OpenAI Responses"
	case GeminiInteractions:
		return "Gemini Interactions"
	}
	return string(n)
}

// Codec converts one protocol to and from canonical form.
//
// Decode* is used when Polyglot acts as a server for this protocol (a client
// speaks it to us) or when it reads an upstream reply in this protocol.
// Encode* is the mirror. A single Codec therefore serves both the inbound and
// the outbound side, which is what makes N protocols cost N implementations
// instead of N².
type Codec interface {
	Name() Name

	// DecodeRequest parses a client request body into canonical form.
	DecodeRequest(body []byte, d *canonical.Diagnostics) (*canonical.Request, error)
	// EncodeRequest renders a canonical request as this protocol's body.
	// req.Model must already be the upstream model name.
	EncodeRequest(req *canonical.Request, d *canonical.Diagnostics) ([]byte, error)

	// DecodeResponse parses a non-streaming upstream reply.
	DecodeResponse(body []byte, d *canonical.Diagnostics) (*canonical.Response, error)
	// EncodeResponse renders a canonical reply for a client of this protocol.
	EncodeResponse(resp *canonical.Response, req *canonical.Request, d *canonical.Diagnostics) ([]byte, error)

	// DecodeStream reads an upstream stream and emits canonical events. It
	// must return promptly when ctx is cancelled. Returning an error from
	// emit aborts the read (the client went away).
	DecodeStream(ctx context.Context, r io.Reader, emit func(*canonical.Event) error) error

	// NewStreamEncoder wraps a client connection to write this protocol's
	// stream format.
	NewStreamEncoder(w io.Writer, req *canonical.Request) StreamEncoder

	// EncodeError renders a canonical error in this protocol's error shape.
	EncodeError(err *canonical.Error) []byte
}

// StreamEncoder turns canonical events into one protocol's stream bytes.
// Write must flush; the gateway does not buffer.
type StreamEncoder interface {
	Write(ev *canonical.Event) error
	// Close writes any terminator the protocol requires (e.g. "[DONE]").
	Close() error
}

var registry = map[Name]Codec{}

// Register makes a codec available. Called from each protocol package's init.
func Register(c Codec) {
	if _, dup := registry[c.Name()]; dup {
		panic(fmt.Sprintf("protocol: duplicate codec %q", c.Name()))
	}
	registry[c.Name()] = c
}

// Get returns the codec for a protocol.
func Get(n Name) (Codec, error) {
	c, ok := registry[n]
	if !ok {
		return nil, canonical.Errorf(canonical.ErrUnsupported, "unknown protocol %q", n)
	}
	return c, nil
}

// MustGet is for call sites that already validated the name.
func MustGet(n Name) Codec {
	c, err := Get(n)
	if err != nil {
		panic(err)
	}
	return c
}

// Registered lists the available protocol names, sorted.
func Registered() []Name {
	out := make([]Name, 0, len(registry))
	for n := range registry {
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
