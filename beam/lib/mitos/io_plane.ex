defmodule Mitos.IOPlane do
  @moduledoc """
  Backend I/O-plane effects for application ingress and delivery.

  This module implements the local first cut of `spec/10_IO_PLANE.md`: app runs
  enter through a connector-shaped signal and successful app runs emit sink
  delivery Effects. The actual connector/sink credentials remain outside the
  sandbox and outside app manifests.
  """

  alias Mitos.AppManifest
  alias Mitos.Trace

  @sensitive_binding_key ~r/(secret|token|api[_-]?key|password|credential|private[_-]?key|authorization|bearer)/i
  @sensitive_url_query_key ~r/(secret|token|api[_-]?key|password|credential|private[_-]?key|authorization|bearer|signature|sig)/i
  @terminal_on_values [nil, "", "run.done", "task_done", "capability_finished"]

  @doc "Append a canonical ingress Effect for the signal that created an app Task."
  def append_signal_received(%Trace{} = trace, %AppManifest{} = app, %{} = task, opts \\ []) do
    connector = connector_name(task, opts)
    entrypoint = get_in(task, ["provenance", "entrypoint"])
    payload = task["payload"] || %{}
    idempotency_key = idempotency_key(task, connector, opts)
    reply_ref = Keyword.get(opts, :reply_ref) || payload["reply_ref"]

    Trace.append_effect(trace, %{
      type: "signal_received",
      caused_by: 0,
      ref:
        %{
          "connector" => connector,
          "idempotency_key" => idempotency_key,
          "app_id" => app.app_id,
          "entrypoint" => entrypoint,
          "task_id" => task["id"]
        }
        |> drop_nil(),
      payload:
        %{
          "connector" => connector,
          "source" => task["source"],
          "intake" =>
            %{
              "intent" => payload["intent"],
              "workspace" => payload["workspace"],
              "input_keys" => payload |> Map.get("inputs", %{}) |> input_keys(),
              "reply_ref" => reply_ref,
              "idempotency_key" => idempotency_key
            }
            |> drop_nil()
        }
        |> drop_nil()
    })
  end

  @doc "Append sink delivery Effects for a completed app run."
  def append_deliveries(
        %Trace{} = trace,
        %AppManifest{} = app,
        %{} = task,
        %{} = pipeline_result,
        opts \\ []
      ) do
    caused_by = Keyword.get(opts, :caused_by, 0)

    app
    |> output_bindings(task, opts)
    |> Enum.reduce({trace, []}, fn binding, {trace, deliveries} ->
      if terminal_binding?(binding) do
        case append_delivery(trace, app, task, pipeline_result, binding, caused_by) do
          {trace, nil} -> {trace, deliveries}
          {trace, delivery} -> {trace, deliveries ++ [delivery]}
        end
      else
        {trace, deliveries}
      end
    end)
  end

  defp append_delivery(trace, app, task, pipeline_result, binding, caused_by) do
    before_seq = trace.seq
    sink = string_or(binding["sink"], "web")
    entrypoint = get_in(task, ["provenance", "entrypoint"])
    raw_destination = destination(binding, sink, task)
    public_destination = public_destination(raw_destination)

    ref =
      delivery_ref(trace.run_id, app, task, binding, sink, raw_destination, public_destination)

    payload =
      render_payload(trace.run_id, app, task, pipeline_result, binding, sink, public_destination)

    trace =
      Trace.append_effect(trace, %{
        type: "delivery_attempted",
        caused_by: caused_by,
        ref: ref,
        payload: %{
          "sink" => sink,
          "entrypoint" => entrypoint,
          "on" => binding["on"] || "run.done",
          "destination" => public_destination,
          "binding" => public_binding(binding)
        }
      })

    receipt = %{
      "delivery_id" => ref["delivery_id"],
      "sink" => sink,
      "destination" => public_destination,
      "status" => "sent"
    }

    trace =
      Trace.append_effect(trace, %{
        type: "delivery_sent",
        caused_by: caused_by,
        ref: ref,
        payload: %{"receipt" => receipt, "rendered" => payload}
      })

    delivery =
      if trace.seq == before_seq do
        nil
      else
        %{"receipt" => receipt, "payload" => payload}
      end

    {trace, delivery}
  end

  defp output_bindings(%AppManifest{} = app, task, opts) do
    entrypoint = get_in(task, ["provenance", "entrypoint"])

    declared_outputs =
      opts
      |> Keyword.get(:io_outputs)
      |> case do
        values when is_list(values) -> stringify_list(values)
        _other -> get_in(app.io, ["outputs"]) || []
      end

    outputs =
      declared_outputs
      |> Enum.filter(&is_map/1)
      |> Enum.filter(fn binding ->
        binding["entrypoint"] in [nil, "", entrypoint]
      end)

    if declared_outputs == [], do: default_outputs(entrypoint), else: outputs
  end

  defp default_outputs(entrypoint) do
    [
      %{
        "sink" => "web",
        "entrypoint" => entrypoint,
        "on" => "run.done",
        "default?" => true
      }
    ]
  end

  defp terminal_binding?(%{"on" => on}), do: on in @terminal_on_values
  defp terminal_binding?(_binding), do: true

  defp render_payload(run_id, app, task, pipeline_result, binding, sink, destination) do
    %{
      "run_id" => run_id,
      "app_id" => app.app_id,
      "app_name" => app.name,
      "task_id" => task["id"],
      "entrypoint" => get_in(task, ["provenance", "entrypoint"]),
      "pipeline_id" => task["pipeline"],
      "summary" => pipeline_result["deliverable_summary"],
      "status" => pipeline_result["status"],
      "sink" => sink,
      "destination" => destination,
      "reply_ref" => get_in(task, ["payload", "reply_ref"]),
      "render" => binding["render"],
      "result" => %{
        "summary" => pipeline_result["deliverable_summary"],
        "steps" => get_in(pipeline_result, ["evidence", "steps"]) || []
      },
      "artifacts" => [],
      "proof_excerpt" => %{
        "app_run_id" => run_id,
        "pipeline_execution_id" => pipeline_result["execution_id"],
        "step_run_ids" => get_in(pipeline_result, ["evidence", "run_ids"]) || []
      }
    }
    |> drop_nil()
  end

  defp delivery_ref(run_id, app, task, binding, sink, raw_destination, public_destination) do
    delivery_id = delivery_id(run_id, sink, raw_destination)

    %{
      "delivery_id" => delivery_id,
      "sink" => sink,
      "dest" => public_destination,
      "app_id" => app.app_id,
      "entrypoint" => get_in(task, ["provenance", "entrypoint"]),
      "task_id" => task["id"],
      "on" => binding["on"] || "run.done"
    }
    |> drop_nil()
  end

  defp delivery_id(run_id, sink, destination) do
    key = "#{run_id}:#{sink}:#{destination}"
    digest = :crypto.hash(:sha256, key) |> Base.encode16(case: :lower)
    "delivery:sha256:" <> digest
  end

  defp destination(binding, sink, task) do
    string_or(
      binding["url"] || binding["path"] || binding["channel"] ||
        reply_destination(binding["reply_to"], task) ||
        binding["destination"],
      sink
    )
  end

  defp reply_destination("input.reply_ref", task), do: get_in(task, ["payload", "reply_ref"])
  defp reply_destination(value, _task), do: value

  defp connector_name(task, opts) do
    Keyword.get(opts, :connector)
    |> string_or(nil)
    |> case do
      nil -> connector_from_source(task["source"])
      connector -> connector
    end
  end

  defp connector_from_source("connector:" <> rest) do
    rest
    |> String.split(":", parts: 2)
    |> List.first()
    |> string_or("manual")
  end

  defp connector_from_source("manual"), do: "manual"
  defp connector_from_source("web"), do: "web"
  defp connector_from_source(_source), do: "manual"

  defp idempotency_key(task, connector, opts) do
    Keyword.get(opts, :idempotency_key)
    |> string_or(nil)
    |> case do
      nil -> "#{connector}:#{task["id"]}"
      key -> key
    end
  end

  @doc "Return a public I/O binding view with credential-shaped fields removed recursively."
  def public_binding(%{} = binding), do: binding |> stringify_value() |> public_value()
  def public_binding(value), do: value

  defp input_keys(%{} = inputs), do: inputs |> Map.keys() |> Enum.sort()
  defp input_keys(_inputs), do: []

  defp public_value(%{} = map) do
    map
    |> Enum.reject(fn {key, _value} -> sensitive_binding_key?(key) end)
    |> Map.new(fn {key, value} -> {key, public_value(value)} end)
  end

  defp public_value(values) when is_list(values), do: Enum.map(values, &public_value/1)
  defp public_value(value) when is_binary(value), do: public_destination(value)
  defp public_value(value), do: value

  defp public_destination(value) when is_binary(value) do
    uri = URI.parse(value)

    if is_binary(uri.scheme) and is_binary(uri.host) do
      uri
      |> Map.put(:userinfo, nil)
      |> Map.put(:query, public_url_query(uri.query))
      |> URI.to_string()
    else
      value
    end
  end

  defp public_destination(value), do: value

  defp public_url_query(nil), do: nil

  defp public_url_query(query) do
    query
    |> String.split("&", trim: false)
    |> Enum.map(&public_url_query_pair/1)
    |> Enum.join("&")
  end

  defp public_url_query_pair(pair) do
    [key | _rest] = String.split(pair, "=", parts: 2)

    if sensitive_url_query_key?(safe_form_decode(key)) do
      key <> "=[redacted]"
    else
      pair
    end
  end

  defp safe_form_decode(value) do
    URI.decode_www_form(value)
  rescue
    ArgumentError -> value
  end

  defp sensitive_binding_key?(key), do: Regex.match?(@sensitive_binding_key, to_string(key))
  defp sensitive_url_query_key?(key), do: Regex.match?(@sensitive_url_query_key, to_string(key))

  defp stringify_list(values), do: Enum.map(values, &stringify_value/1)

  defp stringify_value(%{} = map),
    do: Map.new(map, fn {key, value} -> {to_string(key), stringify_value(value)} end)

  defp stringify_value(values) when is_list(values), do: Enum.map(values, &stringify_value/1)
  defp stringify_value(value), do: value

  defp string_or(value, _default) when is_binary(value) and value != "", do: value
  defp string_or(_value, default), do: default

  defp drop_nil(map), do: Enum.reject(map, fn {_key, value} -> is_nil(value) end) |> Map.new()
end
