defmodule Mitos.Decision.Policy do
  @moduledoc """
  Whether to ask a human at all, and what silence may do (`15 §1`, `§2`).

      Escalate on irreversibility x uncertainty, never on category.

  Category ("it's a write", "it's an egress") is a property of the *mechanism*.
  Irreversibility is a property of the *consequence*, and the consequence is
  what a human is actually being asked about. Gating on category is what
  produces the interrupt storms that make long-horizon autonomy unusable.

  Classification is **deterministic** — computed from the act plus the run's
  grants, never by a model and never from the agent's own claim about its
  intent. Where it cannot be derived, the answer is `:external`:

      Unclassifiable is not permission (15 §2.1).

  The corollary is the design's main autonomy lever: a run that structurally
  cannot reach anything outside its sandbox earns near-total freedom for free,
  because every mistake it can make is one `rewind` away (`16 §3`).
  """

  alias Mitos.Decision.Card

  @classes [:internal, :recoverable, :external]

  # Kinds that escalate regardless of class, because no policy can resolve them.
  @unguessable [:missing_input, :judgment]

  def classes, do: @classes
  def unguessable_kinds, do: @unguessable

  @doc """
  Classify an act. `act` is a map describing what is about to happen:

      %{kind: :workspace_write | :exec | :egress | :git_push | :publish | :payment | :message,
        paths: [...], dest: "...", requires_approval: bool, remote: "...", grants: %{...}}

  Returns `:internal | :recoverable | :external`.
  """
  def classify(act) when is_map(act) do
    act = Map.new(act, fn {k, v} -> {atomize(k), v} end)

    cond do
      # An explicit policy demand always wins upward, never downward.
      act[:requires_approval] == true -> :external
      act[:kind] in [:publish, :payment, :message, :prod_mutation] -> :external
      act[:kind] == :git_push -> push_class(act)
      act[:kind] == :egress -> egress_class(act)
      act[:kind] in [:workspace_write, :exec] -> workspace_class(act)
      act[:kind] == :sandbox_write -> :recoverable
      # 15 §2.1: an act we cannot describe is treated as the worst case.
      true -> :external
    end
  end

  # A push to a mitos-owned mirror is recoverable (we hold the other end); a
  # push to an upstream nobody controls is not.
  defp push_class(act) do
    if mitos_owned?(act[:remote], act[:grants]), do: :recoverable, else: :external
  end

  # An egress with a broker rule that attaches a credential is a real outbound
  # side effect on someone else's system: recoverable only if the policy says
  # the destination is one mitos can undo against, which it never asserts today.
  defp egress_class(act) do
    cond do
      act[:requires_approval] -> :external
      act[:read_only] == true -> :recoverable
      true -> :external
    end
  end

  # Inside the declared workspace, a checkpoint undoes it (16 §3). Outside, it
  # escaped the thing rewind can restore.
  defp workspace_class(act) do
    paths = List.wrap(act[:paths])
    root = act[:workspace]

    cond do
      paths == [] -> :internal
      is_nil(root) -> :recoverable
      Enum.all?(paths, &inside?(&1, root)) -> :internal
      true -> :recoverable
    end
  end

  defp inside?(path, root) do
    p = Path.expand(to_string(path))
    r = Path.expand(to_string(root))
    p == r or String.starts_with?(p, r <> "/")
  end

  defp mitos_owned?(nil, _grants), do: false

  defp mitos_owned?(remote, grants) do
    owned = (grants || %{})[:mirrors] || (grants || %{})["mirrors"] || []
    Enum.any?(owned, &(to_string(&1) == to_string(remote)))
  end

  @doc """
  Should this act interrupt a human, and how? Returns

      :act                      # log only, never ask (internal)
      {:review, class}          # act, log, queue for the async digest (recoverable)
      {:escalate, class}        # park and ask (external / unguessable)

  This is the whole interrupt budget in one function, which is why it is
  deterministic and testable rather than distributed through the call sites.
  """
  def disposition(act) do
    kind = atomize((Map.new(act, fn {k, v} -> {atomize(k), v} end))[:kind])

    if kind in @unguessable do
      {:escalate, :external}
    else
      case classify(act) do
        :internal -> :act
        :recoverable -> {:review, :recoverable}
        :external -> {:escalate, :external}
      end
    end
  end

  @doc """
  Default + TTL for a class, honouring `15 §4`. `external` gets `{nil, nil}` —
  fail-secure — and no configuration may override that, because a configurable
  fail-secure is a fail-safe with extra steps.
  """
  def silence(:external, _opts), do: {nil, nil}

  def silence(class, opts) when class in [:internal, :recoverable] do
    {opts[:default] || "approve", opts[:ttl_ms] || default_ttl(class)}
  end

  defp default_ttl(class) do
    Application.get_env(:mitos, :decision, [])
    |> Keyword.get(:ttl_ms, internal: 5 * 60_000, recoverable: 30 * 60_000)
    |> Keyword.get(class, 30 * 60_000)
  end

  @doc """
  Build a card for an act that `disposition/1` said to escalate. Fills the
  class-appropriate silence policy so a caller cannot accidentally hand an
  `external` act a default.
  """
  def card_for(act, fields) do
    class = classify(act)
    {default, ttl} = silence(class, fields)

    Card.new(
      Map.merge(Map.new(fields), %{
        reversibility: class,
        default: default,
        ttl_ms: ttl
      })
    )
  end

  @doc """
  A run's escalation floor: below it, an act goes to the digest instead of a
  push (`15 §5.3`). Per-tenant, defaulting to "everything recoverable is a
  digest item".
  """
  def escalation_floor(tenant \\ nil) do
    Application.get_env(:mitos, :decision, [])
    |> Keyword.get(:escalation_floor, %{})
    |> Map.get(tenant_org(tenant), :recoverable)
  end

  defp tenant_org(nil), do: nil
  defp tenant_org(%{"org" => org}), do: org
  defp tenant_org(%{org: org}), do: org
  defp tenant_org(other), do: other

  defp atomize(a) when is_atom(a), do: a

  defp atomize(s) when is_binary(s) do
    String.to_existing_atom(s)
  rescue
    ArgumentError -> :unknown
  end

  defp atomize(_), do: :unknown
end
