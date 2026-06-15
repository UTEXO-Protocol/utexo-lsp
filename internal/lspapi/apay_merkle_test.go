package lspapi

import (
	"encoding/hex"
	"testing"
)

func apayMerkleFixture() [][32]byte {
	var rpk [33]byte
	rpk[0] = 0x02
	for i := 1; i < 33; i++ {
		rpk[i] = 0x11
	}
	var bid [16]byte
	for i := range 16 {
		bid[i] = byte(i)
	}
	entries := make([]apayLeafInput, 0, 5)
	for i := uint64(1); i <= 5; i++ {
		var ph [32]byte
		for j := range ph {
			ph[j] = byte(i)
		}
		entries = append(entries, apayLeafInput{hashIndex: i, paymentHash: ph})
	}
	return apayLeaves(rpk, bid, entries)
}

func TestApayMerkleReferenceVector(t *testing.T) {
	leaves := apayMerkleFixture()
	if len(leaves) != 5 {
		t.Fatalf("expected 5 leaves, got %d", len(leaves))
	}
	if got := hex.EncodeToString(leaves[0][:]); got != "eabd11aa1b47ea65fa1c5a7cd7992dcaf9f0725ced14eabfebd20a697e8c08a9" {
		t.Fatalf("leaf0 = %s", got)
	}
	if got := hex.EncodeToString(leaves[4][:]); got != "2a583c0c4ee4b0d8d985970116b8035830556eac01a123c0151f8d8dd9fa83fe" {
		t.Fatalf("leaf4 = %s", got)
	}
	root := merkleRoot(leaves)
	if got := hex.EncodeToString(root[:]); got != "2a89ca7e910bae70bc3f03f0252ec22de83cc40a5b4ba1582b649be5ea667132" {
		t.Fatalf("root = %s", got)
	}
}

func TestApayMerkleProofRoundtrip(t *testing.T) {
	leaves := apayMerkleFixture()
	root := merkleRoot(leaves)

	p0 := merkleProof(leaves, 0)
	if len(p0) != 3 {
		t.Fatalf("proof0 len = %d", len(p0))
	}
	if p0[0].Sibling != "7659a406e2a812a5ed733169fdccf6c02cef92e078c93c11a5332aa654061847" || p0[0].Side != merkleSideRight {
		t.Fatalf("proof0[0] = %+v", p0[0])
	}
	if !merkleVerify(leaves[0], p0, root) {
		t.Fatal("verify idx0 failed")
	}

	p4 := merkleProof(leaves, 4)
	if !merkleVerify(leaves[4], p4, root) {
		t.Fatal("verify idx4 failed")
	}
	if p4[0].Sibling != hex.EncodeToString(leaves[4][:]) || p4[0].Side != merkleSideRight {
		t.Fatalf("proof4[0] = %+v", p4[0])
	}
	if p4[2].Side != merkleSideLeft {
		t.Fatalf("proof4[2].side = %s", p4[2].Side)
	}

	bad := leaves[0]
	bad[0] ^= 0x01
	if merkleVerify(bad, p0, root) {
		t.Fatal("tampered leaf verified")
	}
}

func TestApayMerkleSingleLeaf(t *testing.T) {
	leaves := apayMerkleFixture()[:1]
	if merkleRoot(leaves) != leaves[0] {
		t.Fatal("single-leaf root must equal the leaf")
	}
	if len(merkleProof(leaves, 0)) != 0 {
		t.Fatal("single-leaf proof must be empty")
	}
}
