# 02 – Server, clients, and execution hosts

## Ownership and deployment

Every project MUST have one home Mitos server. It owns task state, services,
decisions, grants, reservations, and operation receipts. Clients connect to that
server; client caches and drafts MUST NOT establish authoritative state.

An execution host runs workers and owns their workspaces, output, and enforceable
limits. A process host is the component that launches and inspects processes; it
may be an in-process adapter, a separate daemon, or a remote provider. Deployment
MUST state which component owns each responsibility.

| Deployment | Client | Home server and execution |
|---|---|---|
| Local desktop | Browser or CLI on desktop | Same desktop |
| Desktop with mobile access | Desktop browser and phone | Desktop remains awake and reachable |
| Always-on machine | Laptop and phone | Home server or remote machine |
| Remote worker placement | Any authorized client | Home server admits work on another execution host |

Remote client access and remote worker placement are independent capabilities.
Connecting a phone MUST NOT relocate work to the phone. A client connected to
several servers MUST identify the server for every operation and decision.

## Disconnect and failure behavior

| Event | Required behavior |
|---|---|
| Browser closes, CLI disconnects, phone sleeps | Execution continues; reconnect restores the authoritative view. |
| Home server restarts | Replay durable state and reconcile pending execution before replacement. |
| Process host survives a server restart | Reattach only using exact incarnation and launch receipt. |
| Execution host loses power | Record interruption or uncertainty; saved work supports recovery, not process survival. |
| Network partition | Preserve uncertainty and unsafe claims; absence of contact does not prove exit. |

Process survival, durable state, executor replacement, and host failover MUST be
advertised separately. A provider without surviving-process inspection MUST NOT
claim seamless restart. The current local implementation preserves saved state
but marks active work interrupted on server restart.

## Remote access

Remote clients MUST use authenticated, scoped, revocable bindings and encrypted
transport. Pairing MUST bind a client to a particular server identity and expose
its permitted operations. Revocation MUST reject subsequent operations and close
privileged streams. Stale client views MUST fail revision checks on mutation.

A relay MAY forward encrypted connections and a notification gateway MAY deliver
wake-up hints. Neither owns work, grants, or decision resolution. Dismissing a
notification MUST NOT resolve the underlying decision. SSH may place execution;
it does not by itself supply a remote-runtime protocol or confinement.

The existing optional HTTPS device API exposes a restricted subset of server
operations. It MUST NOT be represented as full paired remote access. See
[API and compatibility](14_API_AND_COMPATIBILITY.md).

## Process-host contract

The server MUST durably admit work before dispatch. A process host MUST bind launch,
inspection, cancellation, and output to an operation identity and exact process
incarnation. A PID alone is insufficient because it can be reused. Lost launch
responses MUST be resolved by inspecting the original receipt before another launch.

Remote placement MUST retain one home authority and host-local enforcement. During
a partition, previously admitted work MAY continue only within already granted,
locally enforceable limits. Hosts MUST NOT invent new grants or release claims on
behalf of an unreachable authority. Reconnection MUST reconcile receipts, resource
custody, and results before conflicting work starts.

Automatic server failover, replicated writers, and live process migration are
outside the initial deployment contract.
