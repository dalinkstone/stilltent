package metrics

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
)

// Exporter serves metrics in Prometheus exposition format over HTTP.
type Exporter struct {
	collector *Collector
	addr      string
}

// NewExporter creates a new Prometheus metrics exporter.
func NewExporter(collector *Collector, addr string) *Exporter {
	if addr == "" {
		addr = ":9100"
	}
	return &Exporter{
		collector: collector,
		addr:      addr,
	}
}

// Handler returns an http.Handler that serves metrics in Prometheus format.
func (e *Exporter) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		output := FormatPrometheus(e.collector.Samples())
		fmt.Fprint(w, output)
	})
}

// Addr returns the configured listen address.
func (e *Exporter) Addr() string {
	return e.addr
}

// FormatPrometheus converts a list of samples to Prometheus exposition format.
func FormatPrometheus(samples []Sample) string {
	if len(samples) == 0 {
		return ""
	}

	var b strings.Builder

	// Group samples by name to emit HELP/TYPE once per metric.
	type metricGroup struct {
		help    string
		typ     MetricType
		samples []Sample
	}
	groups := make(map[string]*metricGroup)
	var order []string

	for _, s := range samples {
		g, ok := groups[s.Name]
		if !ok {
			g = &metricGroup{help: s.Help, typ: s.Type}
			groups[s.Name] = g
			order = append(order, s.Name)
		}
		g.samples = append(g.samples, s)
	}

	sort.Strings(order)

	for _, name := range order {
		g := groups[name]
		fmt.Fprintf(&b, "# HELP %s %s\n", name, g.help)
		typStr := "gauge"
		if g.typ == MetricCounter {
			typStr = "counter"
		}
		fmt.Fprintf(&b, "# TYPE %s %s\n", name, typStr)

		for _, s := range g.samples {
			fmt.Fprintf(&b, "%s%s %g\n", s.Name, formatLabels(s.Labels), s.Value)
		}
		b.WriteString("\n")
	}

	return b.String()
}

// formatLabels formats a label map into Prometheus label syntax {k="v",...}.
func formatLabels(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}

	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var parts []string
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf(`%s="%s"`, k, escapeLabelValue(labels[k])))
	}

	return "{" + strings.Join(parts, ",") + "}"
}

// escapeLabelValue escapes special characters in Prometheus label values.
func escapeLabelValue(v string) string {
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, `"`, `\"`)
	v = strings.ReplaceAll(v, "\n", `\n`)
	return v
}
