import pytest

import breez_sdk_spark as breez

from breez_conformance.tokens import new_token_invoice
from breez_conformance.wallet import new_spark_address


async def test_parse_token_invoice(alice_sdk, funded_token):
    """A token-denominated spark invoice parses back to its token identifier and amount."""
    token_id, _ = funded_token
    invoice = await new_token_invoice(alice_sdk, token_id, amount=1_000, description="parse me")

    parsed = await alice_sdk.sdk.parse(input=invoice)
    assert isinstance(parsed, breez.InputType.SPARK_INVOICE)
    details = parsed[0]
    assert details.token_identifier == token_id
    assert details.amount == 1_000
    assert details.description == "parse me"


async def test_parse_spark_address(alice_sdk):
    """A spark address (used to receive tokens) parses as a spark address."""
    address = await new_spark_address(alice_sdk)
    parsed = await alice_sdk.sdk.parse(input=address)
    assert isinstance(parsed, breez.InputType.SPARK_ADDRESS)


async def test_parse_invalid_input(alice_sdk):
    with pytest.raises(Exception):
        await alice_sdk.sdk.parse(input="definitely not a payment instruction")
