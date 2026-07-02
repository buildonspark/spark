"""SDK wallet lifecycle: connect, event listening, and generic wait machinery.

These deliberately use only the public breez-sdk-spark API: connect(), an EventListener that
forwards events into an asyncio.Queue, and polling built on the SDK's own calls.
"""

from __future__ import annotations

import asyncio
import logging
import secrets
import tempfile
from dataclasses import dataclass
from typing import Awaitable, Callable, Optional, TypeVar

import breez_sdk_spark as breez

from .config import regtest_config

_LOG = logging.getLogger(__name__)

T = TypeVar("T")


class _QueueListener(breez.EventListener):
    """Forwards SDK events into an asyncio.Queue (mirrors the Rust ChannelEventListener)."""

    def __init__(self, queue: asyncio.Queue) -> None:
        self._queue = queue

    async def on_event(self, event: breez.SdkEvent) -> None:
        _LOG.info("SDK event: %s", event)
        try:
            self._queue.put_nowait(event)
        except asyncio.QueueFull:
            _LOG.warning("event queue full; dropping event")


@dataclass
class SdkInstance:
    sdk: breez.BreezSdk
    events: asyncio.Queue
    storage_dir: str
    _tmp: Optional[tempfile.TemporaryDirectory] = None

    async def disconnect(self) -> None:
        try:
            await self.sdk.disconnect()
        finally:
            if self._tmp is not None:
                self._tmp.cleanup()


async def build_sdk(seed_bytes: Optional[bytes] = None) -> SdkInstance:
    """Connect a fresh regtest SDK on a unique temp storage dir, with an event queue attached."""
    if seed_bytes is None:
        seed_bytes = secrets.token_bytes(32)
    return await _build(breez.Seed.ENTROPY(seed_bytes))


async def build_sdk_from_mnemonic(mnemonic: str, passphrase: Optional[str] = None) -> SdkInstance:
    return await _build(breez.Seed.MNEMONIC(mnemonic=mnemonic, passphrase=passphrase))


async def _build(seed: breez.Seed) -> SdkInstance:
    tmp = tempfile.TemporaryDirectory(prefix="breez-conformance-")
    config = regtest_config()
    sdk = await breez.connect(
        request=breez.ConnectRequest(config=config, seed=seed, storage_dir=tmp.name)
    )
    queue: asyncio.Queue = asyncio.Queue(maxsize=1000)
    await sdk.add_event_listener(listener=_QueueListener(queue))
    await sdk.get_info(request=breez.GetInfoRequest(ensure_synced=True))
    return SdkInstance(sdk=sdk, events=queue, storage_dir=tmp.name, _tmp=tmp)


async def poll_until(check: Callable[[], Awaitable[T]], timeout_secs: float, poll_secs: float = 1.0) -> T:
    """Poll an async predicate that raises until it succeeds, or time out."""
    loop = asyncio.get_running_loop()
    deadline = loop.time() + timeout_secs
    last_err: Optional[BaseException] = None
    while True:
        try:
            return await check()
        except Exception as exc:  # noqa: BLE001 - predicate signals "not yet" by raising
            last_err = exc
            if loop.time() >= deadline:
                raise TimeoutError(f"timed out after {timeout_secs}s: {exc}") from last_err
            await asyncio.sleep(poll_secs)


async def wait_for_event(
    queue: asyncio.Queue, timeout_secs: float, matcher: Callable[[breez.SdkEvent], Optional[T]]
) -> T:
    """Wait for an event the matcher accepts. matcher returns a value to accept, None to ignore."""
    loop = asyncio.get_running_loop()
    deadline = loop.time() + timeout_secs
    while True:
        remaining = deadline - loop.time()
        if remaining <= 0:
            raise TimeoutError(f"timed out after {timeout_secs}s waiting for event")
        event = await asyncio.wait_for(queue.get(), timeout=remaining)
        result = matcher(event)
        if result is not None:
            return result


async def new_spark_address(inst: SdkInstance) -> str:
    resp = await inst.sdk.receive_payment(
        request=breez.ReceivePaymentRequest(payment_method=breez.ReceivePaymentMethod.SPARK_ADDRESS())
    )
    return resp.payment_request
