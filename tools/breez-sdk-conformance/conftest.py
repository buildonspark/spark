import logging
import os

import pytest

from breez_conformance.tokens import create_and_mint_token
from breez_conformance.wallet import build_sdk

logging.basicConfig(
    level=os.environ.get("BREEZ_CONFORMANCE_LOG_LEVEL", "INFO"),
    format="%(asctime)s %(levelname)s %(name)s %(message)s",
)


@pytest.fixture
async def alice_sdk():
    inst = await build_sdk()
    try:
        yield inst
    finally:
        await inst.disconnect()


@pytest.fixture
async def bob_sdk():
    inst = await build_sdk()
    try:
        yield inst
    finally:
        await inst.disconnect()


@pytest.fixture
async def funded_token(alice_sdk):
    """A token issued and minted by alice_sdk, settled and ready to transfer. Returns (id, metadata)."""
    return await create_and_mint_token(alice_sdk)
