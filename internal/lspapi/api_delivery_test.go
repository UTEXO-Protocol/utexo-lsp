package lspapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"utexo-lsp/pkg/node_client"
)

// newDeliveryTestAPI wires an API against a stub node.
func newDeliveryTestAPI(t *testing.T, cfg Config, handler http.HandlerFunc) *API {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	client := node_client.NewClient(srv.URL, "", srv.Client())
	return &API{cfg: cfg, lspClient: client, rgbClient: client}
}

func channelsHandler(channels ...node_client.Channel) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/listchannels":
			_ = json.NewEncoder(w).Encode(node_client.ListChannelsResponse{Channels: channels})
		default:
			http.NotFound(w, r)
		}
	}
}

func strptr(s string) *string { return &s }

// A 200 carrying status "Failed" is a failure — the shape RLN returns on a
// routing error.
func TestSendLNByInvoiceRejectsFailedStatus(t *testing.T) {
	api := newDeliveryTestAPI(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(node_client.SendPaymentResponse{
			PaymentHash: "abc123",
			Status:      node_client.PaymentStatusFailed,
		})
	})

	hash, err := api.sendLNByInvoice(context.Background(), "lnbcrt1")
	if err == nil {
		t.Fatal("expected an error for a payment the node reported as Failed")
	}
	if hash != "abc123" {
		t.Fatalf("expected the payment hash to be reported for retries, got %q", hash)
	}
	// The sentinel is what lets APay keep its old behaviour; losing it would make
	// the APay outbox retry forever.
	if !errors.Is(err, errPaymentReportedFailed) {
		t.Fatalf("a node-reported failure must be distinguishable from a transport error, got %v", err)
	}
}

// APay retries transport errors but tolerates node-reported failures, so the two
// must stay distinguishable.
func TestSendLNByInvoiceTransportErrorIsNotReportedFailure(t *testing.T) {
	api := newDeliveryTestAPI(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})

	_, err := api.sendLNByInvoice(context.Background(), "lnbcrt1")
	if err == nil {
		t.Fatal("expected an error for a 500 response")
	}
	if errors.Is(err, errPaymentReportedFailed) {
		t.Fatal("a transport error must not be classified as a node-reported failure")
	}
}

func TestSendLNByInvoiceAcceptsPending(t *testing.T) {
	api := newDeliveryTestAPI(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(node_client.SendPaymentResponse{
			PaymentHash: "abc123",
			Status:      node_client.PaymentStatusPending,
		})
	})

	hash, err := api.sendLNByInvoice(context.Background(), "lnbcrt1")
	if err != nil {
		t.Fatalf("pending means initiated, not failed: %v", err)
	}
	if hash != "abc123" {
		t.Fatalf("unexpected payment hash %q", hash)
	}
}

func TestCanDeliverNow(t *testing.T) {
	const payee = "02payee"
	const assetID = "rgb:asset"

	invoice := &node_client.DecodeLNInvoiceResponse{
		AmtMsat:     3_000_000,
		AssetID:     assetID,
		AssetAmount: 2,
		PayeePubkey: payee,
	}

	usable := node_client.Channel{
		PeerPubkey:                payee,
		AssetID:                   strptr(assetID),
		IsUsable:                  true,
		CapacitySat:               100_000,
		AssetLocalAmount:          50,
		NextOutboundHTLCLimitMsat: 10_000_000,
	}

	tests := []struct {
		name     string
		channels []node_client.Channel
		want     bool
	}{
		{"no channel at all", nil, false},
		{"channel not usable yet", []node_client.Channel{func() node_client.Channel {
			c := usable
			c.IsUsable = false
			return c
		}()}, false},
		{"not enough asset units", []node_client.Channel{func() node_client.Channel {
			c := usable
			c.AssetLocalAmount = 1
			return c
		}()}, false},
		{"amount over the per-HTLC ceiling", []node_client.Channel{func() node_client.Channel {
			c := usable
			c.NextOutboundHTLCLimitMsat = 1_000_000
			return c
		}()}, false},
		{"wrong asset", []node_client.Channel{func() node_client.Channel {
			c := usable
			c.AssetID = strptr("rgb:other")
			return c
		}()}, false},
		{"deliverable", []node_client.Channel{usable}, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			api := newDeliveryTestAPI(t, Config{}, channelsHandler(tc.channels...))
			reason, ok := api.canDeliverNow(context.Background(), invoice)
			if ok != tc.want {
				t.Fatalf("canDeliverNow = %v (%s), want %v", ok, reason, tc.want)
			}
			if !ok && reason == "" {
				t.Fatal("a refusal must explain itself — the reason lands in last_error")
			}
		})
	}
}

