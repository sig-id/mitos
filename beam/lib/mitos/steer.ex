defmodule Mitos.Steer do
  @moduledoc """
  Control as a verified transaction (14 §3.3), never a keystroke.

      wait for a quiescence edge >= T_steer
      before   <- snapshot                      # MUST be :ok
      expected <- classifier.transition(before, command)
      write(encode(command))
      after    <- snapshot at the next edge
      matches? -> steer_applied  |  else -> ESC, steer_rejected, escalate

  Two properties are load-bearing and neither is an optimization:

    * **A `steer_rejected` is never retried automatically.** A screen that did
      not go where the classifier predicted means the classifier is wrong about
      *this build* of this harness. Retrying blind is exactly how a wrong
      keystroke lands in a prompt that was never understood.
    * **An `:unknown` modal state refuses the write** and escalates to a human
      (`15`). Guessing here would convert a display change into an unlogged
      authority decision.

  Steering is mitos-initiated (`05 §5.2`): the agent never calls home, and every
  attempt lands on the log as an Effect like any other authority act.
  """

  alias Mitos.Observation.{Classifier, Clock, Terminal}

  @type outcome ::
          {:ok, %{before: String.t(), after: String.t()}}
          | {:error, {:escalate, atom} | :busy | :unstable | atom}

  @doc """
  Apply one steer command. Required keys: `terminal`, `write` (`fun(binary)`),
  `command`. Optional: `clock`, `classifier`, `append`, `caused_by`,
  `thresholds`, `settle_ms`.
  """
  @spec apply(map) :: outcome
  def apply(%{terminal: terminal, write: write, command: command} = ctx) do
    classifier = ctx[:classifier] || Classifier.Heuristic
    thresholds = Clock.thresholds(ctx[:thresholds] || %{})
    settle = ctx[:settle_ms] || thresholds.steer * 8

    with {:ok, before} <- read(terminal, thresholds.steer, settle),
         class = classifier.classify(before),
         :ok <- refuse_unknown(class),
         {:ok, expected} <- expectation(classifier, class, command),
         {:ok, bytes} <- encode(classifier, class, command),
         :ok <- write.(bytes) do
      settle_and_verify(ctx, classifier, command, before, expected, write, thresholds, settle)
    else
      # Failures before the write: nothing landed, so there is nothing to undo.
      {:error, reason} -> reject(ctx, command, nil, nil, reason, nil)
    end
  end

  defp settle_and_verify(ctx, classifier, command, before, expected, write, thresholds, settle) do
    case read_after(ctx.terminal, thresholds.steer, settle, before, expected) do
      {:ok, after_grid} ->
        verify(ctx, classifier, command, before, after_grid, expected, write)

      # The screen never moved within the settle window. That IS the predicted
      # transition failing to happen — the same finding as a wrong move, and it
      # gets the same treatment: back out, report, escalate, never retry.
      {:error, :unchanged} ->
        write.("\e")
        reject(ctx, command, before, nil, :transition_mismatch, nil)

      {:error, reason} ->
        write.("\e")
        reject(ctx, command, before, nil, reason, nil)
    end
  end

  # --- steps ---

  defp read(terminal, min_quiet, wait, extra \\ []) do
    # `require_content`: a blank screen carries no modal state, and steering
    # against one would be a keystroke into nothing (14 §3.2).
    opts = [min_quiet_ms: min_quiet, wait: wait, require_content: true] ++ extra

    case Terminal.snapshot(terminal, opts) do
      {:ok, grid} -> {:ok, grid}
      {:error, reason} -> {:error, reason}
    end
  end

  # After the write the terminal is quiet until the response arrives. If the
  # expectation entails a change, an identical grid means "not yet", not "the
  # command did nothing" — so wait past it rather than read the pre-write screen
  # and call the transaction a mismatch.
  defp read_after(terminal, min_quiet, wait, before, expected) do
    extra = if Classifier.entails_change?(expected), do: [differs_from: before.digest], else: []
    read(terminal, min_quiet, wait, extra)
  end

  # 14 §3.2: `:unknown` is an answer, and the answer is "ask a human".
  defp refuse_unknown(%{kind: :unknown}), do: {:error, {:escalate, :unknown_modal}}
  defp refuse_unknown(_class), do: :ok

  defp expectation(classifier, class, command) do
    case classifier.transition(class, command) do
      {:ok, expected} -> {:ok, expected}
      :unknown -> {:error, {:escalate, :unpredictable_transition}}
    end
  end

  defp encode(classifier, class, command) do
    case classifier.encode(class, command) do
      {:ok, bytes} -> {:ok, bytes}
      :unsupported -> {:error, {:escalate, :unsupported_command}}
    end
  end

  defp verify(ctx, classifier, command, before, after_grid, expected, write) do
    after_class = classifier.classify(after_grid)

    if Classifier.satisfied?(expected, before, after_grid, after_class) do
      emit(ctx,
        type: :steer_applied,
        ref: %{"command" => inspect(command), "stage_id" => ctx[:stage_id]},
        payload: %{
          "before_digest" => before.digest,
          "after_digest" => after_grid.digest,
          "before_kind" => to_string(before_kind(classifier, before)),
          "after_kind" => to_string(after_class.kind),
          "pty_offset" => after_grid.pty_offset
        }
      )

      {:ok, %{before: before.digest, after: after_grid.digest}}
    else
      # Undo what we can (ESC leaves most modals) and hand the decision up. We
      # do NOT try a different key.
      write.("\e")
      reject(ctx, command, before, after_grid, :transition_mismatch, after_class.kind)
    end
  end

  defp reject(ctx, command, before, after_grid, reason, after_kind) do
    emit(ctx,
      type: :steer_rejected,
      ref: %{"command" => inspect(command), "stage_id" => ctx[:stage_id]},
      payload: %{
        "reason" => to_string(elem_or(reason)),
        "before_digest" => before && before.digest,
        "after_digest" => after_grid && after_grid.digest,
        "after_kind" => after_kind && to_string(after_kind)
      }
    )

    {:error, escalating(reason)}
  end

  defp before_kind(classifier, grid), do: classifier.classify(grid).kind

  # Every failure mode here is a human's problem, not a retry's: an unreadable
  # screen, an unpredictable transition, or a write that did not land.
  defp escalating({:escalate, _} = e), do: e
  defp escalating(reason), do: {:escalate, reason}

  defp elem_or({:escalate, r}), do: r
  defp elem_or(r), do: r

  defp emit(%{append: append} = ctx, fields) when is_function(append, 1) do
    fields = if ctx[:caused_by], do: Keyword.put(fields, :caused_by, ctx[:caused_by]), else: fields
    append.(fields)
  end

  defp emit(_ctx, _fields), do: :ok
end
