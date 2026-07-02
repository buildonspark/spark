"""Regtest SDK configuration for conformance runs.

By default this uses the released SDK's built-in regtest config (the public Spark regtest
operators). Set BREEZ_CONFORMANCE_STACK=loadtest to instead point the SDK at Lightspark's
loadtest operators/SSP.
"""

from __future__ import annotations

import os

import breez_sdk_spark as breez

# Lightspark loadtest regtest stack (from spark-wallet SparkWalletConfig::loadtest_regtest_config).
_LOADTEST_OPERATORS = (
    (
        0,
        "0000000000000000000000000000000000000000000000000000000000000001",
        "https://0.spark.loadtest.dev.sparkinfra.net",
        "03d8d2d331e07f572636dfd371a30dfa139a8bdc99ea98f1f48e27dcc664589ecc",
    ),
    (
        1,
        "0000000000000000000000000000000000000000000000000000000000000002",
        "https://1.spark.loadtest.dev.sparkinfra.net",
        "023b1f3e062137ffc541a8edeaab7a4648aafa506d0208956123507d66d3886ac6",
    ),
    (
        2,
        "0000000000000000000000000000000000000000000000000000000000000003",
        "https://2.spark.loadtest.dev.sparkinfra.net",
        "02a2c62aa3230d9a51759b3d67399f57223455656369d28120fb39ef062b4469c8",
    ),
)
_LOADTEST_SSP_URL = "https://api.loadtest.dev.sparkinfra.net"
_LOADTEST_SSP_PUBKEY = "03e23a4912c275d1ba8742cfdfc7e9befdc2243a74be2412b7b77d227643353a1f"
_LOADTEST_SSP_SCHEMA = "graphql/spark/rc"


def regtest_config() -> "breez.Config":
    config = breez.default_config(breez.Network.REGTEST)
    config.api_key = None
    config.lnurl_domain = None
    config.sync_interval_secs = 5
    config.real_time_sync_server_url = None

    if os.environ.get("BREEZ_CONFORMANCE_STACK", "default").lower() == "loadtest":
        _apply_loadtest_stack(config)
    return config


def _apply_loadtest_stack(config: "breez.Config") -> None:
    spark_config = config.spark_config
    spark_config.signing_operators = [
        breez.SparkSigningOperator(
            id=op_id,
            identifier=identifier,
            address=address,
            identity_public_key=pubkey,
        )
        for (op_id, identifier, address, pubkey) in _LOADTEST_OPERATORS
    ]
    spark_config.ssp_config = breez.SparkSspConfig(
        base_url=_LOADTEST_SSP_URL,
        identity_public_key=_LOADTEST_SSP_PUBKEY,
        schema_endpoint=_LOADTEST_SSP_SCHEMA,
    )
    config.spark_config = spark_config
