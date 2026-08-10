from __future__ import annotations


def test_lightning_address_set_and_callback(env):
    # 1. Get user A's pubkey
    pubkey_a = env.user_a.nodeinfo()["pubkey"]
    assert pubkey_a is not None

    # 2. Register custom handle 'alice_e2e_...' for user A
    import secrets
    username = f"alice_e2e_{secrets.token_hex(4)}"
    sig = env.user_a.sign_message(username)
    reg_resp = env.lsp_api.set_lightning_address(pubkey=pubkey_a, username=username, signature=sig)
    assert reg_resp["username"] == username
    assert reg_resp["domain"] is not None

    # 3. Query back by pubkey
    by_pk_resp = env.lsp_api.get_lightning_address_by_pubkey(pubkey_a)
    assert by_pk_resp["username"] == username

    # 4. Perform LNURL-pay discovery
    discovery_resp = env.lsp_api.lnurlp_discovery(username)
    assert discovery_resp["tag"] == "payRequest"
    assert discovery_resp["recipient_pubkey"] == pubkey_a

    # 5. Seed order hashes for user A so LNURL-pay callback has payment hashes in its pool
    import secrets
    import hashlib
    preimage = secrets.token_bytes(32)
    payment_hash = hashlib.sha256(preimage).hexdigest()

    order_payload = {
        "peer_pubkey": pubkey_a,
        "protocol_version": 1,
        "hashes": [
            {
                "hash_index": "1",
                "payment_hash": payment_hash
            }
        ]
    }
    
    order_resp = env.lsp_api.post_internal_async_order_new(order_payload)
    assert "error" not in order_resp

    # 6. Fetch a BOLT11 invoice via LNURL-pay callback
    callback_resp = env.lsp_api.lnurlp_callback(username, 3000000)
    assert "pr" in callback_resp
    bolt11 = callback_resp["pr"]
    assert bolt11.startswith("lnbc")
