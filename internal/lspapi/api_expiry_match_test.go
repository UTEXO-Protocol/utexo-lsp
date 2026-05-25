package lspapi

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAlignAndValidateLNExpiryWithRGBAutofill(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	exp := now.Unix() + 3600
	ln := &LNInvoiceInput{}
	decoded := &decodeRGBResponse{ExpirationTimestamp: &exp}

	require.NoError(t, alignAndValidateLNExpiryWithRGB(ln, decoded, now, 5))
	require.EqualValues(t, 3600, ln.ExpirySec)
}

func TestAlignAndValidateLNExpiryWithRGBRejectsMismatch(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	exp := now.Unix() + 3600
	ln := &LNInvoiceInput{ExpirySec: 1200}
	decoded := &decodeRGBResponse{ExpirationTimestamp: &exp}

	require.Error(t, alignAndValidateLNExpiryWithRGB(ln, decoded, now, 5))
}

func TestAlignAndValidateLNExpiryWithRGBAllowsTolerance(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	exp := now.Unix() + 3600
	ln := &LNInvoiceInput{ExpirySec: 3598}
	decoded := &decodeRGBResponse{ExpirationTimestamp: &exp}

	require.NoError(t, alignAndValidateLNExpiryWithRGB(ln, decoded, now, 5))
}
