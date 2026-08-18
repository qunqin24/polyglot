package canonical

import "fmt"

// Fidelity records how faithfully a field survived a protocol conversion.
// Protocols are not equivalent; Polyglot never silently drops a field, it
// records what happened so the Inspector and logs can show it.
type Fidelity string

const (
	// FidelityExact: the field has a direct, identical counterpart.
	FidelityExact Fidelity = "exact"
	// FidelitySemantic: represented differently but with the same meaning.
	FidelitySemantic Fidelity = "semantic"
	// FidelityLossy: carried over with a loss of precision or detail.
	FidelityLossy Fidelity = "lossy"
	// FidelityUnsupported: the target protocol cannot express this at all.
	FidelityUnsupported Fidelity = "unsupported"
)

// Mode selects what Polyglot does when a conversion is not exact. Only
// ModeBestEffort is wired into the gateway today; the other two are recorded
// here so the behaviour is a policy decision rather than an accident.
type Mode string

const (
	// ModeStrict: fail the request on lossy or unsupported conversions.
	ModeStrict Mode = "strict"
	// ModeWarn: proceed, but surface warnings to the client.
	ModeWarn Mode = "warn"
	// ModeBestEffort: proceed silently, recording notes for logs/Inspector.
	ModeBestEffort Mode = "best_effort"
)

// Note is a single conversion observation.
type Note struct {
	// Stage is where it happened, e.g. "decode:openai" or "encode:gemini".
	Stage    string   `json:"stage"`
	Field    string   `json:"field"`
	Fidelity Fidelity `json:"fidelity"`
	Detail   string   `json:"detail,omitempty"`
}

// Diagnostics collects conversion notes for one request. A nil *Diagnostics is
// valid and drops everything, so codecs can be called without one.
type Diagnostics struct {
	Stage string
	Mode  Mode
	Notes []Note

	parent *Diagnostics
}

func NewDiagnostics() *Diagnostics { return &Diagnostics{Mode: ModeBestEffort} }

// WithStage returns a view that tags subsequent notes with the given stage.
// It shares the underlying note slice through the parent.
func (d *Diagnostics) WithStage(stage string) *Diagnostics {
	if d == nil {
		return nil
	}
	return &Diagnostics{Stage: stage, Mode: d.Mode, Notes: nil, parent: d}
}

func (d *Diagnostics) Note(field string, f Fidelity, format string, args ...any) {
	if d == nil {
		return
	}
	n := Note{Stage: d.Stage, Field: field, Fidelity: f, Detail: fmt.Sprintf(format, args...)}
	d.add(n)
}

func (d *Diagnostics) add(n Note) {
	if d.parent != nil {
		d.parent.add(n)
		return
	}
	d.Notes = append(d.Notes, n)
}

// Lossy reports whether any note is lossy or unsupported.
func (d *Diagnostics) Lossy() bool {
	if d == nil {
		return false
	}
	for _, n := range d.Notes {
		if n.Fidelity == FidelityLossy || n.Fidelity == FidelityUnsupported {
			return true
		}
	}
	return false
}

// All returns the collected notes.
func (d *Diagnostics) All() []Note {
	if d == nil {
		return nil
	}
	return d.Notes
}
