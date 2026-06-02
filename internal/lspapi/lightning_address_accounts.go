package lspapi

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	mrand "math/rand"
	"strings"

	"github.com/btcsuite/btcd/btcec/v2"
)

const lightningAddressAccountRetryLimit = 128

func parseClientPubkey(raw string) (string, error) {
	decoded, err := hex.DecodeString(strings.TrimSpace(raw))
	if err != nil {
		return "", errors.New("client pubkey must be hex encoded")
	}
	if len(decoded) != btcec.PubKeyBytesLenCompressed || !btcec.IsCompressedPubKey(decoded) {
		return "", errors.New("client pubkey must use compressed encoding")
	}

	pubkey, err := btcec.ParsePubKey(decoded)
	if err != nil {
		return "", errors.New("client pubkey is not a valid secp256k1 point")
	}

	return hex.EncodeToString(pubkey.SerializeCompressed()), nil
}

func normalizeLightningAddressHandle(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

func normalizePeerPubkey(peerPubkey string) string {
	return strings.ToLower(strings.TrimSpace(peerOnly(peerPubkey)))
}

func mintLightningAddressHandle() string {
	return strings.ToLower(randomHaikuHandle())
}

var lightningAddressAdjectives = []string{
	"amber", "brisk", "calm", "daring", "eager", "frosty", "gentle", "hidden",
	"icy", "jolly", "kind", "lucky", "mellow", "nimble", "opal", "proud",
	"quick", "rusty", "silent", "tidy", "urban", "vivid", "wild", "young",
}

var lightningAddressNouns = []string{
	"aurora", "beacon", "comet", "delta", "ember", "forest", "glider", "harbor",
	"island", "jungle", "kernel", "lagoon", "meadow", "nebula", "oasis", "prairie",
	"quartz", "river", "summit", "thunder", "uplink", "valley", "whisper", "zephyr",
}

func randomHaikuHandle() string {
	seed := make([]byte, 8)
	if _, err := rand.Read(seed); err != nil {
		return fmt.Sprintf("user-%d", mrand.Uint32())
	}
	r := mrand.New(mrand.NewSource(int64(binary.LittleEndian.Uint64(seed))))
	adj := lightningAddressAdjectives[r.Intn(len(lightningAddressAdjectives))]
	noun := lightningAddressNouns[r.Intn(len(lightningAddressNouns))]
	suffix := r.Intn(10000)
	return fmt.Sprintf("%s-%s-%04d", adj, noun, suffix)
}

func (a *API) ensureLightningAddressAccount(ctx context.Context, peerPubkey string) (LightningAddressAccount, error) {
	peerPubkey = normalizePeerPubkey(peerPubkey)
	if peerPubkey == "" {
		return LightningAddressAccount{}, errors.New("empty peer_pubkey")
	}

	if a.db == nil {
		return LightningAddressAccount{}, errors.New("lightning address database is not configured")
	}

	existing, err := a.db.GetLightningAddressAccountByPeerPubkey(ctx, peerPubkey)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, errLightningAddressAccountNotFound) {
		return LightningAddressAccount{}, err
	}

	for range [lightningAddressAccountRetryLimit]struct{}{} {
		handle := mintLightningAddressHandle()
		account := LightningAddressAccount{
			PeerPubkey: peerPubkey,
			Username:   handle,
		}

		inserted, err := a.db.InsertLightningAddressAccount(ctx, account)
		if err != nil {
			return LightningAddressAccount{}, err
		}

		if inserted {
			return a.db.GetLightningAddressAccountByPeerPubkey(ctx, peerPubkey)
		}

		existing, err := a.db.GetLightningAddressAccountByPeerPubkey(ctx, peerPubkey)
		if err == nil {
			return existing, nil
		}
		if !errors.Is(err, errLightningAddressAccountNotFound) {
			return LightningAddressAccount{}, err
		}
	}

	return LightningAddressAccount{}, fmt.Errorf("unable to allocate lightning address for peer_pubkey %s", peerPubkey)
}

func (a *API) lightningAddressAccount(ctx context.Context, rawHandle string) (LightningAddressAccount, bool, error) {
	handle := normalizeLightningAddressHandle(rawHandle)
	if handle == "" {
		return LightningAddressAccount{}, false, nil
	}

	if a.db == nil {
		return LightningAddressAccount{}, false, errors.New("lightning address database is not configured")
	}

	account, err := a.db.GetLightningAddressAccountByUsername(ctx, handle)
	switch {
	case err == nil:
		return account, true, nil
	case errors.Is(err, errLightningAddressAccountNotFound):
		return LightningAddressAccount{}, false, nil
	default:
		return LightningAddressAccount{}, false, err
	}
}

func (a *API) lightningAddressAccountByPubkey(ctx context.Context, rawPubkey string) (LightningAddressAccount, bool, error) {
	peerPubkey := normalizePeerPubkey(rawPubkey)
	if peerPubkey == "" {
		return LightningAddressAccount{}, false, nil
	}

	if a.db == nil {
		return LightningAddressAccount{}, false, errors.New("lightning address database is not configured")
	}

	account, err := a.db.GetLightningAddressAccountByPeerPubkey(ctx, peerPubkey)
	switch {
	case err == nil:
		if normalizePeerPubkey(account.PeerPubkey) != peerPubkey {
			return LightningAddressAccount{}, false, errors.New("store returned a different peer_pubkey than requested")
		}
		return account, true, nil
	case errors.Is(err, errLightningAddressAccountNotFound):
		return LightningAddressAccount{}, false, nil
	default:
		return LightningAddressAccount{}, false, err
	}
}
