package protocol

import (
	"encoding/json"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/qunqin24/polyglot/internal/canonical"
)

// Capturing and replaying unrecognised fields.
//
// Every codec decodes into a struct, and `encoding/json` throws away members
// the struct does not name. For a hub that promises never to drop a field
// without saying so, that is a hole: a provider's own parameters vanish with
// no note, because nothing ever noticed they were there.
//
// The two functions here close it. Codecs call Capture on the way in and Merge
// on the way out; the rule that they only come back out through the protocol
// they came in on lives in Merge, so no codec can get it wrong.

// Scope names an object to collect unknown members from, and the struct whose
// json tags say which members are known.
//
// A Path of "" is the body itself. One level of nesting is supported because
// that is what these protocols use: Gemini keeps its parameters under
// generationConfig, so top-level-only capture would miss nearly all of them.
type Scope struct {
	Path  string
	Known any
}

// Top is the scope for the body's own members.
func Top(known any) Scope { return Scope{Known: known} }

// Nested is the scope for a named object inside the body.
func Nested(path string, known any) Scope { return Scope{Path: path, Known: known} }

// Capture collects the members of body that none of the scopes recognise.
// It returns nil when everything was understood, which is the common case and
// costs one map allocation.
func Capture(proto Name, body []byte, scopes ...Scope) *canonical.Extensions {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil {
		// Not an object, or malformed. The codec's own decode reports that;
		// there is nothing to capture either way.
		return nil
	}

	ext := &canonical.Extensions{Protocol: string(proto)}
	for _, sc := range scopes {
		obj, ok := resolveScope(root, sc.Path)
		if !ok {
			continue
		}
		known := knownMembers(sc.Known)
		names := make([]string, 0, len(obj))
		for name := range obj {
			if !known[name] {
				names = append(names, name)
			}
		}
		// Sorted so the captured order is stable across runs, which keeps
		// notes and Inspector output diffable.
		sort.Strings(names)
		for _, name := range names {
			if len(ext.Items) >= canonical.MaxExtensions {
				ext.Truncated = true
				break
			}
			ext.Items = append(ext.Items, canonical.Extension{
				Path: sc.Path, Name: name, Value: obj[name],
			})
		}
	}

	if len(ext.Items) == 0 {
		return nil
	}
	return ext
}

// Merge puts captured extensions back into an encoded body.
//
// They are replayed only when they came from this same protocol. Otherwise
// they are reported as unsupported and left out: `guided_json` means something
// to a vLLM upstream and nothing to Gemini, and forwarding it across dialects
// would turn a field Polyglot could not translate into an upstream 400.
//
// An extension never overwrites a member the encoder already produced. Whatever
// the codec decided is authoritative; extensions only fill gaps.
func Merge(proto Name, ext *canonical.Extensions, encoded []byte, d *canonical.Diagnostics) []byte {
	if ext.Len() == 0 {
		return encoded
	}
	if !ext.From(string(proto)) {
		d.Note("extensions", canonical.FidelityUnsupported,
			"%d field(s) specific to %s have no equivalent in %s and were not forwarded: %s",
			ext.Len(), ext.Protocol, proto, ext.Summary())
		return encoded
	}

	var root map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &root); err != nil {
		return encoded // not an object; nothing safe to do
	}

	// Group by scope so a nested object is rewritten once, not per member.
	byPath := map[string][]canonical.Extension{}
	paths := []string{}
	for _, it := range ext.Items {
		if _, seen := byPath[it.Path]; !seen {
			paths = append(paths, it.Path)
		}
		byPath[it.Path] = append(byPath[it.Path], it)
	}
	sort.Strings(paths)

	var skipped []string
	for _, path := range paths {
		obj, ok := resolveScope(root, path)
		if !ok {
			// The encoder did not produce this object at all. A top-level or
			// single-member scope is created; an array element is not, because
			// there is no element to attach it to.
			if strings.Contains(path, ".") {
				continue
			}
			obj = map[string]json.RawMessage{}
		}
		for _, it := range byPath[path] {
			if _, exists := obj[it.Name]; exists {
				skipped = append(skipped, it.Name)
				continue
			}
			obj[it.Name] = it.Value
		}
		if path != "" && !writeScope(root, path, obj) {
			continue
		}
	}

	out, err := json.Marshal(root)
	if err != nil {
		return encoded
	}

	restored := ext.Len() - len(skipped)
	if restored > 0 {
		d.Note("extensions", canonical.FidelityExact,
			"%d unrecognised %s field(s) were forwarded unchanged: %s",
			restored, proto, ext.Summary())
	}
	if len(skipped) > 0 {
		sort.Strings(skipped)
		d.Note("extensions", canonical.FidelityLossy,
			"%d field(s) were not forwarded because the conversion set them itself: %s",
			len(skipped), strings.Join(skipped, ", "))
	}
	if ext.Truncated {
		d.Note("extensions", canonical.FidelityLossy,
			"the request carried more than %d unrecognised fields; the rest were dropped",
			canonical.MaxExtensions)
	}
	return out
}

// NoteResponseState reports Responses API server-side state when a request is
// leaving through another protocol. Only the Responses codec can forward it.
func NoteResponseState(req *canonical.Request, target Name, d *canonical.Diagnostics) {
	if req == nil || target == OpenAIResponses {
		return
	}
	if req.PreviousResponseID != "" {
		d.Note("previous_response_id", canonical.FidelityUnsupported,
			"Responses API server-side conversation state cannot be expressed in %s", target)
	}
	if req.Store != nil && *req.Store {
		d.Note("store", canonical.FidelityUnsupported,
			"Responses API response storage cannot be requested through %s", target)
	}
}

