package otelemetry

import "testing"

func TestExportEndpointDefaults(t *testing.T) {
	for _, tc := range []struct {
		protocol ExportProtocol
		endpoint string
		want     string
	}{
		{ProtocolGRPC, "", "localhost:4317"},
		{ProtocolHTTP, "", "localhost:4318"},
		{ProtocolJSON, "", "localhost:4318"},
		{ProtocolHTTP, "collector:9999", "collector:9999"},
	} {
		if got := exportEndpoint(tc.protocol, tc.endpoint); got != tc.want {
			t.Errorf("exportEndpoint(%q, %q) = %q, want %q", tc.protocol, tc.endpoint, got, tc.want)
		}
	}
}
