package main

import "testing"

func TestRequiresAccessToken(t *testing.T) {
	tests := []struct {
		name            string
		allowRemote     bool
		trustedProxies  string
		trustCloudflare bool
		want            bool
	}{
		{
			name:        "loopback development",
			allowRemote: false,
			want:        false,
		},
		{
			name:        "direct non-loopback exposure",
			allowRemote: true,
			want:        true,
		},
		{
			name:           "generic trusted proxy still requires bearer",
			allowRemote:    true,
			trustedProxies: "172.20.0.0/24",
			want:           true,
		},
		{
			name:            "trusted Cloudflare Tunnel may use upstream access policy",
			allowRemote:     true,
			trustedProxies:  "172.20.0.0/24",
			trustCloudflare: true,
			want:            false,
		},
		{
			name:            "Cloudflare trust without proxy configuration remains protected",
			allowRemote:     true,
			trustCloudflare: true,
			want:            true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := requiresAccessToken(
				tt.allowRemote,
				tt.trustedProxies,
				tt.trustCloudflare,
			)

			if got != tt.want {
				t.Fatalf(
					"requiresAccessToken() = %v, want %v",
					got,
					t.want,
				)
			}
		})
	}
}
