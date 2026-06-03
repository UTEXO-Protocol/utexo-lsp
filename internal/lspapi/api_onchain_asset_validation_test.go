package lspapi

import (
	"testing"

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

	if err := applyAndValidateOnchainAssetParams(ln, decoded); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ln.AssetID == nil || *ln.AssetID != assetID {
		t.Fatalf("expected asset_id to be autofilled to %q, got %v", assetID, ln.AssetID)
	}
	if ln.AssetAmount == nil || *ln.AssetAmount != 42 {
		t.Fatalf("expected asset_amount to be autofilled to 42, got %v", ln.AssetAmount)
	}
}

func TestApplyAndValidateOnchainAssetParamsRejectsAssetIDMismatch(t *testing.T) {
	reqAssetID := "assetABC"
	ln := &LNInvoiceInput{AssetID: &reqAssetID}
	decodedAssetID := "assetXYZ"
	decoded := &node_client.DecodeRGBInvoiceResponse{AssetID: &decodedAssetID}

	err := applyAndValidateOnchainAssetParams(ln, decoded)
	if err == nil {
		t.Fatal("expected mismatch error for asset_id, got nil")
	}
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
	if err == nil {
		t.Fatal("expected mismatch error for asset_amount, got nil")
	}
}

func TestExtractFungibleAssignmentAmount(t *testing.T) {
	amount, ok := extractFungibleAssignmentAmount(map[string]any{
		"type":  "Fungible",
		"value": "123",
	})
	if !ok || amount != 123 {
		t.Fatalf("expected (123,true), got (%d,%v)", amount, ok)
	}
}
