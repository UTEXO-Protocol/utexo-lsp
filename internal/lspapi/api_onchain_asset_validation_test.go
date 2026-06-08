package lspapi

import (
	"testing"

	"github.com/stretchr/testify/require"

	"utexo-lsp/pkg/node_client"
)

func TestApplyAndValidateOnchainAssetParamsAutofillsMatchingFields(t *testing.T) {
	ln := &LNInvoiceInput{}
	assetID := "asset123"
	decoded := &node_client.DecodeRGBInvoiceResponse{
		AssetID: &assetID,
		Assignment: map[string]any{
			"type":  "Fungible",
			"value": float64(42),
		},
	}

	require.NoError(t, applyAndValidateOnchainAssetParams(ln, decoded))
	require.NotNil(t, ln.AssetID)
	require.Equal(t, assetID, *ln.AssetID)
	require.NotNil(t, ln.AssetAmount)
	require.EqualValues(t, 42, *ln.AssetAmount)
}

func TestApplyAndValidateOnchainAssetParamsRejectsAssetIDMismatch(t *testing.T) {
	reqAssetID := "assetABC"
	ln := &LNInvoiceInput{AssetID: &reqAssetID}
	decodedAssetID := "assetXYZ"
	decoded := &node_client.DecodeRGBInvoiceResponse{AssetID: &decodedAssetID}

	err := applyAndValidateOnchainAssetParams(ln, decoded)
	require.Error(t, err)
}

func TestApplyAndValidateOnchainAssetParamsRejectsAssetAmountMismatch(t *testing.T) {
	reqAmount := uint64(7)
	ln := &LNInvoiceInput{AssetAmount: &reqAmount}
	decoded := &node_client.DecodeRGBInvoiceResponse{
		Assignment: map[string]any{
			"type":  "Fungible",
			"value": float64(8),
		},
	}

	err := applyAndValidateOnchainAssetParams(ln, decoded)
	require.Error(t, err)
}

func TestExtractFungibleAssignmentAmount(t *testing.T) {
	amount, ok := extractFungibleAssignmentAmount(map[string]any{
		"type":  "Fungible",
		"value": "123",
	})
	require.True(t, ok)
	require.EqualValues(t, 123, amount)
}
