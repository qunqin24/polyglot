package telemetry

import (
	"bytes"
	"io"
	"strconv"
	"strings"
)

// The Prometheus text exposition format, version 0.0.4. It is a documented,
// stable format that every scraper and every hosted backend understands, so
// writing it directly costs about eighty lines and no dependencies.

// writeExposition renders every registered metric. It holds no lock while
// writing, so a slow scraper cannot stall the request path.
func (r *registry) writeExposition(w io.Writer) {
	r.mu.RLock()
	metrics := append([]*metric(nil), r.order...)
	r.mu.RUnlock()

	var buf bytes.Buffer
	for _, m := range metrics {
		samples := m.snapshot()
		if len(samples) == 0 && m.kind != kindGauge {
			// A metric nobody has touched yet says nothing; skipping it keeps
			// a fresh install's output short and honest.
			continue
		}
		buf.WriteString("# HELP " + m.name + " " + escapeHelp(m.help) + "\n")
		buf.WriteString("# TYPE " + m.name + " " + m.kind.String() + "\n")
		switch m.kind {
		case kindHistogram:
			writeHistogram(&buf, m, samples)
		default:
			for _, s := range samples {
				buf.WriteString(m.name)
				writeLabels(&buf, m.labels, s.labels, "", "")
				buf.WriteByte(' ')
				writeFloat(&buf, s.value)
				buf.WriteByte('\n')
			}
		}
	}
	w.Write(buf.Bytes())
}

func writeHistogram(buf *bytes.Buffer, m *metric, samples []sample) {
	for _, s := range samples {
		for i, b := range m.buckets {
			buf.WriteString(m.name + "_bucket")
			writeLabels(buf, m.labels, s.labels, "le", strconv.FormatFloat(b, 'g', -1, 64))
			buf.WriteByte(' ')
			buf.WriteString(strconv.FormatUint(s.counts[i], 10))
			buf.WriteByte('\n')
		}
		buf.WriteString(m.name + "_bucket")
		writeLabels(buf, m.labels, s.labels, "le", "+Inf")
		buf.WriteByte(' ')
		buf.WriteString(strconv.FormatUint(s.count, 10))
		buf.WriteByte('\n')

		buf.WriteString(m.name + "_sum")
		writeLabels(buf, m.labels, s.labels, "", "")
		buf.WriteByte(' ')
		writeFloat(buf, s.sum)
		buf.WriteByte('\n')

		buf.WriteString(m.name + "_count")
		writeLabels(buf, m.labels, s.labels, "", "")
		buf.WriteByte(' ')
		buf.WriteString(strconv.FormatUint(s.count, 10))
		buf.WriteByte('\n')
	}
}

// writeLabels renders {a="1",b="2"}, optionally appending one extra pair (the
// histogram's le).
func writeLabels(buf *bytes.Buffer, names, values []string, extraName, extraValue string) {
	if len(names) == 0 && extraName == "" {
		return
	}
	buf.WriteByte('{')
	for i, n := range names {
		if i > 0 {
			buf.WriteByte(',')
		}
		buf.WriteString(n)
		buf.WriteString(`="`)
		buf.WriteString(escapeLabelValue(values[i]))
		buf.WriteByte('"')
	}
	if extraName != "" {
		if len(names) > 0 {
			buf.WriteByte(',')
		}
		buf.WriteString(extraName)
		buf.WriteString(`="`)
		buf.WriteString(extraValue)
		buf.WriteByte('"')
	}
	buf.WriteByte('}')
}

// writeFloat prints a value the way Prometheus expects: integers without a
// decimal point, everything else in the shortest round-tripping form.
func writeFloat(buf *bytes.Buffer, v float64) {
	buf.WriteString(strconv.FormatFloat(v, 'g', -1, 64))
}

var labelValueEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)

func escapeLabelValue(s string) string { return labelValueEscaper.Replace(s) }

var helpEscaper = strings.NewReplacer(`\`, `\\`, "\n", `\n`)

func escapeHelp(s string) string { return helpEscaper.Replace(s) }
