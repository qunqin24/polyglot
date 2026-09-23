package live

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/qunqin24/polyglot/internal/canonical"
)

func TestLiveMessagesRoundTripWithoutLosingSessionFields(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		client    bool
	}{
		{"setup", `{"setup":{"model":"models/my-model","generationConfig":{"responseModalities":["AUDIO"],"speechConfig":{"voiceConfig":{"prebuiltVoiceConfig":{"voiceName":"Aoede"}}}},"tools":[{"functionDeclarations":[{"name":"lookup"}]}],"sessionResumption":{"handle":"handle-123"},"inputAudioTranscription":{},"systemInstruction":{"parts":[{"text":"Speak clearly"}]}}}`, true},
		{"audio", `{"realtimeInput":{"audio":{"mimeType":"audio/pcm;rate=16000","data":"AAECAw=="},"activityStart":{},"audioStreamEnd":true}}`, true},
		{"tool reply", `{"toolResponse":{"functionResponses":[{"id":"call-1","name":"lookup","response":{"value":42},"extraResponse":"kept"}]}}`, true},
		{"content", `{"clientContent":{"turns":[{"role":"user","parts":[{"text":"hi"},{"inlineData":{"mimeType":"image/png","data":"AQ=="}}]}],"turnComplete":true}}`, true},
		{"server audio", `{"serverContent":{"modelTurn":{"role":"model","parts":[{"inlineData":{"mimeType":"audio/pcm;rate=24000","data":"AAEC"}},{"text":"Hello"}]},"outputTranscription":{"text":"Hello","languageCode":"en"},"turnComplete":true},"usageMetadata":{"promptTokenCount":8,"responseTokenCount":5,"totalTokenCount":13}}`, false},
		{"tool call", `{"toolCall":{"functionCalls":[{"id":"call-1","name":"lookup","args":{"id":1},"extraCall":"kept"}]}}`, false},
		{"interruption", `{"serverContent":{"interrupted":true,"turnComplete":true}}`, false},
		{"resume", `{"sessionResumptionUpdate":{"newHandle":"handle-456","resumable":true}}`, false},
		{"new signal", `{"voiceActivity":{"activityStart":true}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var m *canonical.LiveMessage
			var err error
			if tc.client {
				m, err = DecodeClient([]byte(tc.raw))
			} else {
				m, err = DecodeServer([]byte(tc.raw))
			}
			if err != nil {
				t.Fatal(err)
			}
			d := canonical.NewDiagnostics()
			var encoded []byte
			if tc.client {
				encoded, err = EncodeClient(m, d)
			} else {
				encoded, err = EncodeServer(m, d)
			}
			if err != nil {
				t.Fatal(err)
			}
			var original, result any
			if err := json.Unmarshal([]byte(tc.raw), &original); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(encoded, &result); err != nil {
				t.Fatal(err)
			}
			// Encoders may add empty role/config members. Every original field
			// must survive unchanged, including nested vendor configuration.
			if !contains(result, original) {
				t.Fatalf("Live message lost a field:\noriginal: %s\nencoded:  %s\nnotes: %+v", tc.raw, encoded, d.All())
			}
		})
	}
}

func contains(got, want any) bool {
	switch expected := want.(type) {
	case map[string]any:
		actual, ok := got.(map[string]any)
		if !ok {
			return false
		}
		for key, v := range expected {
			if !contains(actual[key], v) {
				return false
			}
		}
		return true
	case []any:
		actual, ok := got.([]any)
		if !ok || len(actual) != len(expected) {
			return false
		}
		for i, v := range expected {
			if !contains(actual[i], v) {
				return false
			}
		}
		return true
	default:
		return reflect.DeepEqual(got, want)
	}
}

func TestLiveRejectsAmbiguousOrUnsupportedMessages(t *testing.T) {
	for _, raw := range []string{
		`{"setup":{"model":"models/test"},"realtimeInput":{"text":"drop me"}}`,
		`{"realtimeInput":{"text":"hello"`,
		`{"voiceActivity":{"start":true},"unknownControl":{"more":true}}`,
	} {
		if _, err := DecodeClient([]byte(raw)); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
}
