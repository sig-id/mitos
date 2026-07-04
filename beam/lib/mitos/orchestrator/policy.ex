defmodule Mitos.Orchestrator.Policy do
  @moduledoc """
  The orchestrator seam (04 §3). The orchestrator's sole output is ONE
  Decision per turn (04 §3.2); the substrate assembles its inputs (04 §3.1)
  and executes the Decision through deterministic mechanisms.

  `ctx` carries exactly the 04 §3.1 inputs — never raw harness transcripts:

      %{
        intent:       %Mitos.Pipeline{},          # the markdown pipeline intent
        state:        %Mitos.Run.State{},          # fold(event log)
        budget:       %{"usd" => _, ...},          # the run ceiling
        remaining:    %{usd: _, tokens: _, wall_ms: _},
        capabilities: %{name => %Mitos.Capability.Manifest{}},
        config:       map()
      }

  An LLM-backed orchestrator implements this same behaviour with a model call
  (04 §3.4: one call → one Decision); the MVP ships the deterministic
  `Linear` policy (09 §2: single linear pipeline, no branching).
  """

  @callback decide(ctx :: map()) :: Mitos.Event.Decision.t()
end
