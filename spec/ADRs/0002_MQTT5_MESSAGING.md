# ADR 0002: MQTT 5 messaging

Status: accepted, 2026-11-11; initial transport and runtime integration implemented.
Contract: [MQTT 5 profile](../system/05_INTERFACES_AND_INTEGRATIONS.md#mqtt-5-messaging).

## Decision

Use MQTT 5 for asynchronous inter-process and inter-server messaging, with an
existing broker/client implementation and a versioned Mitos payload schema.
The user selected MQTT 5 following consideration of custom messaging, AMQP, and
NATS/JetStream. Use Eclipse Paho Go v0.23.0 with a configured external MQTT 5
broker. Mochi v2.7.9 is an integration-test dependency; it is not embedded in the
Mitos server. Broker products remain selectable through configuration.

Use QoS 1 for durable traffic, persistent sessions, scoped topics, and explicit
Mitos application receipts. The canonical journal owns work state, inbox handling,
and outbox intent. MQTT owns transport delivery. Commands, events, and notes retain
their domain semantics; broker acknowledgment does not establish task completion.

Each home server retains its identity and authority. Multiple servers need no global
broker. Cross-server connections are explicit, scoped, and subject to deduplication.
Existing HTTP APIs and terminal streams do not need to become MQTT interfaces.

## Rationale and alternatives

A custom transport would require maintaining connection, delivery, and flow-control
machinery. Existing protocols let Mitos concentrate on grants, orchestrator fencing,
durable task transitions, and verification. MQTT 5 supplies a standard publish/subscribe
foundation while allowing a small application schema.

AMQP and NATS/JetStream were alternatives; this decision selects MQTT rather than
requiring a second messaging stack. It does not claim that MQTT alone implements
durable agent responsibilities or that the alternatives cannot meet the requirements.

## Consequences

Broker and runtime persistence are separate. A transactional outbox and durable
inbox deduplication MUST bridge them; recovery cannot rely solely on session survival
or transport acknowledgments. Peer identities use pinned Ed25519 keys and exact ingress grants, independently
of broker credentials and topic ACLs. Broker/library evaluation MUST include restart,
session loss, acknowledgment timing, security, and resource limits. Delivery gates
are in [conformance](../system/06_DELIVERY_AND_CONFORMANCE.md).

Protocol reference: [OASIS MQTT 5.0](https://docs.oasis-open.org/mqtt/mqtt/v5.0/os/mqtt-v5.0-os.html).
