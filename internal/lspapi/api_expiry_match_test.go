package lspapi

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"utexo-lsp/pkg/node_client"
)

func TestAlignAndValidateLNExpiryWithRGBAutofill(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	exp := now.Unix() + 3600
	ln := &LNInvoiceInput{}
	decoded := &node_client.DecodeRGBInvoiceResponse{ExpirationTimestamp: &exp}

	require.NoError(t, alignAndValidateLNExpiryWithRGB(ln, decoded, now, 5))
	require.EqualValues(t, 3600, ln.ExpirySec)
}

func TestAlignAndValidateLNExpiryWithRGBRejectsMismatch(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	exp := now.Unix() + 3600
	ln := &LNInvoiceInput{ExpirySec: 1200}
	decoded := &node_client.DecodeRGBInvoiceResponse{ExpirationTimestamp: &exp}

	require.Error(t, alignAndValidateLNExpiryWithRGB(ln, decoded, now, 5))
}

func TestAlignAndValidateLNExpiryWithRGBAllowsTolerance(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	exp := now.Unix() + 3600
	ln := &LNInvoiceInput{ExpirySec: 3598}
	decoded := &node_client.DecodeRGBInvoiceResponse{ExpirationTimestamp: &exp}

	require.NoError(t, alignAndValidateLNExpiryWithRGB(ln, decoded, now, 5))
}
