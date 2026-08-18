package gemini

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/qunqin24/polyglot/internal/canonical"
)

// thinkingStream is a Gemini reply that thinks before answering. The signature
// arrives the way Gemini sends it: on its own part at the end of the thinking
// block, with no text of its own.
const thinkingStream = `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"Let me ","thought":true}]}}]}

data: {"candidates":[{"content":{"role":"model","parts":[{"text":"check.","thought":true}]}}]}

data: {"candidates":[{"content":{"role":"model","parts":[{"thought":true,"thoughtSignature":"SIG-ABC"}]}}]}

data: {"candidates":[{"content":{"role":"model","parts":[{"text":"It is 18C."}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5}}

`

// answerSignedStream is the other place Gemini puts the token, and the one
// that matters most in practice: the thinking parts are unsigned and the
// signature closes the block on the TEXT part of the answer.
const answerSignedStream = `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"Let me think.","thought":true}]}}]}

data: {"candidates":[{"content":{"role":"model","parts":[{"text":"It is 18C.","thoughtSignature":"SIG-ON-TEXT"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5}}

`

// Gemini may send the signature in a final text part with no text of its own.
// It still closes the whole model turn and must not be filtered as empty.
const emptyTextSignedStream = `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"Let me think.","thought":true}]}}]}

data: {"candidates":[{"content":{"role":"model","parts":[{"text":"It is 18C."}]}}]}

data: {"candidates":[{"content":{"role":"model","parts":[{"text":"","thoughtSignature":"SIG-EMPTY-TEXT"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5}}

`

func relayStream(t *testing.T, sse string) string {
	t.Helper()
	var events []*canonical.Event
	err := Codec{}.DecodeStream(context.Background(), strings.NewReader(sse), func(ev *canonical.Event) error {
		cp := *ev
		events = append(events, &cp)
		return nil
	})
	if err != nil {
		t.Fatalf("DecodeStream: %v", err)
	}
	var out strings.Builder
	enc := Codec{}.NewStreamEncoder(&out, &canonical.Request{Model: "m"})
	for _, ev := range events {
		if err := enc.Write(ev); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	if err := enc.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return out.String()
}

func replayRelayedStream(t *testing.T, sse string) ([]byte, *canonical.Diagnostics) {
	t.Helper()
	var parts []part
	for _, line := range strings.Split(relayStream(t, sse), "\n") {
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		var chunk generateResponse
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			t.Fatalf("client could not parse a chunk: %v", err)
		}
		if len(chunk.Candidates) == 0 || chunk.Candidates[0].Content == nil {
			continue
		}
		parts = append(parts, chunk.Candidates[0].Content.Parts...)
	}
	body, err := json.Marshal(generateRequest{Contents: []content{
		{Role: "user", Parts: []part{{Text: "weather?"}}},
		{Role: "model", Parts: parts},
		{Role: "user", Parts: []part{{Text: "and tomorrow?"}}},
	}})
	if err != nil {
		t.Fatalf("marshal history: %v", err)
	}

	d := canonical.NewDiagnostics()
	req, err := Codec{}.DecodeRequest(body, d)
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}
	req.Model = "m"
	sent, err := Codec{}.EncodeRequest(req, d)
	if err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}
	return sent, d
}

func assertReasoningReplayedWithoutLoss(t *testing.T, sent []byte, d *canonical.Diagnostics, text string) {
	t.Helper()
	for _, n := range d.All() {
		if strings.Contains(n.Field, "reasoning") {
			t.Errorf("replaying a signed Gemini reply was reported as lossy: %+v", n)
		}
	}
	if !strings.Contains(string(sent), `"text":"`+text+`","thought":true`) {
		t.Errorf("signed reasoning was not replayed:\n%s", sent)
	}
}

// A thought signature must reach the client even though the part carrying it
// has no text.
//
// It is not an empty part. It is the replay token for the whole thinking
// block, and the client has to send it back with that thought on the next
// turn or Gemini will not accept the thought at all. Skipping it for having
// no text handed the client a history it could never replay: Polyglot then
// dropped the unsigned reasoning from every following request and reported it
// as a conversion loss, when the loss was its own, one turn earlier.
func TestAThoughtSignatureReachesTheClientWithoutTextOfItsOwn(t *testing.T) {
	out := relayStream(t, thinkingStream)
	if !strings.Contains(out, "SIG-ABC") {
		t.Fatalf("the thought signature never reached the client:\n%s", out)
	}
	// AI SDK 6 discards empty reasoning deltas before exposing their provider
	// metadata. Put the token on the immediately preceding non-empty delta so
	// OpenCode can store and replay it.
	if !strings.Contains(out, `{"text":"check.","thought":true,"thoughtSignature":"SIG-ABC"}`) {
		t.Errorf("the signature was not attached to replayable reasoning:\n%s", out)
	}
}

