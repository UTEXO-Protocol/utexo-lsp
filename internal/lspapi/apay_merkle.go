package lspapi

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
)

const (
	apayHashLeafTag  = "UTEXO_APAY_HASH_V1"
	merkleLeafPrefix = byte(0x00)
	merkleNodePrefix = byte(0x01)
	merkleSideLeft   = "left"
	merkleSideRight  = "right"
)

type MerkleProofElement struct {
	Sibling string `json:"sibling"`
	Side    string `json:"side"`
}

type apayLeafInput struct {
	hashIndex   uint64
	paymentHash [32]byte
}

func decodeHash32(s string) ([32]byte, error) {
	var out [32]byte
	b, err := hex.DecodeString(s)
	if err != nil {
		return out, err
	}
	if len(b) != 32 {
		return out, fmt.Errorf("expected 32 bytes, got %d", len(b))
	}
	copy(out[:], b)
	return out, nil
}

func leafHash(recipientPubkey [33]byte, batchID [16]byte, hashIndex uint64, paymentHash [32]byte) [32]byte {
	buf := make([]byte, 0, 1+len(apayHashLeafTag)+33+16+8+32)
	buf = append(buf, merkleLeafPrefix)
	buf = append(buf, apayHashLeafTag...)
	buf = append(buf, recipientPubkey[:]...)
	buf = append(buf, batchID[:]...)
	var idx [8]byte
	binary.BigEndian.PutUint64(idx[:], hashIndex)
	buf = append(buf, idx[:]...)
	buf = append(buf, paymentHash[:]...)
	return sha256.Sum256(buf)
}

func nodeHash(left, right [32]byte) [32]byte {
	buf := make([]byte, 0, 1+32+32)
	buf = append(buf, merkleNodePrefix)
	buf = append(buf, left[:]...)
	buf = append(buf, right[:]...)
	return sha256.Sum256(buf)
}

func nextLevel(level [][32]byte) [][32]byte {
	next := make([][32]byte, 0, (len(level)+1)/2)
	for i := 0; i < len(level); i += 2 {
		left := level[i]
		right := left
		if i+1 < len(level) {
			right = level[i+1]
		}
		next = append(next, nodeHash(left, right))
	}
	return next
}

func apayLeaves(recipientPubkey [33]byte, batchID [16]byte, entries []apayLeafInput) [][32]byte {
	leaves := make([][32]byte, len(entries))
	for i, e := range entries {
		leaves[i] = leafHash(recipientPubkey, batchID, e.hashIndex, e.paymentHash)
	}
	return leaves
}

func merkleRoot(leaves [][32]byte) [32]byte {
	if len(leaves) == 0 {
		panic("merkle root over empty leaf set")
	}
	level := append([][32]byte(nil), leaves...)
	for len(level) > 1 {
		level = nextLevel(level)
	}
	return level[0]
}

func merkleProof(leaves [][32]byte, index int) []MerkleProofElement {
	out := []MerkleProofElement{}
	level := append([][32]byte(nil), leaves...)
	idx := index
	for len(level) > 1 {
		var el MerkleProofElement
		if idx%2 == 0 {
			sib := level[idx]
			if idx+1 < len(level) {
				sib = level[idx+1]
			}
			el = MerkleProofElement{Sibling: hex.EncodeToString(sib[:]), Side: merkleSideRight}
		} else {
			sib := level[idx-1]
			el = MerkleProofElement{Sibling: hex.EncodeToString(sib[:]), Side: merkleSideLeft}
		}
		out = append(out, el)
		level = nextLevel(level)
		idx /= 2
	}
	return out
}

func merkleVerify(leaf [32]byte, proof []MerkleProofElement, root [32]byte) bool {
	current := leaf
	for _, step := range proof {
		sibling, err := decodeHash32(step.Sibling)
		if err != nil {
			return false
		}
		switch step.Side {
		case merkleSideLeft:
			current = nodeHash(sibling, current)
		case merkleSideRight:
			current = nodeHash(current, sibling)
		default:
			return false
		}
	}
	return current == root
}
