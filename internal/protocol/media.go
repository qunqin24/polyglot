package protocol

import (
	"strings"

	"github.com/qunqin24/polyglot/internal/canonical"
)

// Shared multimodal plumbing.
//
// The four protocols disagree about how to spell an attachment but agree on
// what one is, so the differences are small and mechanical:
//
//	OpenAI      image_url.url is a data: URI or an https URL
//	Responses   input_image.image_url, same two shapes, or a file_id
//	Anthropic   source is {base64, media_type, data} or {url} or {file}
//	Gemini      inlineData{mimeType,data} or fileData{fileUri}
//
// Two helpers cover the parts every codec would otherwise repeat.

// ParseDataURI splits "data:image/png;base64,AAA" into its MIME type and
// payload. A URL that is not a base64 data URI returns ok=false and is treated
// as a remote reference.
func ParseDataURI(s string) (mime, data string, ok bool) {
	if !strings.HasPrefix(s, "data:") {
		return "", "", false
	}
	rest := s[len("data:"):]
	meta, payload, found := strings.Cut(rest, ",")
	if !found {
		return "", "", false
	}
	// Parameters may follow the type: image/png;charset=x;base64
	if !strings.HasSuffix(meta, ";base64") {
		// A non-base64 data URI (percent-encoded text) is legal but nothing
		// sends one for an image, and guessing at it would be worse than
		// reporting it.
		return "", "", false
	}
	mime = strings.TrimSuffix(meta, ";base64")
	if i := strings.IndexByte(mime, ';'); i >= 0 {
		mime = mime[:i]
	}
	return mime, payload, true
}

// DataURI renders a data: URI for the protocols that carry one.
func DataURI(mime, data string) string {
	if mime == "" {
		mime = "application/octet-stream"
	}
	return "data:" + mime + ";base64," + data
}

// MediaFromURL turns whatever a client put in a url-shaped field into a Media,
// which is either inline bytes or a remote reference.
func MediaFromURL(url string) *canonical.Media {
	if mime, data, ok := ParseDataURI(url); ok {
		return &canonical.Media{MIMEType: mime, Data: data}
	}
	return &canonical.Media{URL: url}
}

// ClassifyMedia picks image or file from a MIME type, and reports false for
// content Polyglot does not convert yet.
//
// The false case earns its place: audio and video are not implemented, and
// without this check they would fall through to "file" and be sent as a
// document — which the target then rejects. Reporting an attachment Polyglot
// cannot convert is honest; smuggling it through in the wrong shape and
// letting the upstream 400 is not.
func ClassifyMedia(mime string) (canonical.PartType, bool) {
	switch {
	case strings.HasPrefix(mime, "image/"):
		return canonical.PartImage, true
	case strings.HasPrefix(mime, "audio/"), strings.HasPrefix(mime, "video/"):
		return "", false
	default:
		return canonical.PartFile, true
	}
}

// AcceptsMediaURL reports whether a protocol will fetch a remote attachment
// itself. Gemini will not: it takes inline bytes, or a URI for a file already
// uploaded to Google. Everything else takes an https URL directly.
//
// This is a property of the protocol rather than a method on Codec so that
// adding a protocol stays "one package plus a registration" — the rule the
// architecture is built on.
func AcceptsMediaURL(n Name) bool { return n != Gemini }

// MediaNote reports an attachment that could not be carried, naming what it
// was without ever putting the payload in a log line.
func MediaNote(d *canonical.Diagnostics, field string, m *canonical.Media, reason string) {
	d.Note(field, canonical.FidelityUnsupported, "%s was not forwarded: %s", m.Describe(), reason)
}

// BoundMediaUsable reports whether a provider-issued file handle can be used
// by this protocol. A handle only means something to whoever issued it, so it
// follows the same rule as a native tool or a replay token.
func BoundMediaUsable(n Name, m *canonical.Media) bool {
	return m.Bound() && m.Provider == string(n)
}
