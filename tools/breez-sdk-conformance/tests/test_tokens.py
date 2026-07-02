"""Token conformance flows exercised through the public breez-sdk-spark API only.

Each test issues its own token on the regtest stack (a fresh wallet can create and mint without
external funding), so tests are independent and need no faucet or pre-provisioned issuer.
"""

import asyncio

import breez_sdk_spark as breez

from breez_conformance.tokens import (
    create_and_mint_token,
    new_token_invoice,
    send_tokens,
    token_balance,
    wait_for_token_balance,
)
from breez_conformance.wallet import new_spark_address


async def test_create_and_mint_token(alice_sdk):
    """Creating a token and minting supply reflects in issuer and wallet balances + metadata."""
    token_id, metadata = await create_and_mint_token(
        alice_sdk,
        name="Conformance USD",
        ticker="CUSD",
        decimals=6,
        is_freezable=True,
        max_supply=1_000_000_000,
        mint_amount=500_000,
    )

    assert token_id.startswith("btkn")
    assert metadata.identifier == token_id
    assert metadata.name == "Conformance USD"
    assert metadata.ticker == "CUSD"
    assert metadata.decimals == 6
    assert metadata.max_supply == 1_000_000_000
    assert metadata.is_freezable is True
    assert metadata.issuer_public_key

    # Wallet-facing balance reflects the mint.
    info = await alice_sdk.sdk.get_info(request=breez.GetInfoRequest(ensure_synced=True))
    assert info.token_balances[token_id].balance == 500_000

    # Issuer-facing views agree.
    issuer = alice_sdk.sdk.get_token_issuer()
    issuer_balance = await issuer.get_issuer_token_balance()
    assert issuer_balance.balance == 500_000
    assert issuer_balance.token_metadata.identifier == token_id
    issuer_metadata = await issuer.get_issuer_token_metadata()
    assert issuer_metadata.identifier == token_id


async def test_get_tokens_metadata(alice_sdk, funded_token):
    """Metadata for a known token identifier is retrievable and matches issuance."""
    token_id, metadata = funded_token
    resp = await alice_sdk.sdk.get_tokens_metadata(
        request=breez.GetTokensMetadataRequest(token_identifiers=[token_id])
    )
    assert len(resp.tokens_metadata) == 1
    fetched = resp.tokens_metadata[0]
    assert fetched.identifier == token_id
    assert fetched.ticker == metadata.ticker
    assert fetched.decimals == metadata.decimals


async def test_transfer_by_spark_address(alice_sdk, bob_sdk, funded_token):
    """Alice sends tokens to Bob's spark address; balances move by the transferred amount."""
    token_id, _ = funded_token
    alice_before = await token_balance(alice_sdk.sdk, token_id)

    bob_address = await new_spark_address(bob_sdk)
    payment = await send_tokens(alice_sdk, bob_address, amount=120_000, token_identifier=token_id)

    assert payment.status == breez.PaymentStatus.COMPLETED
    assert isinstance(payment.details, breez.PaymentDetails.TOKEN)
    assert payment.details.metadata.identifier == token_id

    assert await wait_for_token_balance(bob_sdk.sdk, token_id, 120_000) == 120_000
    assert await token_balance(alice_sdk.sdk, token_id) == alice_before - 120_000


async def test_transfer_by_spark_invoice(alice_sdk, bob_sdk, funded_token):
    """Bob raises a token spark invoice; Alice pays it and Bob's balance rises by the amount."""
    token_id, _ = funded_token

    invoice = await new_token_invoice(bob_sdk, token_id, amount=50_000, description="conformance")
    payment = await send_tokens(alice_sdk, invoice)

    assert payment.status == breez.PaymentStatus.COMPLETED
    assert isinstance(payment.details, breez.PaymentDetails.TOKEN)
    assert await wait_for_token_balance(bob_sdk.sdk, token_id, 50_000) == 50_000


async def test_burn_reduces_issuer_balance(alice_sdk, funded_token):
    """Burning issuer supply reduces the issuer's token balance by the burned amount."""
    token_id, _ = funded_token
    before = await token_balance(alice_sdk.sdk, token_id)

    issuer = alice_sdk.sdk.get_token_issuer()
    await issuer.burn_issuer_token(request=breez.BurnIssuerTokenRequest(amount=10_000))

    expected = before - 10_000
    current = before
    for _ in range(30):
        current = await token_balance(alice_sdk.sdk, token_id)
        if current == expected:
            break
        await asyncio.sleep(2)
    assert current == expected


async def test_freeze_and_unfreeze(alice_sdk, bob_sdk, funded_token):
    """The issuer can freeze and unfreeze a holder's tokens; the impacted amount is reported."""
    token_id, _ = funded_token

    bob_address = await new_spark_address(bob_sdk)
    await send_tokens(alice_sdk, bob_address, amount=80_000, token_identifier=token_id)
    await wait_for_token_balance(bob_sdk.sdk, token_id, 80_000)

    issuer = alice_sdk.sdk.get_token_issuer()
    frozen = await issuer.freeze_issuer_token(
        request=breez.FreezeIssuerTokenRequest(address=bob_address)
    )
    assert frozen.impacted_token_amount == 80_000

    unfrozen = await issuer.unfreeze_issuer_token(
        request=breez.UnfreezeIssuerTokenRequest(address=bob_address)
    )
    assert unfrozen.impacted_token_amount == 80_000
