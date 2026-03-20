package metrics

// TracingConfig holds OpenTelemetry tracing configuration.
// Implementation would use go.opentelemetry.io/otel.
type TracingConfig struct {
	Enabled     bool
	ServiceName string
}
