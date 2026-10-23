# CSP key per election

Epic: vocdoni/saas-backend#769. Follows the design in
[Pau's comment](https://github.com/vocdoni/saas-backend/issues/769#issuecomment-6313755166); the
differences are listed at the end. Step-by-step detail lives in the
[plan](../plans/2026-10-22-csp-key-per-election-plan.md); the full design, with current state, acceptance
criteria and failure modes, is [design-full](2026-10-22-csp-key-per-election-design-full.md).

## Problem

Blind CSP verification is linear in the key, and every election's key is the CSP root plus a public
salt (`Q' = Q + salt·G`). One legitimate blind signature can be shifted offline to any other
election or weight: `s' = s + rx·h(m)·(salt₂ − salt₁)`.

## Keys

Derived from the existing CSP root key by hash, so no two keys have a public relation and nothing
new is stored. `seed` is the Mongo `_id` of the question (one question = one on-chain election).

```
dE = H("vocdoni/csp/v3/election-ecdsa", root || seed)   ECDSA election key, its pubkey is the census root
dB = H("vocdoni/csp/v3/election-blind", root || seed)   blind election key, its pubkey is the census root
dX = H("vocdoni/csp/v3/weight-key", dB || u256be(w))     blind signing key for weight w
```

`H` is keccak256 with a counter, retried until the result is a valid scalar. `dE` signs ECDSA
bundles. `dB` only signs weight certificates. `dX` only blind-signs.

## Weight certificate

The CSP blind-signs with `dX` for the voter's census weight and returns, with the signature:

```
weightKey   = X (pubkey of dX, 33 bytes)
certificate = eth personal-sign by dB of
              "vocdoni/csp/v3/weight-cert" || processId || u256be(w) || X
```

The certificate is deterministic and public. It cannot be confused with a bundle signature,
because the message is not a valid bundle encoding.

## Chain (vochain-sequencer)

Two new census origins, and each one accepts a single proof type:

| Origin | Proof type | The vote is valid if |
|---|---|---|
| `OFF_CHAIN_CA_V3_ECDSA` | `ECDSA` | the bundle signer is the census root |
| `OFF_CHAIN_CA_V3_BLIND` | `ECDSA_BLIND` | the certificate signer is the census root, over (this processId, bundle weight, X), and the blind signature verifies against X |

The chain never derives a key. New elections must have a valid 33-byte census root.

Legacy origins (`OFF_CHAIN_CA`, `OFF_CHAIN_CA_V2`):
- New processes are rejected.
- Votes on already-open legacy elections keep verifying as today, until they end.
- A certificate on a legacy origin is rejected.

There is no fork height. vochain-sequencer is a single node and never re-executes past blocks, so
the rules apply from the deploy of the binary.

Proto (dvote-protobuf 1.18.0): the two origins, plus
`ProofCA.weightCertificate = CAWeightCertificate{weightKey, signature}`.

## saas-backend

- **Publish:** each question gets its own census root (`dE` or `dB`) and origin V3. Both are
  stored on the question.
- **Signing scheme by the question's stored origin:**
  - V3 uses the keys above.
  - An empty origin means a question published before this change, signed exactly as today with
    salts.
  - Existing questions are not migrated.
- **Sign responses** carry `proofType`, and for blind also `weightKey` and `weightCertificate`, so
  clients don't need to know the origin.
- **#779 is folded in:** we keep its per-question origin and drop its switch of ECDSA to V2.
- **Publish is idempotent per question.** Keys depend only on root and `_id`, so retries reproduce
  them. An orphan election from a lost ack is adopted by matching its root.

## Clients

vocdoni-sdk and the integrator-sdk put the certificate in `ProofCA` and use the `proofType` the CSP
returns. Without it they fall back to today's behaviour. vocdoni-app only bumps packages.

## Transition and rollout

The transition is soft. Legacy elections open on the flag day keep working until they end; on LTS,
some run to 2026-11-07. A follow-up (#797) removes the legacy signers afterwards.

On 2026-11-02:
1. **Beforehand:** tag proto 1.18.0, and release the SDKs and the app. They are backward
   compatible with legacy elections.
2. **Morning:** the LTS cutover from vocdoni-node to vochain-sequencer (#789).
3. **Afternoon,** once the cutover is verified: deploy the sequencer, then saas-backend, back to
   back. In between, a legacy publish from the old saas is rejected, and the idempotent publish
   retries it as V3.

Rollback: redeploy the previous sequencer before saas-backend goes out. Once V3 elections exist,
only rolling forward is possible.

## Security

- **Across elections and weights:** keys are independent hash outputs, the certificate binds
  processId and weight, and the origin fixes the proof type. This blocks the #769 shift.
- **Open legacy elections** keep the #769 weakness until they end (accepted).
- **ROS:** voters sharing one `dX` (same election and weight) who collude with concurrent signing
  sessions can forge one extra ballot at that weight. With about 256 concurrent sessions the
  polynomial ROS attack (Benhamouda et al. 2020) makes this practical. This is the same as with
  today's V2, and it cannot cross elections or weights. It is a property of the blind scheme; if
  we want to mitigate it, we would do it separately.
- **Key leaks:** a leaked `dB`/`dX` exposes one election; a leaked root exposes all of them, as
  today.

## Differences from Pau's comment

| Pau | Here | Why |
|---|---|---|
| vocdoni-node | vochain-sequencer | LTS moves to the sequencer the same morning |
| Fork height in `config/forks.go` | none | single node, no replay; the deploy is the switch |
| Chain-side salting goes away | kept for open legacy elections only | soft transition; removed by #797 |
| Separate key for ECDSA | separate derived key (`dE`), same root secret | no new secret to store; a leaked root exposes all, as today |
| Proof type must match the origin | two origins (`V3_ECDSA`, `V3_BLIND`) | the rule is per origin, and the processIDs differ |
