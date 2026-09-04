defmodule Mitos.Event do
  @moduledoc """
  The two canonical log record types (01 §8.1/8.2, elaborated 04 §5/§6).

  The event log is the append-only ordered union of Decision + Effect events.
  It is simultaneously: run state, debug trace, replay source, audit record,
  and billing ledger. Field names are fixed by `01` §8 and MUST NOT be renamed.
  """

  defmodule Decision do
    @moduledoc "DecisionEvent — orchestrator / loop-manager → substrate (01 §8.1, 04 §5)."

    # seq + ts are stamped by the single writer at append (Mitos.Log.append/2),
    # so they are not enforced at construction.
    @enforce_keys [:actor, :type]
    defstruct run_id: nil,
              seq: nil,
              ts: nil,
              # "orchestrator" | "loop_manager" | "supervisor"
              actor: nil,
              # advance | insert | retry | switch | skip
              #   | request_approval | escalate | finish | abort | rewind
              type: nil,
              # capability_ref | stage_id | checkpoint_id | null
              target: nil,
              # type-specific (e.g. switch -> {model,effort,harness})
              params: %{},
              # hash of the state the decision was taken on (replay integrity)
              inputs_hash: nil,
              # NL justification — AUDIT ONLY, never controls flow
              rationale: "",
              # { tokens, usd, wall_ms, remaining }
              budget: %{}

    @types ~w(advance insert retry switch skip request_approval escalate finish abort rewind)a

    # 16 §5.2: a fleet supervisor acts on ANOTHER run and is restricted to the
    # moves that cannot declare that run's own pipeline judgment. It MUST NOT
    # emit advance/insert/skip/finish.
    @supervisor_types ~w(retry switch rewind escalate abort request_approval)a

    def types, do: @types
    def supervisor_types, do: @supervisor_types

    def new(fields) do
      fields = Map.new(fields)
      type = fields |> Map.fetch!(:type) |> to_atom()

      unless type in @types do
        raise ArgumentError, "unknown Decision type: #{inspect(type)}"
      end

      # 16 §5.2 is enforced here, not by convention: the restricted set is a
      # property of the record, so a supervisor cannot finish another run even
      # by constructing the event by hand.
      if to_string(fields[:actor]) == "supervisor" and type not in @supervisor_types do
        raise ArgumentError,
              "supervisor may not emit #{type} (16 §5.2: restricted to #{inspect(@supervisor_types)})"
      end

      struct!(__MODULE__, %{fields | type: type})
    end

    @doc "kind tag used in the on-disk JSONL union."
    def kind, do: "decision"

    defp to_atom(a) when is_atom(a), do: a
    defp to_atom(s) when is_binary(s), do: String.to_existing_atom(s)
  end

  defmodule Effect do
    @moduledoc "EffectEvent — substrate → log (01 §8.2, extended 04 §6)."

    # seq + ts are stamped by the single writer at append (Mitos.Log.append/2).
    @enforce_keys [:type]
    defstruct run_id: nil,
              seq: nil,
              ts: nil,
              type: nil,
              # the DecisionEvent seq that led here (causality)
              caused_by: nil,
              # ids / paths
              ref: %{},
              # type-specific body
              payload: %{}

    # 01 §8.2 core list + 04 §6 control-plane extensions + 10 §5.3 delivery +
    # 06 §3 task-lifecycle. The trailing "..." in 01 §8.2 is an open list; these
    # are the ones the substrate emits.
    @types ~w(
      capability_started capability_finished tool_brokered
      egress_allowed egress_denied artifact_registered commit
      approval_requested approval_resolved memory_written budget_charged
      iteration_started iteration_finished context_compacted
      harness_switched escalation_raised verification_recorded
      hook_ran result_harvested
      task_enqueued task_blocked task_scheduled task_running task_done
      task_failed task_parked task_cancelled invoke_requested subtask_returned
      signal_received delivery_attempted delivery_sent delivery_failed
      run_started run_finished
      quiescent steer_applied steer_rejected harness_quarantined
      decision_delivered decision_defaulted decision_rejected
      checkpoint_taken oracle_tampered acceptance_recorded rewound
    )a

    def types, do: @types

    def new(fields) do
      fields = Map.new(fields)
      type = fields |> Map.fetch!(:type) |> to_atom()

      unless type in @types do
        raise ArgumentError, "unknown Effect type: #{inspect(type)}"
      end

      struct!(__MODULE__, %{fields | type: type})
    end

    def kind, do: "effect"

    defp to_atom(a) when is_atom(a), do: a
    defp to_atom(s) when is_binary(s), do: String.to_existing_atom(s)
  end

  @doc "Serialize an event to a single JSONL line (tagged union)."
  def to_line(%Decision{} = d), do: encode_line(Decision.kind(), d)
  def to_line(%Effect{} = e), do: encode_line(Effect.kind(), e)

  defp encode_line(kind, event) do
    event
    |> Map.from_struct()
    |> Map.put(:kind, kind)
    |> Mitos.JSON.encode!()
    |> Kernel.<>("\n")
  end

  @doc "Parse a JSONL line back into a Decision or Effect struct."
  def from_line(line) do
    map = Mitos.JSON.decode!(line)
    {kind, map} = Map.pop(map, "kind")
    fields = atomize(map)

    case kind do
      "decision" -> Decision.new(fields)
      "effect" -> Effect.new(fields)
    end
  end

  # Top-level keys become atoms (they are known struct fields); nested payload
  # maps stay string-keyed.
  defp atomize(map) do
    Map.new(map, fn {k, v} -> {String.to_atom(k), v} end)
  end
end