// NoteNativeContent reports an opaque provider-owned content item when it
// cannot leave through the protocol that produced it.
func NoteNativeContent(p canonical.ContentPart, target Name, field string, d *canonical.Diagnostics) {
	if p.Type != canonical.PartNative {
		return
	}
	if p.Native == nil {
		return
	}
	d.Note(field, canonical.FidelityUnsupported,
		"native %s content %q cannot be expressed in %s and was not forwarded",
		p.Native.Protocol, p.Native.Type, target)
}

// RawArray returns the elements of a top-level array member, so a codec can
// keep the original bytes of an entry it decoded into a typed struct. Indices
// line up with the decoded slice, both coming from the same array.
func RawArray(body []byte, member string) []json.RawMessage {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil {
		return nil
	}
	raw, ok := root[member]
	if !ok {
		return nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil
	}
	return items
}

// MergeNativeTools appends provider-executed tools to the encoded body's tools
// array, under the same rule as Merge: only back out through the protocol they
// came in on, and reported rather than translated otherwise.
//
// All four protocols spell this array `tools` at the top level, so one helper
// serves them all.
func MergeNativeTools(proto Name, nt *canonical.NativeTools, encoded []byte, d *canonical.Diagnostics) []byte {
	if nt.Len() == 0 {
		return encoded
	}
	if !nt.From(string(proto)) {
		d.Note("tools", canonical.FidelityUnsupported,
			"%d provider-executed tool(s) from %s cannot be expressed in %s and were not forwarded: %s",
			nt.Len(), nt.Protocol, proto, strings.Join(nt.Names(), ", "))
		return encoded
	}

	var root map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &root); err != nil {
		return encoded
	}
	var tools []json.RawMessage
	if raw, ok := root["tools"]; ok {
		if err := json.Unmarshal(raw, &tools); err != nil {
			return encoded
		}
	}
	for _, it := range nt.Items {
		tools = append(tools, it.Raw)
	}
	rawTools, err := json.Marshal(tools)
	if err != nil {
		return encoded
	}
	root["tools"] = rawTools

	out, err := json.Marshal(root)
	if err != nil {
		return encoded
	}
	d.Note("tools", canonical.FidelityExact,
		"%d provider-executed tool(s) were forwarded unchanged: %s",
		nt.Len(), strings.Join(nt.Names(), ", "))
	return out
}

// resolveScope walks a scope path into the body. A path segment that parses as
// an integer indexes an array, so "candidates.0" reaches the first candidate —
// which is where Gemini puts groundingMetadata, the citations that make a web
// search answer checkable.
func resolveScope(root map[string]json.RawMessage, path string) (map[string]json.RawMessage, bool) {
	if path == "" {
		return root, true
	}
	var cur json.RawMessage
	for i, seg := range strings.Split(path, ".") {
		if i == 0 {
			raw, ok := root[seg]
			if !ok {
				return nil, false
			}
			cur = raw
			continue
		}
		if idx, err := strconv.Atoi(seg); err == nil {
			var items []json.RawMessage
			if json.Unmarshal(cur, &items) != nil || idx >= len(items) {
				return nil, false
			}
			cur = items[idx]
			continue
		}
		var obj map[string]json.RawMessage
		if json.Unmarshal(cur, &obj) != nil {
			return nil, false
		}
		raw, ok := obj[seg]
		if !ok {
			return nil, false
		}
		cur = raw
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(cur, &obj) != nil {
		return nil, false
	}
	return obj, true
}

// writeScope puts a rewritten object back at a scope path.
func writeScope(root map[string]json.RawMessage, path string, obj map[string]json.RawMessage) bool {
	raw, err := json.Marshal(obj)
	if err != nil {
		return false
	}
	segs := strings.Split(path, ".")
	if len(segs) == 1 {
		root[path] = raw
		return true
	}
	// Only "member.index" is produced by the codecs, which keeps this to the
	// one shape that exists rather than a general JSON-pointer writer.
	if len(segs) != 2 {
		return false
	}
	idx, err := strconv.Atoi(segs[1])
	if err != nil {
		return false
	}
	var items []json.RawMessage
	if json.Unmarshal(root[segs[0]], &items) != nil || idx >= len(items) {
		return false
	}
	items[idx] = raw
	arr, err := json.Marshal(items)
	if err != nil {
		return false
	}
	root[segs[0]] = arr
	return true
}

// knownMembers reads the json member names a struct declares. Deriving them
// from the struct rather than a hand-written list means a field added to a
// codec's wire type stops being an "extension" automatically, instead of
// silently being forwarded twice.
func knownMembers(v any) map[string]bool {
	if v == nil {
		return nil
	}
	t := reflect.TypeOf(v)
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil
	}
	if cached, ok := memberCache.Load(t); ok {
		return cached.(map[string]bool)
	}

	out := map[string]bool{}
	var walk func(reflect.Type)
	walk = func(t reflect.Type) {
		for i := range t.NumField() {
			f := t.Field(i)
			tag := f.Tag.Get("json")
			if tag == "-" {
				continue
			}
			name, _, _ := strings.Cut(tag, ",")
			if f.Anonymous && name == "" {
				// An embedded struct's members are members of this object.
				et := f.Type
				for et.Kind() == reflect.Pointer {
					et = et.Elem()
				}
				if et.Kind() == reflect.Struct {
					walk(et)
					continue
				}
			}
			if name == "" {
				name = f.Name
			}
			out[name] = true
		}
	}
	walk(t)

	memberCache.Store(t, out)
	return out
}

// memberCache keeps the reflection out of the request path after the first
// call for each wire type.
var memberCache sync.Map // reflect.Type -> map[string]bool
