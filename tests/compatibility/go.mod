// Official-SDK compatibility tests.
//
// This is a SEPARATE Go module on purpose. Polyglot's runtime implements every
// protocol itself, against net/http and encoding/json, and must never depend on
// a vendor SDK to do it. Keeping the SDKs here means they cannot appear in the
// root go.mod, cannot be linked into the production binary, and cannot creep
// into runtime code by accident — `go build ./...` at the repository root never
// even resolves them.
//
// These tests do not import Polyglot. They build the real binary, run it, and
// talk to it over HTTP exactly as a user's application would.
module github.com/qunqin24/polyglot/tests/compatibility

go 1.25.0

require (
	cloud.google.com/go v0.116.0 // indirect
	cloud.google.com/go/auth v0.9.3 // indirect
	cloud.google.com/go/compute/metadata v0.5.0 // indirect
	github.com/anthropics/anthropic-sdk-go v1.63.1 // indirect
	github.com/bahlo/generic-list-go v0.2.0 // indirect
	github.com/buger/jsonparser v1.1.2 // indirect
	github.com/golang/groupcache v0.0.0-20210331224755-41bb18bfe9da // indirect
	github.com/google/go-cmp v0.6.0 // indirect
	github.com/google/s2a-go v0.1.8 // indirect
	github.com/googleapis/enterprise-certificate-proxy v0.3.4 // indirect
	github.com/gorilla/websocket v1.5.3 // indirect
	github.com/invopop/jsonschema v0.14.0 // indirect
	github.com/openai/openai-go/v3 v3.51.0 // indirect
	github.com/pb33f/ordered-map/v2 v2.3.1 // indirect
	github.com/standard-webhooks/standard-webhooks/libraries v0.0.1 // indirect
	github.com/tidwall/gjson v1.19.0 // indirect
	github.com/tidwall/match v1.1.1 // indirect
	github.com/tidwall/pretty v1.2.1 // indirect
	github.com/tidwall/sjson v1.2.5 // indirect
	go.opencensus.io v0.24.0 // indirect
	go.yaml.in/yaml/v4 v4.0.0-rc.2 // indirect
	golang.org/x/crypto v0.54.0 // indirect
	golang.org/x/net v0.57.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.40.0 // indirect
	google.golang.org/genai v1.68.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20240903143218-8af14fe29dc1 // indirect
	google.golang.org/grpc v1.66.2 // indirect
	google.golang.org/protobuf v1.34.2 // indirect
)
