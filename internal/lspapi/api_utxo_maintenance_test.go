package lspapi

import (
	"testing"

	"utexo-lsp/pkg/node_client"
)

func TestUtxoMaintenanceEnabledDisabled(t *testing.T) {
	enabled, err := utxoMaintenanceEnabled(0, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if enabled {
		t.Fatalf("expected maintenance disabled")
	}
}

func TestUtxoMaintenanceEnabledValid(t *testing.T) {
	enabled, err := utxoMaintenanceEnabled(3, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !enabled {
		t.Fatalf("expected maintenance enabled")
	}
}

func TestUtxoMaintenanceEnabledInvalidRange(t *testing.T) {
	if _, err := utxoMaintenanceEnabled(5, 5); err == nil {
		t.Fatal("expected error for target<=min")
	}
}

func TestUtxoCreateCountRefillsToTarget(t *testing.T) {
	num, err := utxoCreateCount(2, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if num != 8 {
		t.Fatalf("expected num=8, got %d", num)
	}
}

func TestUtxoCreateCountZeroFree(t *testing.T) {
	num, err := utxoCreateCount(0, 7)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if num != 7 {
		t.Fatalf("expected num=7, got %d", num)
	}
}

func TestUtxoCreateCountAtOrAboveTarget(t *testing.T) {
	num, err := utxoCreateCount(10, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if num != 0 {
		t.Fatalf("expected num=0, got %d", num)
	}
}

func TestCountFreeColorableUtxos(t *testing.T) {
	unspents := []node_client.Unspent{
		// vanilla BTC UTXO: not colorable -> not free
		{UTXO: node_client.UTXO{Outpoint: "a:0", Colorable: false}},
		// empty colorable UTXO -> free
		{UTXO: node_client.UTXO{Outpoint: "b:0", Colorable: true}},
		// asset-occupied colorable UTXO -> not free
		{
			UTXO:           node_client.UTXO{Outpoint: "c:0", Colorable: true},
			RgbAllocations: []node_client.RgbAllocation{{AssetID: "rgb:x", Settled: true}},
		},
		// colorable UTXO with a pending (unsettled) allocation -> not free
		{
			UTXO:           node_client.UTXO{Outpoint: "d:0", Colorable: true},
			RgbAllocations: []node_client.RgbAllocation{{AssetID: "rgb:y", Settled: false}},
		},
		// colorable UTXO reserved by a pending blind receive: empty allocations but
		// pending_blinded > 0 -> not free
		{UTXO: node_client.UTXO{Outpoint: "e:0", Colorable: true}, PendingBlinded: 1},
		// another empty colorable UTXO -> free
		{UTXO: node_client.UTXO{Outpoint: "f:0", Colorable: true}},
	}

	if got := countFreeColorableUtxos(unspents); got != 2 {
		t.Fatalf("expected 2 free colorable utxos, got %d", got)
	}
}

func TestCountFreeColorableUtxosEmpty(t *testing.T) {
	if got := countFreeColorableUtxos(nil); got != 0 {
		t.Fatalf("expected 0, got %d", got)
	}
}
