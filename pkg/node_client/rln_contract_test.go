package node_client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Wire contracts checked against RLN v0.13.0-beta.3 (af03c7f) src/routes.rs.
func TestRLNTransferFilter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		filter, ok := payload["asset_filter"].(map[string]any)
		if r.URL.Path != "/listtransfers" || !ok || filter["type"] != "Id" || filter["value"] != "rgb:asset" || payload["asset_id"] != nil {
			t.Errorf("unexpected payload: %v", payload)
		}
		w.Write([]byte(`{"transfers":[{"idx":42,"kind":"ReceiveBlind","status":"Settled"}],"first_index_offset":0,"last_index_offset":0}`))
	}))
	defer server.Close()
	result, err := NewClient(server.URL, "", server.Client()).ListTransfers(context.Background(), ListTransfersRequest{AssetID: "rgb:asset"})
	if err != nil || len(result.Transfers) != 1 || result.Transfers[0].Status != TransferStatusSettled || result.Transfers[0].Idx != 42 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestRLNRGBInvoice(t *testing.T) {
	for _, endpoints := range [][]string{nil, {"rpc://shared:3000/json-rpc"}} {
		t.Run("transport", func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var payload map[string]any
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
				}
				transport, ok := payload["transport_endpoints"].([]any)
				if !ok || len(transport) != len(endpoints) {
					t.Errorf("transport must be an array: %v", payload)
				}
				if len(endpoints) > 0 && transport[0] != endpoints[0] {
					t.Errorf("endpoint lost: %v", transport)
				}
				if _, exists := payload["expiration_timestamp"]; exists {
					t.Error("nil expiry must be omitted, not null")
				}
				w.Write([]byte(`{"recipient_id":"recipient","invoice":"rgb:invoice","expiration_timestamp":1800000000,"batch_transfer_idx":7}`))
			}))
			defer server.Close()
			result, err := NewClient(server.URL, "", server.Client()).RGBInvoice(context.Background(), RGBInvoiceRequest{TransportEndpoints: endpoints})
			if err != nil || result.ExpirationTimestamp == nil || *result.ExpirationTimestamp != 1800000000 || result.BatchTransferIdx != 7 {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}

func TestRLNChannelDecode(t *testing.T) {
	var result ListChannelsResponse
	err := json.Unmarshal([]byte(`{"channels":[{"channel_id":"id","peer_pubkey":"peer","asset_id":"rgb:asset","status":"Opened","ready":true,"is_usable":true,"funding_txid":"tx","asset_local_amount":12,"asset_remote_amount":8,"outbound_balance_msat":1000000}]}`), &result)
	if err != nil || len(result.Channels) != 1 {
		t.Fatalf("decode: %v", err)
	}
	c := result.Channels[0]
	if !c.Ready || !c.IsUsable || c.Status != "Opened" || c.AssetID == nil || *c.AssetID != "rgb:asset" || c.AssetLocalAmount != 12 || c.AssetRemoteAmount != 8 {
		t.Fatalf("channel: %+v", c)
	}
}
