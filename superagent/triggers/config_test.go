package triggers

import "testing"

func TestCoreEndpoint(t *testing.T) {
	tests := []struct {
		name              string
		endpoint          string
		allowInsecureHTTP bool
		valid             bool
	}{
		{name: "IPv4 loopback", endpoint: "http://127.0.0.1:24370", valid: true},
		{name: "IPv6 loopback", endpoint: "http://[::1]:24370/", valid: true},
		{name: "loopback without port", endpoint: "http://127.0.0.1"},
		{name: "remote HTTPS", endpoint: "https://core.example.com", valid: true},
		{name: "remote HTTP", endpoint: "http://192.0.2.10:5970"},
		{name: "remote HTTP opt-in", endpoint: "http://192.0.2.10:5970", allowInsecureHTTP: true, valid: true},
		{name: "credentials", endpoint: "https://user:secret@core.example.com"},
		{name: "path", endpoint: "https://core.example.com/v1/channel"},
		{name: "empty query", endpoint: "https://core.example.com?"},
		{name: "empty fragment", endpoint: "https://core.example.com#"},
		{name: "unsupported scheme", endpoint: "file://core.example.com", allowInsecureHTTP: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateCoreEndpoint(tt.endpoint, tt.allowInsecureHTTP)
			if (err == nil) != tt.valid {
				t.Fatalf("validateCoreEndpoint(%q, %t) = %v, want valid=%t", tt.endpoint, tt.allowInsecureHTTP, err, tt.valid)
			}
		})
	}
}

func TestStatusEndpointStaysOnLoopback(t *testing.T) {
	if err := validateStatusEndpoint("http://127.0.0.1:24371"); err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []string{"http://192.0.2.10:24371", "https://core.example.com"} {
		if err := validateStatusEndpoint(endpoint); err == nil {
			t.Fatalf("accepted remote status endpoint %q", endpoint)
		}
	}
}
