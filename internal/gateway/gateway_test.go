package gateway

import (
	"testing"

	"github.com/qunqin24/polyglot/internal/canonical"
)

func TestMetadataOnlyDeltasAreNotContentTokens(t *testing.T) {
	tests := []struct {
		name string
		ev   canonical.Event
		want bool
	}{
		{name: "text", ev: canonical.Event{Type: canonical.EventTextDelta, Text: "answer"}, want: true},
		{name: "reasoning", ev: canonical.Event{Type: canonical.EventReasoningDelta, Text: "thinking"}, want: true},
		{name: "empty text signature", ev: canonical.Event{Type: canonical.EventTextDelta, Signature: "sig"}},
		{name: "empty reasoning signature", ev: canonical.Event{Type: canonical.EventReasoningDelta,
			Reasoning: &canonical.ReasoningMeta{Signature: "sig"}}},
		{name: "tool start", ev: canonical.Event{Type: canonical.EventToolCallStart}, want: true},
		{name: "tool arguments", ev: canonical.Event{Type: canonical.EventToolCallDelta, ArgumentsDelta: "{}"}, want: true},
		{name: "usage", ev: canonical.Event{Type: canonical.EventUsage}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isContent(&tc.ev); got != tc.want {
				t.Errorf("isContent() = %v, want %v", got, tc.want)
			}
		})
	}
}
