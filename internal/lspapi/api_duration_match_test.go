package lspapi

import (
	"testing"
	"time"

	"utexo-lsp/pkg/node_client"

	"github.com/stretchr/testify/require"
)

func TestAlignAndValidateRGBDurationWithLNAutofill(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	decoded := &node_client.DecodeLNInvoiceResponse{
		Timestamp: now.Unix(),
		ExpirySec: 3600,
	}
	params := &RGBInvoiceInput{}

	require.NoError(t, alignAndValidateRGBDurationWithLN(params, decoded, now, 5))
	require.NotNil(t, params.DurationSeconds)
	require.EqualValues(t, 3600, *params.DurationSeconds)
}

func TestAlignAndValidateRGBDurationWithLNRejectsMismatch(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	decoded := &node_client.DecodeLNInvoiceResponse{
		Timestamp: now.Unix(),
		ExpirySec: 3600,
	}
	d := uint32(1200)
	params := &RGBInvoiceInput{DurationSeconds: &d}

	require.Error(t, alignAndValidateRGBDurationWithLN(params, decoded, now, 5))
}

func TestAlignAndValidateRGBDurationWithLNAllowsTolerance(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	decoded := &node_client.DecodeLNInvoiceResponse{
		Timestamp: now.Unix(),
		ExpirySec: 3600,
	}
	d := uint32(3598)
	params := &RGBInvoiceInput{DurationSeconds: &d}

	require.NoError(t, alignAndValidateRGBDurationWithLN(params, decoded, now, 5))
}
