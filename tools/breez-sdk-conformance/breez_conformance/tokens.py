"""Token operations exercised through the public breez-sdk-spark API.

Token flows are self-funding on regtest: a fresh wallet can create and mint its own token via
the issuer API (get_token_issuer), so nothing here needs a faucet or external funding.
"""

from __future__ import annotations

from typing import Optional

import breez_sdk_spark as breez

from .wallet import SdkInstance, poll_until


async def token_balance(sdk: breez.BreezSdk, token_identifier: str) -> int:
    """Current balance for a token identifier, syncing first. Returns 0 if the wallet holds none."""
    await sdk.sync_wallet(request=breez.SyncWalletRequest())
    info = await sdk.get_info(request=breez.GetInfoRequest(ensure_synced=False))
    entry = info.token_balances.get(token_identifier)
    return entry.balance if entry is not None else 0


async def wait_for_token_balance(
    sdk: breez.BreezSdk, token_identifier: str, min_balance: int, timeout_secs: float = 120
) -> int:
    """Wait until the wallet's balance for a token reaches min_balance.

    Token mints/transfers settle asynchronously, so callers poll here rather than assuming the
    balance is available immediately after the operation returns.
    """

    async def check() -> int:
        balance = await token_balance(sdk, token_identifier)
        if balance >= min_balance:
            return balance
        raise AssertionError(f"token {token_identifier} balance {balance} < {min_balance}")

    return await poll_until(check, timeout_secs)


async def create_and_mint_token(
    issuer_inst: SdkInstance,
    *,
    name: str = "Conformance USD",
    ticker: str = "CUSD",
    decimals: int = 6,
    is_freezable: bool = True,
    max_supply: int = 1_000_000_000,
    mint_amount: int = 500_000,
) -> tuple[str, breez.TokenMetadata]:
    """Create a token issued by this wallet, mint an initial supply, and wait for it to settle.

    Returns (token_identifier, metadata). The wallet becomes the issuer of the returned token.
    """
    issuer = issuer_inst.sdk.get_token_issuer()
    metadata = await issuer.create_issuer_token(
        request=breez.CreateIssuerTokenRequest(
            name=name,
            ticker=ticker,
            decimals=decimals,
            is_freezable=is_freezable,
            max_supply=max_supply,
        )
    )
    await issuer.mint_issuer_token(request=breez.MintIssuerTokenRequest(amount=mint_amount))
    await wait_for_token_balance(issuer_inst.sdk, metadata.identifier, mint_amount)
    return metadata.identifier, metadata


async def new_token_invoice(
    inst: SdkInstance,
    token_identifier: str,
    amount: int,
    description: Optional[str] = None,
) -> str:
    """Create a spark invoice denominated in a token, for the wallet to receive."""
    resp = await inst.sdk.receive_payment(
        request=breez.ReceivePaymentRequest(
            payment_method=breez.ReceivePaymentMethod.SPARK_INVOICE(
                amount=amount,
                token_identifier=token_identifier,
                expiry_time=None,
                description=description,
                sender_public_key=None,
            )
        )
    )
    return resp.payment_request


async def send_tokens(
    sender_inst: SdkInstance,
    payment_request: str,
    *,
    amount: Optional[int] = None,
    token_identifier: Optional[str] = None,
) -> breez.Payment:
    """Send tokens to a spark address or pay a token spark invoice.

    For a bare spark address, pass amount and token_identifier. For a token invoice both are
    already encoded in the request and may be omitted.
    """
    extra = {}
    if amount is not None:
        extra["amount"] = amount
    if token_identifier is not None:
        extra["token_identifier"] = token_identifier
    prepared = await sender_inst.sdk.prepare_send_payment(
        request=breez.PrepareSendPaymentRequest(
            payment_request=breez.PaymentRequest.INPUT(input=payment_request),
            **extra,
        )
    )
    resp = await sender_inst.sdk.send_payment(
        request=breez.SendPaymentRequest(prepare_response=prepared)
    )
    return resp.payment
