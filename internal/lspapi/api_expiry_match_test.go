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

	if err := alignAndValidateLNExpiryWithRGB(ln, decoded, now, 45*time.Second); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ln.ExpirySec != 2655 {
		t.Fatalf("expected expiry_sec to reserve 900+45 seconds, got %d", ln.ExpirySec)
	}
}

func TestAlignAndValidateLNExpiryWithRGBRejectsMismatch(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	exp := now.Unix() + 3600
	ln := &LNInvoiceInput{ExpirySec: 2701}
	decoded := &node_client.DecodeRGBInvoiceResponse{ExpirationTimestamp: &exp}

	if err := alignAndValidateLNExpiryWithRGB(ln, decoded, now, 0); err == nil {
		t.Fatal("expected mismatch error, got nil")
	}
}

func TestAlignAndValidateLNExpiryWithRGBAllowsShorterExpiry(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	exp := now.Unix() + 3600
	ln := &LNInvoiceInput{ExpirySec: 1200}
	decoded := &node_client.DecodeRGBInvoiceResponse{ExpirationTimestamp: &exp}

	if err := alignAndValidateLNExpiryWithRGB(ln, decoded, now, 45*time.Second); err != nil {
		t.Fatalf("expected shorter-expiry success, got %v", err)
	}
}

func TestAlignAndValidateLNExpiryWithRGBRejectsInsufficientReserve(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	for _, remaining := range []int64{0, 900, 944, 945} {
		exp := now.Unix() + remaining
		if err := alignAndValidateLNExpiryWithRGB(&LNInvoiceInput{}, &node_client.DecodeRGBInvoiceResponse{ExpirationTimestamp: &exp}, now, 45*time.Second); err == nil {
			t.Errorf("accepted invoice with only %d seconds remaining", remaining)
		}
	}
}
