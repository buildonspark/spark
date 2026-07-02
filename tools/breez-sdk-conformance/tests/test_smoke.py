import breez_sdk_spark as breez


async def test_connect_and_token_balances(alice_sdk):
    """The released SDK connects to the regtest stack and returns coherent token state."""
    info = await alice_sdk.sdk.get_info(request=breez.GetInfoRequest(ensure_synced=True))
    assert info.identity_pubkey
    assert isinstance(info.token_balances, dict)
    # A fresh wallet holds no tokens.
    assert info.token_balances == {}
