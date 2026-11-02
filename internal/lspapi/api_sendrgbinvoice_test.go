package lspapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"utexo-lsp/pkg/node_client"
)

func TestSendRGBByInvoicePreservesAuthenticatedInvoice(t *testing.T) {
	// Opaque invoice data must reach RLN unchanged; reconstructing a recipient
	// map loses the proxy 1.0 public key, encryption preference and expiration.
	const invoice = "rgb:opaque-invoice?pubkey=key&encryption=Required&expiry=2000000000"
	const amount = uint64(9007199254740993) // Must not round through float64.
	for _, status := range []int{http.StatusOK, http.StatusBadRequest} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			sends := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/decodergbinvoice":
					fmt.Fprintf(w, `{"asset_id":"rgb:asset","recipient_type":"Blind","transport_endpoints":["rpc://proxy/json-rpc"],"public_key":"%s","encryption":"Required","assignment":{"type":"Fungible","value":%d}}`, strings.Repeat("ab", 32), amount)
				case "/sendrgbinvoice":
					sends++
					var payload map[string]json.RawMessage
					if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
						t.Error(err)
						return
					}
					var gotInvoice string
					_ = json.Unmarshal(payload["invoice"], &gotInvoice)
					if gotInvoice != invoice || string(payload["amount"]) != fmt.Sprint(amount) || string(payload["asset_id"]) != `"rgb:asset"` {
						t.Errorf("invoice/amount changed: %s", payload)
					}
					if string(payload["donation"]) != "false" || string(payload["fee_rate"]) != "7" || string(payload["min_confirmations"]) != "2" {
						t.Errorf("send policy changed: %s", payload)
					}
					if payload["recipient_map"] != nil || payload["skip_sync"] != nil {
						t.Errorf("legacy fields in invoice send: %s", payload)
					}
					w.WriteHeader(status)
					if status == http.StatusOK {
						fmt.Fprint(w, `{"txid":"tx","batch_transfer_idx":42}`)
					} else {
						fmt.Fprint(w, `{"error":"invoice rejected"}`)
					}
				default:
					t.Errorf("unexpected endpoint (must not fall back): %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			client := node_client.NewClient(server.URL, "", server.Client())
			a := NewAPI(Config{SendRGBFeeRate: 7, MinConfirmations: 2}, nil, client, client)
			err := a.sendRGBByInvoice(context.Background(), invoice)
			if (err != nil) != (status != http.StatusOK) || sends != 1 {
				t.Fatalf("sends=%d err=%v", sends, err)
			}
		})
	}
}

func TestOnchainSendRejectsUndeliverableInvoiceBeforeLNInvoice(t *testing.T) {
	for _, tc := range []struct {
		name       string
		assignment string
		recipient  string
		auth       string
	}{
		{"any", `{"type":"Any"}`, "Blind", ""},
		{"zero", `{"type":"Fungible","value":0}`, "Blind", ""},
		{"negative", `{"type":"Fungible","value":-1}`, "Blind", ""},
		{"fractional", `{"type":"Fungible","value":1.5}`, "Blind", ""},
		{"overflow", `{"type":"Fungible","value":18446744073709551616}`, "Blind", ""},
		{"nonfungible", `{"type":"NonFungible"}`, "Blind", ""},
		{"witness", `{"type":"Fungible","value":1}`, "Witness", ""},
		{"legacy-proxy", `{"type":"Fungible","value":1}`, "Blind", `,"transport_endpoints":["rpc://proxy/json-rpc"]`},
		{"invalid-key", `{"type":"Fungible","value":1}`, "Blind", `,"transport_endpoints":["rpc://proxy/json-rpc"],"public_key":"invalid"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/decodergbinvoice" {
					t.Errorf("must reject before creating or paying invoice: %s", r.URL.Path)
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				fmt.Fprintf(w, `{"asset_id":"rgb:asset","recipient_type":%q,"assignment":%s%s}`, tc.recipient, tc.assignment, tc.auth)
			}))
			defer server.Close()
			client := node_client.NewClient(server.URL, "", server.Client())
			a := NewAPI(Config{HTTPTimeout: time.Second}, nil, client, client)
			w := httptest.NewRecorder()
			a.handleOnchainSend(w, httptest.NewRequest(http.MethodPost, "/onchain_send", strings.NewReader(`{"rgb_invoice":"rgb:invoice"}`)))
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			if err := a.sendRGBByInvoice(context.Background(), "rgb:invoice"); err == nil {
				t.Fatal("delivery must also reject the unsupported invoice")
			}
		})
	}
}

type onchainExpiryStore struct {
	Store
	record OnchainSendRecord
	status string
}

func (s *onchainExpiryStore) ListOnchainPending(context.Context, int) ([]OnchainSendRecord, error) {
	return []OnchainSendRecord{s.record}, nil
}

func (s *onchainExpiryStore) UpdateOnchainStatus(_ context.Context, _ int64, status, _ string) error {
	s.status = status
	return nil
}

func TestOnchainSendDeliversSettledPaymentAfterLNExpiry(t *testing.T) {
	expired := time.Now().Add(-time.Second)
	db := &onchainExpiryStore{record: OnchainSendRecord{
		ID: 1, UserRGBInvoice: "rgb:invoice", LspLNInvoice: "lnbcrt1", LNExpiresAt: &expired,
	}}
	sends := 0
	a := newDeliveryTestAPI(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/invoicestatus":
			fmt.Fprint(w, `{"status":"Succeeded"}`)
		case "/decodergbinvoice":
			fmt.Fprint(w, `{"asset_id":"rgb:asset","recipient_type":"Blind","assignment":{"type":"Fungible","value":1}}`)
		case "/sendrgbinvoice":
			sends++
			fmt.Fprint(w, `{"txid":"tx","batch_transfer_idx":42}`)
		default:
			t.Errorf("unexpected request: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	a.db = db
	if err := a.monitorOnchainSend(context.Background()); err != nil || sends != 1 || db.status != statusCompleted {
		t.Fatalf("status=%s sends=%d err=%v", db.status, sends, err)
	}
}
