package lspapi

import (
	"context"
	"encoding/hex"
	"path/filepath"
	"strings"
	"testing"
)

func newApayTestStore(t *testing.T) *SQLStore {
	t.Helper()
	store, err := NewStore(Config{
		DatabaseDriver: "sqlite",
		DatabaseURL:    filepath.Join(t.TempDir(), "apay-batch.db"),
	})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func mustDecodePubkey33(t *testing.T, s string) [33]byte {
	t.Helper()
	v, err := decodePubkey33(s)
	if err != nil {
		t.Fatalf("decode pubkey %q: %v", s, err)
	}
	return v
}

func mustDecodeBatchID16(t *testing.T, s string) [16]byte {
	t.Helper()
	v, err := decodeBatchId16(s)
	if err != nil {
		t.Fatalf("decode batch_id %q: %v", s, err)
	}
	return v
}

func mustDecodeHash32(t *testing.T, s string) [32]byte {
	t.Helper()
	v, err := decodeHash32(s)
	if err != nil {
		t.Fatalf("decode hash %q: %v", s, err)
	}
	return v
}

func buildTestBatch(t *testing.T, recipientPubkey, hostPubkey, batchID string, hashes []AsyncOrderNewHashInput) *ApayBatchCommitment {
	t.Helper()
	rp := mustDecodePubkey33(t, recipientPubkey)
	bid := mustDecodeBatchID16(t, batchID)
	entries := make([]apayLeafInput, 0, len(hashes))
	for _, h := range hashes {
		idx, err := parseHashIndex(h.HashIndex)
		if err != nil {
			t.Fatalf("parse hash_index %q: %v", h.HashIndex, err)
		}
		entries = append(entries, apayLeafInput{hashIndex: idx, paymentHash: mustDecodeHash32(t, h.PaymentHash)})
	}
	root := merkleRoot(apayLeaves(rp, bid, entries))
	return &ApayBatchCommitment{
		HostPubkey: hostPubkey,
		BatchID:    batchID,
		BatchRoot:  hex.EncodeToString(root[:]),
		BatchSize:  uint64(len(entries)),
		BatchSig:   strings.Repeat("00", 64),
		CreatedAt:  1700000000,
		ExpiresAt:  0,
	}
}

func parseHashIndex(s string) (uint64, error) {
	var n uint64
	for _, c := range strings.TrimSpace(s) {
		n = n*10 + uint64(c-'0')
	}
	return n, nil
}

func testHashes() []AsyncOrderNewHashInput {
	return []AsyncOrderNewHashInput{
		{HashIndex: "1", PaymentHash: strings.Repeat("a1", 32)},
		{HashIndex: "2", PaymentHash: strings.Repeat("b2", 32)},
		{HashIndex: "3", PaymentHash: strings.Repeat("c3", 32)},
	}
}

const (
	testRecipientPubkey = "02" + "1111111111111111111111111111111111111111111111111111111111111111"
	testHostPubkey      = "03" + "2222222222222222222222222222222222222222222222222222222222222222"
	testBatchID         = "000102030405060708090a0b0c0d0e0f"
)

func TestApplyAsyncOrderNewStoresBatchAndBuildsProof(t *testing.T) {
	store := newApayTestStore(t)
	ctx := context.Background()
	if _, err := store.InsertLightningAddressAccount(ctx, LightningAddressAccount{
		PeerPubkey: testRecipientPubkey,
		Username:   "alice",
	}); err != nil {
		t.Fatalf("insert account: %v", err)
	}
	hashes := testHashes()
	batch := buildTestBatch(t, testRecipientPubkey, testHostPubkey, testBatchID, hashes)
	addrSig := "firmadigital"

	resp, rpcErr, err := store.ApplyAsyncOrderNew(ctx, AsyncOrderNewRequest{
		PeerPubkey:      testRecipientPubkey,
		ProtocolVersion: asyncOrderProtocolVersion,
		Hashes:          hashes,
		Batch:           batch,
		AddressSig:      &addrSig,
	})
	if err != nil {
		t.Fatalf("apply async order new: %v", err)
	}
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	if resp.OrderID != "1" {
		t.Fatalf("order id = %q", resp.OrderID)
	}

	proof, err := store.BuildApayInvoiceProof(ctx, 1, 2)
	if err != nil {
		t.Fatalf("build proof: %v", err)
	}
	if proof == nil {
		t.Fatal("expected a proof")
	}
	if proof.BatchID != testBatchID {
		t.Fatalf("batch_id = %q", proof.BatchID)
	}
	if proof.PaymentHash != hashes[1].PaymentHash {
		t.Fatalf("payment_hash = %q", proof.PaymentHash)
	}
	if proof.BatchRoot != batch.BatchRoot {
		t.Fatalf("batch_root = %q want %q", proof.BatchRoot, batch.BatchRoot)
	}
	if proof.RecipientPubkey != testRecipientPubkey || proof.HostPubkey != testHostPubkey {
		t.Fatalf("pubkeys: recipient=%q host=%q", proof.RecipientPubkey, proof.HostPubkey)
	}
	if proof.BatchSize != 3 || proof.HashIndex != 2 {
		t.Fatalf("batch_size=%d hash_index=%d", proof.BatchSize, proof.HashIndex)
	}

	rpk := mustDecodePubkey33(t, testRecipientPubkey)
	bid := mustDecodeBatchID16(t, testBatchID)
	ph := mustDecodeHash32(t, hashes[1].PaymentHash)
	leaf := leafHash(rpk, bid, 2, ph)
	root := mustDecodeHash32(t, proof.BatchRoot)
	if !merkleVerify(leaf, proof.MerkleProof, root) {
		t.Fatal("merkle proof did not verify")
	}

	sig, err := store.GetApayAddressAttestation(ctx, testRecipientPubkey)
	if err != nil {
		t.Fatalf("get attestation: %v", err)
	}
	if sig == nil || *sig != addrSig {
		t.Fatalf("address_sig = %v", sig)
	}
}

func TestApplyAsyncOrderNewRejectsBadBatchRoot(t *testing.T) {
	store := newApayTestStore(t)
	ctx := context.Background()
	hashes := testHashes()
	batch := buildTestBatch(t, testRecipientPubkey, testHostPubkey, testBatchID, hashes)
	batch.BatchRoot = strings.Repeat("ff", 32)

	_, rpcErr, err := store.ApplyAsyncOrderNew(ctx, AsyncOrderNewRequest{
		PeerPubkey:      testRecipientPubkey,
		ProtocolVersion: asyncOrderProtocolVersion,
		Hashes:          hashes,
		Batch:           batch,
	})
	if err != nil {
		t.Fatalf("apply async order new: %v", err)
	}
	if rpcErr == nil {
		t.Fatal("expected an rpc error for a mismatched batch_root")
	}
}

func TestBuildApayInvoiceProofNilWithoutBatch(t *testing.T) {
	store := newApayTestStore(t)
	ctx := context.Background()
	hashes := testHashes()

	if _, _, err := store.ApplyAsyncOrderNew(ctx, AsyncOrderNewRequest{
		PeerPubkey:      testRecipientPubkey,
		ProtocolVersion: asyncOrderProtocolVersion,
		Hashes:          hashes,
	}); err != nil {
		t.Fatalf("apply async order new: %v", err)
	}
	proof, err := store.BuildApayInvoiceProof(ctx, 1, 1)
	if err != nil {
		t.Fatalf("build proof: %v", err)
	}
	if proof != nil {
		t.Fatalf("expected nil proof for legacy entry, got %+v", proof)
	}
}
