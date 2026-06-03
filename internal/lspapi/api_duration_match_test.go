package lspapi

import (
	"testing"
	"time"

	"utexo-lsp/pkg/node_client"
)

func TestAlignAndValidateRGBDurationWithLNAutofill(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	decoded := &node_client.DecodeLNInvoiceResponse{
		Timestamp: now.Unix(),
		ExpirySec: 3600,
	}
	params := &RGBInvoiceInput{}

	if err := alignAndValidateRGBDurationWithLN(params, decoded, now, 5); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if params.DurationSeconds == nil || *params.DurationSeconds != 3600 {
		t.Fatalf("expected duration_seconds=3600, got %v", params.DurationSeconds)
	}
}

func TestAlignAndValidateRGBDurationWithLNRejectsMismatch(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	decoded := &node_client.DecodeLNInvoiceResponse{
		Timestamp: now.Unix(),
		ExpirySec: 3600,
	}
	d := uint32(1200)
	params := &RGBInvoiceInput{DurationSeconds: &d}

	if err := alignAndValidateRGBDurationWithLN(params, decoded, now, 5); err == nil {
		t.Fatal("expected mismatch error")
	}
}

func TestAlignAndValidateRGBDurationWithLNAllowsTolerance(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	decoded := &node_client.DecodeLNInvoiceResponse{
		Timestamp: now.Unix(),
		ExpirySec: 3600,
	}
	d := uint32(3598)
	params := &RGBInvoiceInput{DurationSeconds: &d}

	if err := alignAndValidateRGBDurationWithLN(params, decoded, now, 5); err != nil {
		t.Fatalf("expected within-tolerance success, got %v", err)
	}
}
