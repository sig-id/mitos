# Mitos specification

Start with [the product](product/01_PRODUCT.md): why Mitos exists, who it serves,
its market, and the experience it promises. Then read [architecture](system/01_ARCHITECTURE.md)
for the system model and [delivery](system/06_DELIVERY_AND_CONFORMANCE.md) for the
current baseline and remaining work.

## Adaptive runtime reading path

[ADR 0004](ADRs/0004_ADAPTIVE_WORK_GRAPHS.md) explains the decision to generate work
graphs as evidence arrives, including lessons from Chief Wiggum. Start with
[graph transactions and completion](system/02_RUNTIME.md#adaptive-work-graphs), then
[controllers and agent templates](system/03_ORCHESTRATION.md#adaptive-controllers)
and [native observations](system/05_INTERFACES_AND_INTEGRATIONS.md#native-agent-observation-contract).
[G1–G5](system/06_DELIVERY_AND_CONFORMANCE.md#adaptive-runtime-delivery-gates) define
implementation order and acceptance; this design is not a shipped graph runtime.

## Authority

Product and system specifications are normative, including requirements not yet
implemented. MUST, MUST NOT, SHOULD, SHOULD NOT, and MAY express requirement levels.
Unqualified contract behavior is required unless explicitly marked as an example,
rationale, market assessment, or current-baseline observation. A requirement is
not a claim that the current build satisfies it.

Product defines outcomes and scope. System defines the contracts that deliver them.
ADRs explain decisions and alternatives; they do not create a competing contract.
Implementation gaps MUST remain explicit. Conflicts MUST be resolved in the owning
specification rather than hidden by a precedence rule between overlapping documents.

Each directory is numbered independently. Product has one document; system is
grouped by architectural responsibility. There is no fixed document quota. Add
material to its owner and consolidate overlap before introducing another document.
UI details, historical test logs, sample evidence files, and obsolete designs
do not belong in the specifications. Tool/deployment instructions may remain with their code.
