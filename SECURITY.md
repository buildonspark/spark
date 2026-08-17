# Security Policy

## Program status: between platforms

Our HackerOne program is retired. The replacement is being stood up on Intigriti and should open
within two weeks.

Informational, low, and medium findings: hold them and submit through Intigriti when it opens.

If you feel your finding is urgent or critical in nature and cannot wait, please email
<security@lightspark.com>, following the instructions below.

## Reporting an urgent or critical vulnerability

Send the report to <security@lightspark.com>. Keep the vulnerability out of the subject line:

```
Critical Security report - <Github UserName>
```

Encrypt the body to our PGP key (below). Fill in this template as the plaintext, then encrypt
the whole thing:

```
Reporter:            <name or handle, and how you want to be credited>
Contact:             <email, plus your PGP public key so we can reply encrypted>
Affected component:  <e.g. signing operator, JS SDK, SSP, a specific endpoint or host>
Severity:            <your rating, and why you rate it there>

Summary:
  <two or three sentences: what is broken and what it lets an attacker do>

Reproduction:
  <numbered steps with the exact requests, inputs, keys, or transactions used.
   Say which network you tested against: regtest, signet, testnet, or mainnet.>

Impact:
  <what an attacker gains: funds at risk, whose funds, under what preconditions,
   and whether it needs a malicious operator or other privileged position>

Proof of concept:
  <code, transcript, transaction IDs, or a link. Attach files if that's easier,
   encrypted to the same key.>

Disclosure:
  <any deadline you intend to hold us to, and whether you've told anyone else>
```

If you cannot get PGP working, send us a message without disclosing the vulnerability and we
will reach out to you.

## Our PGP key

This key is temporary and expires **2026-09-10**. It covers the gap until the Intigriti program
opens. Once Intigriti is live, submit there. We will publish a long-lived key and retire this
one.

Fingerprint:

```
07D0 8C7D 0644 6E55 8207  A88C 308E 95C5 B800 6A38
```

The key it belongs to:

```
-----BEGIN PGP PUBLIC KEY BLOCK-----

mDMEanuy2hYJKwYBBAHaRw8BAQdAj5voTTiAYhmyGmp1T9jpCIr0jS7rqbxj7oAL
HcI2i5W0PkxpZ2h0c3BhcmsgU2VjdXJpdHkgKFRyYW5zaXRvcnkgS2V5KSA8U2Vj
dXJpdHlATGlnaHRzcGFyay5jb20+iJYEExYKAD4CGwMFCwkIBwIGFQoJCAsCBBYC
AwECHgECF4AWIQQH0Ix9BkRuVYIHqIwwjpXFuABqOAUCanuzQAUJACeNZgAKCRAw
jpXFuABqONnLAQD5W5cyCtx8uj9jNYa969QGZeYnJGKD6BPjhrFYuKLPlQEA9OKm
XJ6V+Z/ppe3pA/O5t7TPQ98gpi7WziQOEbOhsA64OARqe7LaEgorBgEEAZdVAQUB
AQdAtygXj2ybRiEMQA1bCRag2Tk8jJcO2G3kOYpBsFnJjGwDAQgHiH4EGBYKACYC
GwwWIQQH0Ix9BkRuVYIHqIwwjpXFuABqOAUCanuzWwUJACeNgQAKCRAwjpXFuABq
OK3DAQCe+xNAAENGa2hUY9zfm4ecswleYJh/kyAfSZia1gtBvgD/S8JiQfrllCgw
rtDrIOaRB0vm7e9PqxtZm0ZTgAPLGAw=
=BSsI
-----END PGP PUBLIC KEY BLOCK-----
```

Save that block to `lightspark-security.asc`, then:

```bash
gpg --import lightspark-security.asc
gpg --encrypt --armor --recipient security@lightspark.com report.txt
```

## Out of scope

| Category | Details |
|---|---|
| Attacks requiring a malicious signing operator | Spark's trust model assumes at least one honest operator, so attacks that need an operator to misbehave are excluded for now. |
| Denial of service | Do not run anything that could affect the availability of our systems without written authorization from us, request floods included. |
| Known vulnerable dependencies, without a PoC | Tell us how the vulnerability is reachable and exploitable in our code. A version number from a scanner is not a report. |
| `support.lightspark.com` and `support@lightspark.com` | Probing them or mailing them about bugs can disqualify you from rewards. |
| Assets not explicitly in scope | The Intigriti program will carry the current asset list. |

Intigriti's Core Ineligible Findings will apply to the new program on top of these.

## Rules

- If we cannot reproduce the bug from your report, it is not eligible for a reward.
- One vulnerability per report, unless you have to chain several to show real impact.
- On duplicates, the first reproducible report wins. Several bugs sharing one root cause get one
  bounty.
- Do not violate anyone's privacy, destroy data, or degrade our service. Only touch accounts you
  own or have written permission to test.
- Do not discuss a vulnerability outside the program without our written consent, including
  after we've fixed it.

## Testing Spark

Test against regtest or signet, not mainnet. The repository ships a full local environment (see
the README and `run-everything.sh`) with bitcoind, Postgres, three signing operators, and an
SSP. Build your proof of concept there.

Nothing you run against mainnet operators should move real funds or degrade the network for
anyone else.

## What we're most interested in

Bugs that break custody or availability of funds:

- Any path that lets a party spend a leaf they do not own, or spend one twice
- Broken key tweaking or key splitting, where old key material stays usable after a transfer
- Missing or incorrect authorization on user-facing gRPC handlers: one user acting on another's
  resources
- Timelock ordering bugs that let a previous owner exit ahead of the current one
- Races and TOCTOU in multi-step database operations, especially around leaf locking
- Anything that breaks unilateral exit, so a user cannot recover funds when the operators stop
  responding
- Token (BTKN) supply bugs: minting, burning, or freezing outside the issuer's authority

## Supported versions

Spark is continuously deployed and operators run the current release, so fixes land on the live
network, not in backported branches. For the client SDKs, report against the latest published
version. We do not patch older ones.
