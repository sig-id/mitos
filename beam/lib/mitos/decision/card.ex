defmodule Mitos.Decision.Card do
  @moduledoc """
  The one shape every request for a human takes (`01 §8.9`, elaborated `15 §3`).

  Carried in the `approval_requested` Effect payload; the Effect names are
  unchanged (`01 §8.2`). A card is an *input* to the substrate, never an
  authority path — the agent asks no one for anything (`01 §8.5`).

  The invariant this module exists to enforce structurally, rather than by
  procedure:

      reversibility in [:internal, :recoverable]  ->  default REQUIRED, ttl REQUIRED  (fail-safe)
      reversibility == :external                  ->  default FORBIDDEN, no ttl       (fail-secure)

  Inverting it is the single most dangerous defect available in this design: a
  default on an `external` act means **silence authorizes an irreversible
  action**. So `new/1` refuses such a card rather than downgrading it, and
  `15 §4`'s schema rule (`default` null ⇔ `ttl_ms` null) is checked both ways.
  """

  alias Mitos.Decision.Policy

  @kinds ~w(approve choose supply_value amend_brief rewind abort)a
  @question_max 200

  @enforce_keys [:decision_id, :run_id, :kind, :reversibility, :question]
  defstruct decision_id: nil,
            run_id: nil,
            caused_by: nil,
            tenant: %{},
            stage_id: nil,
            kind: :approve,
            reversibility: :external,
            question: nil,
            # FOLDED state only — never a transcript, never `rationale`
            context: %{attempted: nil, blocked_by: nil, evidence_refs: []},
            options: [],
            default: nil,
            ttl_ms: nil,
            blast_radius: %{paths: [], destinations: [], spend: nil},
            cost_of_wrong: %{wasted_wall_ms: 0, wasted_usd: 0.0, recoverable: true},
            value_schema: nil,
            dedupe_key: nil,
            raised_at: nil

  @type t :: %__MODULE__{}

  def kinds, do: @kinds

  @doc """
  Build a card, enforcing `15 §3`/`§4`. Returns `{:error, reason}` rather than a
  silently repaired card — a card that cannot be expressed correctly is a bug in
  whatever raised it, and repairing it here would hide exactly the case that
  matters.
  """
  def new(fields) do
    f = Map.new(fields)

    card = %__MODULE__{
      decision_id: f[:decision_id] || Mitos.id(),
      run_id: Map.fetch!(f, :run_id),
      caused_by: f[:caused_by],
      tenant: f[:tenant] || %{},
      stage_id: f[:stage_id],
      kind: atomize(f[:kind] || :approve),
      reversibility: atomize(f[:reversibility] || :external),
      question: f[:question],
      context: normalize_context(f[:context] || %{}),
      options: normalize_options(f[:options] || default_options(atomize(f[:kind] || :approve))),
      default: f[:default] && to_string(f[:default]),
      ttl_ms: f[:ttl_ms],
      blast_radius: normalize_blast(f[:blast_radius] || %{}),
      cost_of_wrong: normalize_cost(f[:cost_of_wrong] || %{}),
      value_schema: f[:value_schema],
      dedupe_key: f[:dedupe_key],
      raised_at: f[:raised_at] || Mitos.ts()
    }

    card = %{card | dedupe_key: card.dedupe_key || dedupe_key(card)}

    with :ok <- validate(card), do: {:ok, card}
  end

  def new!(fields) do
    case new(fields) do
      {:ok, card} -> card
      {:error, reason} -> raise ArgumentError, "invalid decision card: #{inspect(reason)}"
    end
  end

  @doc "Validate a card against 15 §3/§4. The fail-safe/fail-secure split lives here."
  def validate(%__MODULE__{} = c) do
    cond do
      c.kind not in @kinds ->
        {:error, {:unknown_kind, c.kind}}

      c.reversibility not in Policy.classes() ->
        {:error, {:unknown_reversibility, c.reversibility}}

      not is_binary(c.question) or c.question == "" ->
        {:error, :question_required}

      String.length(c.question) > @question_max ->
        {:error, {:question_too_long, String.length(c.question)}}

      # 15 §4, fail-secure: an externally-visible act has no default, so silence
      # can never authorize it. Refused, never downgraded.
      c.reversibility == :external and not is_nil(c.default) ->
        {:error, :external_may_not_default}

      c.reversibility == :external and not is_nil(c.ttl_ms) ->
        {:error, :external_may_not_expire}

      # 15 §4, fail-safe: a reversible act MUST advance on silence, or the
      # interrupt you removed comes back as a stall.
      c.reversibility in [:internal, :recoverable] and is_nil(c.default) ->
        {:error, :reversible_requires_default}

      # The schema rule, checked both ways (01 §8.9).
      is_nil(c.default) != is_nil(c.ttl_ms) ->
        {:error, :default_and_ttl_must_agree}

      c.default && not Enum.any?(c.options, &(&1.id == c.default)) ->
        {:error, {:default_not_an_option, c.default}}

      c.kind == :choose and length(c.options) < 2 ->
        {:error, :choose_requires_options}

      c.kind == :supply_value and is_nil(c.value_schema) ->
        {:error, :supply_value_requires_schema}

      Enum.count(c.options, & &1.recommended) > 1 ->
        {:error, :at_most_one_recommendation}

      true ->
        :ok
    end
  end

  @doc "Has this card's TTL elapsed? An `external` card never expires (15 §4)."
  def expired?(%__MODULE__{ttl_ms: nil}, _now), do: false

  def expired?(%__MODULE__{ttl_ms: ttl, raised_at: raised}, now) do
    case {DateTime.from_iso8601(to_string(raised)), DateTime.from_iso8601(to_string(now))} do
      {{:ok, t0, _}, {:ok, t1, _}} -> DateTime.diff(t1, t0, :millisecond) >= ttl
      _ -> false
    end
  end

  @doc "Serializable form for the `approval_requested` Effect payload."
  def to_payload(%__MODULE__{} = c) do
    %{
      "decision_id" => c.decision_id,
      "kind" => to_string(c.kind),
      "reversibility" => to_string(c.reversibility),
      "question" => c.question,
      "context" => %{
        "attempted" => c.context.attempted,
        "blocked_by" => c.context.blocked_by,
        "evidence_refs" => c.context.evidence_refs
      },
      "options" =>
        Enum.map(c.options, &%{"id" => &1.id, "label" => &1.label, "consequence" => &1.consequence, "recommended" => &1.recommended}),
      "default" => c.default,
      "ttl_ms" => c.ttl_ms,
      "blast_radius" => %{
        "paths" => c.blast_radius.paths,
        "destinations" => c.blast_radius.destinations,
        "spend" => c.blast_radius.spend
      },
      "cost_of_wrong" => %{
        "wasted_wall_ms" => c.cost_of_wrong.wasted_wall_ms,
        "wasted_usd" => c.cost_of_wrong.wasted_usd,
        "recoverable" => c.cost_of_wrong.recoverable
      },
      "value_schema" => c.value_schema,
      "dedupe_key" => c.dedupe_key,
      "stage_id" => c.stage_id,
      "raised_at" => c.raised_at
    }
  end

  @doc "Rebuild a card from an `approval_requested` payload (the fold direction)."
  def from_payload(run_id, %{} = p, caused_by \\ nil) do
    %__MODULE__{
      decision_id: p["decision_id"],
      run_id: run_id,
      caused_by: caused_by,
      stage_id: p["stage_id"],
      kind: atomize(p["kind"] || "approve"),
      reversibility: atomize(p["reversibility"] || "external"),
      question: p["question"],
      context: normalize_context(p["context"] || %{}),
      options: normalize_options(p["options"] || []),
      default: p["default"],
      ttl_ms: p["ttl_ms"],
      blast_radius: normalize_blast(p["blast_radius"] || %{}),
      cost_of_wrong: normalize_cost(p["cost_of_wrong"] || %{}),
      value_schema: p["value_schema"],
      dedupe_key: p["dedupe_key"],
      raised_at: p["raised_at"]
    }
  end

  # Identical decisions across parallel workers collapse into one card and
  # resolve together (15 §5.3) — the difference between three workers hitting
  # the same wall and three pushes to a phone.
  defp dedupe_key(%__MODULE__{} = c) do
    Mitos.sha256(
      Enum.join(
        [
          to_string(c.kind),
          to_string(c.reversibility),
          c.question,
          Enum.map_join(c.options, ",", & &1.id),
          Enum.join(c.blast_radius.destinations, ","),
          Enum.join(c.blast_radius.paths, ",")
        ],
        "|"
      )
    )
  end

  defp default_options(:approve),
    do: [
      %{id: "approve", label: "Approve", consequence: "the action proceeds", recommended: false},
      %{id: "deny", label: "Deny", consequence: "the action is refused", recommended: false}
    ]

  defp default_options(:abort),
    do: [
      %{id: "abort", label: "Abort the run", consequence: "the run ends; work so far is kept", recommended: false},
      %{id: "continue", label: "Keep going", consequence: "the run continues", recommended: false}
    ]

  defp default_options(_), do: []

  defp normalize_options(options) do
    Enum.map(options, fn o ->
      o = Map.new(o, fn {k, v} -> {to_string(k), v} end)

      %{
        id: to_string(o["id"]),
        label: o["label"] || to_string(o["id"]),
        # 15 §3.1: every option states what happens if it is chosen. A bare
        # label makes the card unanswerable without opening the console.
        consequence: o["consequence"] || "",
        recommended: !!o["recommended"]
      }
    end)
  end

  defp normalize_context(ctx) do
    ctx = Map.new(ctx, fn {k, v} -> {to_string(k), v} end)

    %{
      attempted: ctx["attempted"],
      blocked_by: ctx["blocked_by"],
      evidence_refs: ctx["evidence_refs"] || []
    }
  end

  defp normalize_blast(b) do
    b = Map.new(b, fn {k, v} -> {to_string(k), v} end)
    %{paths: b["paths"] || [], destinations: b["destinations"] || [], spend: b["spend"]}
  end

  defp normalize_cost(c) do
    c = Map.new(c, fn {k, v} -> {to_string(k), v} end)

    %{
      wasted_wall_ms: c["wasted_wall_ms"] || 0,
      wasted_usd: c["wasted_usd"] || 0.0,
      recoverable: Map.get(c, "recoverable", true)
    }
  end

  defp atomize(a) when is_atom(a), do: a
  defp atomize(s) when is_binary(s), do: String.to_existing_atom(s)
end
