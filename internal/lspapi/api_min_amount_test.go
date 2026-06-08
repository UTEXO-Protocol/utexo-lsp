package lspapi

import (
	"testing"

	"utexo-lsp/pkg/node_client"

	"github.com/stretchr/testify/require"
)

func TestEnsureLNInvoiceInputMinAmount(t *testing.T) {
	min := uint64(3_000_000)

	ln := &LNInvoiceInput{}
	require.NoError(t, ensureLNInvoiceInputMinAmount(ln, min))
	require.NotNil(t, ln.AmtMsat)
	require.Equal(t, min, *ln.AmtMsat)

	tooLow := uint64(1000)
	ln2 := &LNInvoiceInput{AmtMsat: &tooLow}
	require.Error(t, ensureLNInvoiceInputMinAmount(ln2, min))
}

func TestEnsureDecodedLNMinAmount(t *testing.T) {
	min := uint64(3_000_000)

	require.Error(t, ensureDecodedLNMinAmount(&node_client.DecodeLNInvoiceResponse{}, min))

	tooLow := int64(1000)
	require.Error(t, ensureDecodedLNMinAmount(&node_client.DecodeLNInvoiceResponse{AmtMsat: tooLow}, min))

	ok := int64(3_000_000)
	require.NoError(t, ensureDecodedLNMinAmount(&node_client.DecodeLNInvoiceResponse{AmtMsat: ok}, min))
}