// A large balance must not make an over-ceiling payment look deliverable — that
// mismatch stranded 30_000 sat on signet.
func TestCanDeliverNowIgnoresBalance(t *testing.T) {
	const payee = "02payee"
	invoice := &node_client.DecodeLNInvoiceResponse{AmtMsat: 30_000_000, PayeePubkey: payee}

	api := newDeliveryTestAPI(t, Config{}, channelsHandler(node_client.Channel{
		PeerPubkey:                payee,
		IsUsable:                  true,
		CapacitySat:               100_000,
		OutboundBalanceMsat:       93_340_000,
		NextOutboundHTLCLimitMsat: 10_000_000,
	}))

	if _, ok := api.canDeliverNow(context.Background(), invoice); ok {
		t.Fatal("30_000_000 msat must not pass a 10_000_000 msat per-HTLC ceiling")
	}
}

func TestValidateDeliverableAmounts(t *testing.T) {
	cfg := Config{
		DefaultChannelCapacitySat: 100_000,
		DefaultChannelAssetAmount: 50,
		PeerInFlightPercent:       10, // → 10_000_000 msat per payment
	}

	tests := []struct {
		name    string
		invoice *node_client.DecodeLNInvoiceResponse
		wantErr bool
	}{
		{"within both ceilings", &node_client.DecodeLNInvoiceResponse{AmtMsat: 5_000_000, AssetAmount: 10}, false},
		{"asset over channel size", &node_client.DecodeLNInvoiceResponse{AmtMsat: 5_000_000, AssetAmount: 100}, true},
		{"sats over per-HTLC ceiling", &node_client.DecodeLNInvoiceResponse{AmtMsat: 30_000_000, AssetAmount: 10}, true},
		{"exactly at the ceilings", &node_client.DecodeLNInvoiceResponse{AmtMsat: 10_000_000, AssetAmount: 50}, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			api := newDeliveryTestAPI(t, cfg, channelsHandler())
			err := api.validateDeliverableAmounts(context.Background(), tc.invoice)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateDeliverableAmounts err = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

// An existing channel's negotiated limits win over the config estimate.
func TestValidateDeliverableAmountsUsesExistingChannel(t *testing.T) {
	cfg := Config{
		DefaultChannelCapacitySat: 100_000,
		DefaultChannelAssetAmount: 50,
		PeerInFlightPercent:       10,
	}
	const payee = "02payee"

	api := newDeliveryTestAPI(t, cfg, channelsHandler(node_client.Channel{
		PeerPubkey:                payee,
		IsUsable:                  true,
		AssetLocalAmount:          200,
		NextOutboundHTLCLimitMsat: 50_000_000,
	}))

	err := api.validateDeliverableAmounts(context.Background(), &node_client.DecodeLNInvoiceResponse{
		AmtMsat:     30_000_000,
		AssetAmount: 100,
		PayeePubkey: payee,
	})
	if err != nil {
		t.Fatalf("a wider existing channel must be honoured: %v", err)
	}
}

func TestDeliveryBackoff(t *testing.T) {
	api := &API{cfg: Config{
		DeliveryRetryBaseDelay: 30 * time.Second,
		DeliveryRetryMaxDelay:  5 * time.Minute,
	}}

	want := []time.Duration{
		30 * time.Second,
		30 * time.Second,
		time.Minute,
		2 * time.Minute,
		4 * time.Minute,
		5 * time.Minute,
		5 * time.Minute,
	}
	for attempts, expected := range want {
		if got := api.deliveryBackoff(int64(attempts)); got != expected {
			t.Fatalf("deliveryBackoff(%d) = %v, want %v", attempts, got, expected)
		}
	}
}
