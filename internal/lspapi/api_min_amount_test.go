package lspapi

import (
	"testing"

	"utexo-lsp/pkg/node_client"
)

func TestEnsureLNInvoiceInputMinAmount(t *testing.T) {
	min := uint64(3_000_000)

	ln := &LNInvoiceInput{}
	if err := ensureLNInvoiceInputMinAmount(ln, min); err != nil {
		t.Fatalf("unexpected error for nil amt_msat: %v", err)
	}
	if ln.AmtMsat == nil || *ln.AmtMsat != min {
		t.Fatalf("expected amt_msat autofilled to %d, got %v", min, ln.AmtMsat)
	}

	tooLow := uint64(1000)
	ln2 := &LNInvoiceInput{AmtMsat: &tooLow}
	if err := ensureLNInvoiceInputMinAmount(ln2, min); err == nil {
		t.Fatal("expected error for too-low amt_msat")
	}
}

func TestEnsureDecodedLNMinAmount(t *testing.T) {
	min := uint64(3_000_000)

	if err := ensureDecodedLNMinAmount(&node_client.DecodeLNInvoiceResponse{}, min); err == nil {
		t.Fatal("expected error for amountless invoice")
	}

	tooLow := int64(1000)
	if err := ensureDecodedLNMinAmount(&node_client.DecodeLNInvoiceResponse{AmtMsat: tooLow}, min); err == nil {
		t.Fatal("expected error for too-low decoded amount")
	}

	ok := int64(3_000_000)
	if err := ensureDecodedLNMinAmount(&node_client.DecodeLNInvoiceResponse{AmtMsat: ok}, min); err != nil {
		t.Fatalf("unexpected error for valid amount: %v", err)
	}
}
