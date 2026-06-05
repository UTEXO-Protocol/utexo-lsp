package lspapi

import (
	"testing"
	"time"

	"utexo-lsp/pkg/node_client"
)

func TestAlignAndValidateLNExpiryWithRGBAutofill(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	exp := now.Unix() + 3600
	ln := &LNInvoiceInput{}
	decoded := &node_client.DecodeRGBInvoiceResponse{ExpirationTimestamp: &exp}

	if err := alignAndValidateLNExpiryWithRGB(ln, decoded, now, 5); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ln.ExpirySec != 3600 {
		t.Fatalf("expected expiry_sec to be autofilled with 3600, got %d", ln.ExpirySec)
	}
}

func TestAlignAndValidateLNExpiryWithRGBRejectsMismatch(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	exp := now.Unix() + 3600
	ln := &LNInvoiceInput{ExpirySec: 1200}
	decoded := &node_client.DecodeRGBInvoiceResponse{ExpirationTimestamp: &exp}

	if err := alignAndValidateLNExpiryWithRGB(ln, decoded, now, 5); err == nil {
		t.Fatal("expected mismatch error, got nil")
	}
}

func TestAlignAndValidateLNExpiryWithRGBAllowsTolerance(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	exp := now.Unix() + 3600
	ln := &LNInvoiceInput{ExpirySec: 3598}
	decoded := &node_client.DecodeRGBInvoiceResponse{ExpirationTimestamp: &exp}

	if err := alignAndValidateLNExpiryWithRGB(ln, decoded, now, 5); err != nil {
		t.Fatalf("expected within-tolerance success, got %v", err)
	}
}
