# ADR 0002: Specification structure and scope

Status: accepted, 2026-11-11.
Contracts: [product](../product/01_PRODUCT.md), [system catalog](../README.md).

## Context

Moving older documents into `spec/` left overlapping architecture, isolated service
features, a chronological implementation ledger, UI/device details, and evidence
JSON alongside normative contracts. Product material described implementation
objects without explaining the market or the server/client experience. Numbering
shared between product and system made both directories difficult to navigate.

## Decision

Use one product document and 15 system documents, numbered consecutively within
each directory. Product owns purpose, audience, market, offering, deployment
experience, and commitments. System owns behavior, boundaries, invariants, and
acceptance. Each contract has one document owner.

Rewrite and consolidate rather than retain the old layout under new paths. Remove
standalone frontend, handheld rendering/sound, historical evidence, obsolete
commerce architecture, and redundant feature documents. Keep the material relevant
to the operating-system direction. Tool and deployment instructions may remain
beside their implementations. `docs/` is not reintroduced.

Keep architecture rationale for the common durable runtime and this restructuring.
Retire renderer benchmarking, superseded console navigation, and the earlier
competitive snapshot from the architecture-decision catalog. The product owns the
current sourced competitive assessment. Git history retains previous documents.

Specifications are normative targets. One delivery document distinguishes the
current baseline from planned guarantees and defines acceptance gates. Test outputs
belong with a change or release, not in a continuously growing specification log.

## Alternatives and consequences

Simple renaming preserves duplication and conflicting authority. Archiving all old
material inside the specification preserves the same navigation burden. A single
large system document makes independent contracts harder to find and maintain.

The chosen structure requires updating local links and keeping summaries precise.
Detailed wire-schema changes still require versioning and implementation checks;
removing an example is not authorization to break an API. New architecture work
must amend the owning contract rather than create another overlapping document.