// The whole loop, which is what the user actually experiences: a streamed
// reply comes back, the client replays that turn, and the next request must
// carry the reasoning rather than reporting it as lost.
func TestReplayingAStreamedThoughtLosesNothing(t *testing.T) {
	relayed := relayStream(t, thinkingStream)

	// Rebuild the assistant turn from what the client received, the way a
	// client library does when it appends the reply to its history.
	var parts []part
	for _, line := range strings.Split(relayed, "\n") {
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		var chunk generateResponse
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			t.Fatalf("client could not parse a chunk: %v", err)
		}
		if len(chunk.Candidates) == 0 || chunk.Candidates[0].Content == nil {
			continue
		}
		parts = append(parts, chunk.Candidates[0].Content.Parts...)
	}

	history := generateRequest{Contents: []content{
		{Role: "user", Parts: []part{{Text: "weather?"}}},
		{Role: "model", Parts: parts},
		{Role: "user", Parts: []part{{Text: "and tomorrow?"}}},
	}}
	body, err := json.Marshal(history)
	if err != nil {
		t.Fatalf("marshal history: %v", err)
	}

	d := canonical.NewDiagnostics()
	req, err := Codec{}.DecodeRequest(body, d)
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}
	req.Model = "m"
	sent, err := Codec{}.EncodeRequest(req, d)
	if err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}

	for _, n := range d.All() {
		if strings.Contains(n.Field, "reasoning") {
			t.Errorf("replaying a reply Polyglot itself produced was reported as lossy: %+v", n)
		}
	}
	if !strings.Contains(string(sent), "SIG-ABC") {
		t.Errorf("the signature did not survive back to the upstream:\n%s", sent)
	}
}

// The same loop, for the shape where the signature rides on the answer text
// rather than on a thought part.
//
// This is the case that kept the loss alive after the thought-part leak was
// closed. Canonical had nowhere to put a token on a text part — reasoning and
// tool calls each had a home, plain text did not — so it was dropped on
// decode, before any encoder could have written it back.
func TestASignatureOnTheAnswerTextSurvivesAndReplays(t *testing.T) {
	relayed := relayStream(t, answerSignedStream)
	if !strings.Contains(relayed, "SIG-ON-TEXT") {
		t.Fatalf("the signature on the answer text never reached the client:\n%s", relayed)
	}

	sent, d := replayRelayedStream(t, answerSignedStream)
	if !strings.Contains(string(sent), "SIG-ON-TEXT") {
		t.Errorf("the signature did not survive back to the upstream:\n%s", sent)
	}
	assertReasoningReplayedWithoutLoss(t, sent, d, "Let me think.")
}

func TestAnEmptyTextSignatureSurvivesAndReplaysTheThought(t *testing.T) {
	relayed := relayStream(t, emptyTextSignedStream)
	if !strings.Contains(relayed, `{"text":"It is 18C.","thoughtSignature":"SIG-EMPTY-TEXT"}`) {
		t.Fatalf("the trailing signature was not attached to non-empty text:\n%s", relayed)
	}
	if strings.Contains(relayed, `{"thoughtSignature":"SIG-EMPTY-TEXT"}`) {
		t.Fatalf("the signature remained on an empty delta that AI SDK 6 discards:\n%s", relayed)
	}

	sent, d := replayRelayedStream(t, emptyTextSignedStream)
	if !strings.Contains(string(sent), "SIG-EMPTY-TEXT") {
		t.Errorf("the empty text signature did not survive back to the upstream:\n%s", sent)
	}
	assertReasoningReplayedWithoutLoss(t, sent, d, "Let me think.")
}

func TestAccumulatorKeepsASignatureOnText(t *testing.T) {
	acc := canonical.NewAccumulator()
	acc.Add(&canonical.Event{Type: canonical.EventTextDelta, Index: 0, Text: "answer"})
	acc.Add(&canonical.Event{Type: canonical.EventTextDelta, Index: 0, Signature: "SIG-ON-TEXT"})

	parts := acc.Response().Message.Content
	if len(parts) != 1 || parts[0].Text != "answer" || parts[0].Signature != "SIG-ON-TEXT" {
		t.Fatalf("accumulated text lost its signature: %+v", parts)
	}
}
