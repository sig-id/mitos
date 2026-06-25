defmodule Mitos.Connector do
  @moduledoc """
  Platform-level ingress adapter behaviour — the input side of the I/O plane
  (`spec/10_IO_PLANE.md` §3).

  A connector turns an external signal (a webhook POST, a chat message, an
  inbound email) into the canonical mitos work unit: a **Task**. Connectors
  live at the platform, not inside an application (`10 §3.4`); an application
  only declares *which* connector classes feed it via its `io.inputs` bindings.

  Credentials never enter the sandbox: connector secrets are held in the
  control-plane vault (`Mitos.KeyVault`, `01 §8.5`) and the connector only
  emits a redacted `reply_ref` onto the Task for round-trip delivery (`10 §5.4`).

  ## Callbacks

  * `capabilities/0`            — static declaration (`round_trip?`, `auth`, scopes).
  * `receive/2`                 — authenticate + parse an inbound raw delivery.
  * `normalize/2`               — fold a parsed signal into the intake shape that
    becomes a Task payload (`%{intent, inputs, parameters, workspace, reply_ref,
    idempotency_key}`).
  * `ack/3`                     — protocol-level acknowledgement to the source.

  `normalize/2` MUST produce an **idempotency key** from the source delivery id
  (`10 §3.2`); re-delivery of the same signal MUST NOT create a duplicate Task.
  """

  @type handle :: term()
  @type signal :: term()
  @type intake :: %{
          optional(:intent) => String.t(),
          optional(:inputs) => map(),
          optional(:parameters) => map(),
          optional(:workspace) => map(),
          optional(:reply_ref) => String.t(),
          optional(:idempotency_key) => String.t()
        }

  @doc "Static capability declaration for this connector class."
  @callback capabilities() :: %{
              required(:ingress) => true,
              required(:round_trip?) => boolean(),
              optional(:auth) => term(),
              optional(:scopes) => [String.t()]
            }

  @doc "Authenticate and parse an inbound raw delivery into one or more signals."
  @callback receive(handle, raw :: term(), opts :: keyword()) ::
              {:ok, [signal]} | {:ok, signal} | {:error, term()}

  @doc "Fold a parsed signal into the intake shape mapped onto a Task payload."
  @callback normalize(signal, opts :: keyword()) :: {:ok, intake} | {:error, term()}

  @doc "Protocol-level acknowledgement to the source (HTTP 200, Slack ack, …)."
  @callback ack(handle, signal, outcome :: term()) :: :ok | {:error, term()}

  @doc """
  Resolve a connector name to its adapter module, or `nil` when unregistered.

  Generic platform connectors (`webhook`, `manual`, `web`, `chat`, `email`,
  `tracker`) are registered here. An application MAY declare a usage-specific
  connector (`10 §3.4`); such a connector is resolved at bind time, not here.
  """
  def lookup(name) when is_binary(name) do
    Map.get(registry(), normalize_name(name))
  end

  def lookup(_name), do: nil

  @doc "Registered connector names."
  def names, do: Map.keys(registry()) |> Enum.sort()

  defp registry do
    %{
      "webhook" => Mitos.Connectors.Webhook,
      "manual" => Mitos.Connectors.Manual,
      "web" => Mitos.Connectors.Manual
    }
  end

  defp normalize_name(name) do
    name |> to_string() |> String.downcase() |> String.trim()
  end
end
