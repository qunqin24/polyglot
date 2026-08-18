package protocol_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRuntimeUsesNoVendorSDK enforces the rule mechanically instead of trusting
// a document: Polyglot implements every protocol itself, against net/http and
// encoding/json. The official SDKs are compatibility probes and live in their
// own module under tests/compatibility, so they can never be linked into the
// binary. If a vendor SDK appears in the root go.mod, someone reached for a
// shortcut that would hand ownership of the wire format to a third party.
func TestRuntimeUsesNoVendorSDK(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}

	banned := []string{
		"github.com/openai/openai-go",
		"github.com/anthropics/anthropic-sdk-go",
		"google.golang.org/genai",
		"cloud.google.com/go/vertexai",
	}
	for _, mod := range banned {
		if strings.Contains(string(b), mod) {
			t.Errorf("%s is in the root go.mod; vendor SDKs are test-only and belong in "+
				"tests/compatibility, never in runtime code", mod)
		}
	}
}
