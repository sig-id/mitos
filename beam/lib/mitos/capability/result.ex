defmodule Mitos.Capability.Result do
  @moduledoc """
  The soft result + evidence a capability emits (03 §7). A capability writes a
  `RESULT` artifact to a known workspace path; the runtime harvests it at exit.

  `signal` is SOFT and ADVISORY — it MUST NOT directly drive control flow; the
  orchestrator weighs `signal` + `evidence` + `metrics` + `trace` before deciding
  (03 §7, 01 §5). Missing/invalid RESULT ⇒ synthetic `signal: "unknown"`; the
  orchestrator handles it, never the substrate.
  """

  # Conventional path inside the workspace where a capability drops its result.
  @result_filename "RESULT.json"

  def result_filename, do: @result_filename

  @doc "Parse a RESULT artifact body into a normalized map. Never raises."
  def parse(nil), do: synthetic("no RESULT artifact produced")

  def parse(body) when is_binary(body) do
    case Mitos.JSON.decode(body) do
      {:ok, %{} = map} -> normalize(map)
      _ -> synthetic("invalid RESULT artifact (unparseable)")
    end
  end

  @doc "Read + parse the RESULT artifact from a workspace directory."
  def harvest(workspace_dir) do
    path = Path.join(workspace_dir, @result_filename)

    case File.read(path) do
      {:ok, body} -> parse(body)
      {:error, _} -> synthetic("no RESULT artifact at #{@result_filename}")
    end
  end

  defp normalize(map) do
    %{
      "signal" => map["signal"] || "unknown",
      "confidence" => map["confidence"] || 0.0,
      "summary" => map["summary"] || "",
      "metrics" => map["metrics"] || %{},
      "evidence" => normalize_evidence(map["evidence"] || []),
      "needs" => map["needs"] || []
    }
  end

  # Evidence entries MUST be content-addressed (03 §7). Fill sha256 where a body
  # is inlined; otherwise trust the declared hash.
  defp normalize_evidence(list) when is_list(list) do
    Enum.map(list, fn
      %{} = e -> Map.take(e, ["kind", "path", "artifact", "sha256"])
      other -> %{"kind" => "unknown", "value" => other}
    end)
  end

  defp normalize_evidence(_), do: []

  defp synthetic(reason) do
    %{
      "signal" => "unknown",
      "confidence" => 0.0,
      "summary" => reason,
      "metrics" => %{},
      "evidence" => [],
      "needs" => []
    }
  end
end
