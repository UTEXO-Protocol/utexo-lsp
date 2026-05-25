package lspapi

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateLightningAddressDomainURL(t *testing.T) {
	base := Config{
		LSPBaseURL:                      "http://127.0.0.1:3001",
		LightningAddressMinSendableMsat: 1,
		LightningAddressMaxSendableMsat: 1,
	}

	tests := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{name: "origin", url: "http://127.0.0.1:8080", wantErr: false},
		{name: "path", url: "https://example.com/app", wantErr: true},
		{name: "trailing slash", url: "https://example.com/", wantErr: true},
		{name: "query", url: "https://example.com?x=1", wantErr: true},
		{name: "fragment", url: "https://example.com#frag", wantErr: true},
		{name: "scheme-less", url: "//example.com", wantErr: true},
		{name: "userinfo", url: "https://user:pass@example.com", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := base
			cfg.LightningAddressDomainURL = tt.url

			err := cfg.Validate()
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}
