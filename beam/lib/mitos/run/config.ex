defmodule Mitos.Run.Config do
  @moduledoc """
  The durable per-run configuration — everything needed to (re)start a run's
  process. Persisted to `<run_dir>/config.json` at first start so crash
  recovery is `resume(run_id)` with no other state (04 §7: the log + this file
  are the whole run). Carries **no secrets** (09 §4): `scope` is the vault
  lookup key, never key material.
  """

  @enforce_keys [:run_id]
  defstruct run_id: nil,
            # tenant stamp — the run_id namespace is "<org>/<id>" (09 §4)
            tenant: %{"org" => "local", "project" => "default"},
            task_id: nil,
            # markdown pipeline intent (04 §8)
            pipeline: "",
            # %{name => capability dir} — manifests are reloaded (content-addressed) at init
            capabilities: %{},
            # input workspace source (dir path) | nil
            workspace: nil,
            # the run's SUBJECT data (01 §8.7 payload.inputs)
            inputs: %{},
            # per-run knobs (01 §8.7 payload.parameters)
            parameters: %{},
            # %{"usd" => _, "tokens" => _, "wall_ms" => _} — declared ceilings only
            budget: %{},
            # broker credential scope (seller org for marketplace runs, 08 §3.2)
            scope: nil,
            # trust-tier override (facilitator forces T2 at publish, 08 §8; MVP T1, 09 §2)
            trust_tier: nil,
            # Mitos.Orchestrator.Policy implementation
            policy: Mitos.Orchestrator.Policy.Linear,
            # facilitator offer key — set on paid runs; triggers settle-on-finish (08 §7)
            offer_key: nil,
            # I/O-plane ingress provenance (10 §3): %{connector: name, intake: %{...}};
            # nil for runs started directly. Carries no secrets. Emits signal_received +
            # task_enqueued at run start (10 §7).
            ingress: nil,
            max_iterations: nil

  @type t :: %__MODULE__{}

  def new(fields) do
    fields = Map.new(fields)

    run_id =
      Map.get(fields, :run_id) ||
        Path.join(get_in(fields, [:tenant, "org"]) || "local", Mitos.id("run"))

    struct!(__MODULE__, Map.put(fields, :run_id, run_id))
  end

  def path(run_id), do: Path.join(Mitos.run_dir(run_id), "config.json")

  def store(%__MODULE__{} = c) do
    File.mkdir_p!(Mitos.run_dir(c.run_id))

    body =
      c
      |> Map.from_struct()
      |> Map.update!(:policy, &Atom.to_string/1)
      |> Mitos.JSON.encode!()

    File.write!(path(c.run_id), body)
    c
  end

  def load(run_id) do
    with {:ok, body} <- File.read(path(run_id)),
         {:ok, %{} = raw} <- Mitos.JSON.decode(body) do
      {:ok,
       %__MODULE__{
         run_id: raw["run_id"],
         tenant: raw["tenant"] || %{},
         task_id: raw["task_id"],
         pipeline: raw["pipeline"] || "",
         capabilities: raw["capabilities"] || %{},
         workspace: raw["workspace"],
         inputs: raw["inputs"] || %{},
         parameters: raw["parameters"] || %{},
         budget: raw["budget"] || %{},
         scope: raw["scope"],
         trust_tier: raw["trust_tier"],
         policy: String.to_existing_atom(raw["policy"] || "Elixir.Mitos.Orchestrator.Policy.Linear"),
         offer_key: raw["offer_key"],
         ingress: raw["ingress"],
         max_iterations: raw["max_iterations"]
       }}
    else
      _ -> {:error, :no_config}
    end
  end
end
