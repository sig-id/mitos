defmodule Mitos.SandboxProvider.RemoteMicrovm do
  @moduledoc """
  Generic HTTP-backed remote microVM provider.

  This is the BEAM-side client seam for T2 providers. Operators point it at a
  provider service with `:mitos, :remote_microvm_endpoint` or
  `MITOS_REMOTE_MICROVM_ENDPOINT`; the provider service owns the actual E2B or
  Daytona VM lifecycle and returns normalized events plus harvest metadata.
  """

  @behaviour Mitos.SandboxProvider
  @http_timeout_ms 5_000

  @impl true
  def prepare(%{} = launch_spec) do
    with {:ok, endpoint} <- endpoint(),
         :ok <- validate_launch_spec(launch_spec) do
      {:ok, %{endpoint: endpoint, launch_spec: launch_spec}}
    end
  end

  @impl true
  def start(%{endpoint: endpoint, launch_spec: launch_spec}, argv, env) do
    with :ok <- validate_start_env(launch_spec, env),
         {:ok, response} <-
           post_json(endpoint, "/runs", %{
             "launch_spec" => normalize(launch_spec),
             "argv" => Enum.map(List.wrap(argv), &to_string/1),
             "env" => env_map(env)
           }),
         {:ok, remote_run_id} <- remote_run_id(response),
         {:ok, egress_attestation} <- egress_attestation(response, launch_spec) do
      {:ok,
       %{
         endpoint: endpoint,
         remote_run_id: remote_run_id,
         egress_attestation: egress_attestation,
         events: list_or(response["events"]),
         event_cursor: response["event_cursor"] || response["cursor"] || response["next_cursor"],
         stream_poll_ms: positive_integer(response["stream_poll_ms"], 100),
         stream_max_empty_polls: positive_integer(response["stream_max_empty_polls"], 60),
         harvest: harvest_or(response["harvest"]),
         snapshot: snapshot_or(response["snapshot"] || response["snap"])
       }}
    end
  end

  def start(_handle, _argv, _env), do: {:error, :invalid_launch_handle}

  @impl true
  def stream(%{events: events, event_cursor: cursor} = session) when not is_nil(cursor) do
    Stream.resource(
      fn ->
        %{
          session: session,
          pending: Enum.map(list_or(events), &normalize/1),
          cursor: cursor,
          done?: false,
          empty_polls: 0
        }
      end,
      &next_stream_event/1,
      fn _state -> :ok end
    )
  end

  def stream(%{events: events}), do: Enum.map(list_or(events), &normalize/1)

  def stream(_session), do: []

  @impl true
  def control(%{endpoint: endpoint, remote_run_id: remote_run_id}, command)
      when is_binary(remote_run_id) and remote_run_id != "" do
    with {:ok, command} <- control_command(command),
         {:ok, response} <-
           post_json(endpoint, run_path(remote_run_id, "/control"), %{
             "command" => command
           }) do
      case response do
        %{"ok" => false, "error" => error} -> {:error, error}
        %{"ok" => false} -> {:error, :control_rejected}
        _response -> :ok
      end
    end
  end

  def control(_session, _command), do: {:error, :missing_remote_run_id}

  @impl true
  def snapshot(%{snapshot: snapshot}) when is_map(snapshot), do: {:ok, normalize(snapshot)}

  def snapshot(%{endpoint: endpoint, remote_run_id: remote_run_id})
      when is_binary(remote_run_id) do
    with {:ok, response} <-
           post_json(endpoint, run_path(remote_run_id, "/snapshot"), %{}),
         :ok <- remote_ok(response),
         %{} = snapshot <- payload_map(response, ["snapshot", "snap"]) do
      {:ok, normalize(snapshot)}
    else
      nil -> {:error, :snapshot_missing}
      {:error, reason} -> {:error, reason}
      _other -> {:error, :snapshot_missing}
    end
  end

  def snapshot(_session), do: {:error, :snapshot_missing}

  @impl true
  def harvest(%{harvest: harvest}) when is_map(harvest), do: {:ok, normalize_harvest(harvest)}

  def harvest(%{endpoint: endpoint, remote_run_id: remote_run_id})
      when is_binary(remote_run_id) do
    with {:ok, response} <-
           post_json(endpoint, run_path(remote_run_id, "/harvest"), %{}),
         :ok <- remote_ok(response),
         %{} = harvest <- payload_map(response, ["harvest"]) do
      {:ok, normalize_harvest(harvest)}
    else
      nil -> {:error, :harvest_missing}
      {:error, reason} -> {:error, reason}
      _other -> {:error, :harvest_missing}
    end
  end

  def harvest(_session), do: {:error, :harvest_missing}

  @impl true
  def stop(%{endpoint: endpoint, remote_run_id: remote_run_id}) when is_binary(remote_run_id) do
    _ = post_json(endpoint, run_path(remote_run_id, "/stop"), %{})
    :ok
  end

  def stop(_session), do: :ok

  @impl true
  def capabilities do
    %{
      isolation: :microvm,
      remote?: true,
      egress: :broker,
      data_mount: :snapshot,
      metering: [:cpu, :mem, :wall, :egress],
      snapshot?: true,
      net_modes: [:broker_proxy]
    }
  end

  @impl true
  def child_spec(_opts) do
    %{
      id: __MODULE__,
      start: {Task, :start_link, [fn -> :ok end]},
      restart: :temporary
    }
  end

  defp endpoint do
    endpoint =
      Application.get_env(:mitos, :remote_microvm_endpoint) ||
        System.get_env("MITOS_REMOTE_MICROVM_ENDPOINT")

    if is_binary(endpoint) and endpoint != "" do
      {:ok, String.trim_trailing(endpoint, "/")}
    else
      {:error, :remote_microvm_endpoint_not_configured}
    end
  end

  defp validate_launch_spec(%{} = launch_spec) do
    if t2?(launch_spec) do
      with :ok <- validate_t2_posture(launch_spec),
           :ok <- validate_t2_workspace(launch_spec),
           :ok <- validate_t2_data(launch_spec) do
        :ok
      end
    else
      :ok
    end
  end

  defp validate_t2_posture(launch_spec) do
    posture = field(launch_spec, :posture, %{})
    workspace = field(posture, :workspace, %{})
    data = field(posture, :data, %{})

    cond do
      not is_map(posture) ->
        {:error, {:invalid_t2_launch_spec, "posture is required"}}

      field(posture, :net_mode) != "deny_all_except_broker" ->
        {:error, {:invalid_t2_launch_spec, "net_mode must be deny_all_except_broker"}}

      field(posture, :egress) not in ["broker", :broker] ->
        {:error, {:invalid_t2_launch_spec, "egress must be broker"}}

      field(posture, :host_network) != false ->
        {:error, {:invalid_t2_launch_spec, "host_network must be false"}}

      field(posture, :loopback_bypass) != false ->
        {:error, {:invalid_t2_launch_spec, "loopback_bypass must be false"}}

      blank?(field(posture, :broker_endpoint)) ->
        {:error, {:invalid_t2_launch_spec, "broker_endpoint is required"}}

      not is_map(workspace) or field(workspace, :host_bind) != false ->
        {:error, {:invalid_t2_launch_spec, "workspace host_bind must be false"}}

      not truthy?(field(workspace, :ephemeral)) ->
        {:error, {:invalid_t2_launch_spec, "workspace must be ephemeral"}}

      not is_map(data) or field(data, :host_bind) != false ->
        {:error, {:invalid_t2_launch_spec, "data host_bind must be false"}}

      field(data, :mode) != "ro" ->
        {:error, {:invalid_t2_launch_spec, "data mode must be ro"}}

      field(data, :source) != "snapshot_or_volume" ->
        {:error, {:invalid_t2_launch_spec, "data source must be snapshot_or_volume"}}

      true ->
        :ok
    end
  end

  defp validate_t2_workspace(launch_spec) do
    workspace = field(launch_spec, :workspace)

    cond do
      not blank?(field(launch_spec, :cwd)) ->
        {:error, {:invalid_t2_launch_spec, "T2 cwd must be provider-internal"}}

      workspace in [nil, ""] ->
        :ok

      not is_map(workspace) ->
        {:error, {:invalid_t2_launch_spec, "workspace must be an object"}}

      truthy?(field(workspace, :host_bind)) or not blank?(field(workspace, :bind)) or
          not blank?(field(workspace, :path)) ->
        {:error, {:invalid_t2_launch_spec, "T2 workspace must not use host bind/path mounts"}}

      true ->
        :ok
    end
  end

  defp validate_t2_data(launch_spec) do
    data = field(launch_spec, :data)

    cond do
      data in [nil, ""] ->
        :ok

      not is_map(data) ->
        {:error, {:invalid_t2_launch_spec, "data must be a snapshot or volume object"}}

      map_size(data) == 0 ->
        :ok

      truthy?(field(data, :host_bind)) or not blank?(field(data, :bind)) or
          not blank?(field(data, :path)) ->
        {:error, {:invalid_t2_launch_spec, "T2 data must not use host bind/path mounts"}}

      blank?(field(data, :snapshot)) and blank?(field(data, :volume)) ->
        {:error, {:invalid_t2_launch_spec, "T2 data requires snapshot or volume"}}

      field(data, :ro, true) != true ->
        {:error, {:invalid_t2_launch_spec, "T2 data must be read-only"}}

      true ->
        :ok
    end
  end

  defp validate_start_env(launch_spec, env) do
    if t2?(launch_spec) and map_size(env_map(env)) > 0 do
      {:error, {:invalid_t2_launch_spec, "T2 starts must not inject env"}}
    else
      :ok
    end
  end

  defp post_json(endpoint, path, body) do
    uri = URI.parse(endpoint <> path)
    encoded = Jason.encode!(body)

    case connect(uri) do
      {:ok, socket, transport} ->
        try do
          with :ok <- send_request(socket, transport, uri, encoded),
               {:ok, status, response_body} <- read_response(socket, transport) do
            decode_response(status, response_body)
          end
        after
          close(socket, transport)
        end

      {:error, reason} ->
        {:error, {:remote_provider_unavailable, reason}}
    end
  end

  defp next_stream_event(%{done?: true, pending: []} = state), do: {:halt, state}

  defp next_stream_event(%{pending: [event | rest]} = state) do
    event = normalize(event)
    done? = terminal_event?(event)
    {[event], %{state | pending: rest, done?: done?}}
  end

  defp next_stream_event(%{session: session, cursor: cursor} = state) do
    case poll_events(session, cursor) do
      {:ok, events, next_cursor, done?} ->
        events = Enum.map(events, &normalize/1)
        done? = done? or Enum.any?(events, &terminal_event?/1)

        cond do
          events != [] ->
            next_stream_event(%{
              state
              | pending: events,
                cursor: next_cursor || cursor,
                done?: done?,
                empty_polls: 0
            })

          done? ->
            {:halt, %{state | cursor: next_cursor || cursor, done?: true}}

          state.empty_polls + 1 >= stream_max_empty_polls(session) ->
            event = %{
              "type" => "ERROR",
              "kind" => "stream",
              "detail" => "remote event stream timed out"
            }

            {[event], %{state | done?: true}}

          true ->
            Process.sleep(stream_poll_ms(session))

            next_stream_event(%{
              state
              | cursor: next_cursor || cursor,
                empty_polls: state.empty_polls + 1
            })
        end

      {:error, reason} ->
        event = %{"type" => "ERROR", "kind" => "stream", "detail" => inspect(reason)}
        {[event], %{state | done?: true}}
    end
  end

  defp poll_events(%{endpoint: endpoint, remote_run_id: remote_run_id}, cursor)
       when is_binary(remote_run_id) do
    body = if cursor in [nil, ""], do: %{}, else: %{"cursor" => cursor}

    with {:ok, response} <- post_json(endpoint, run_path(remote_run_id, "/events"), body),
         :ok <- remote_ok(response) do
      events = response["events"] || response["items"] || []
      next_cursor = response["next_cursor"] || response["cursor"] || response["event_cursor"]
      done? = truthy?(response["done"] || response["complete"] || response["terminal"])
      {:ok, list_or(events), next_cursor, done?}
    end
  end

  defp poll_events(_session, _cursor), do: {:error, :missing_remote_run_id}

  defp run_path(remote_run_id, suffix) do
    "/runs/#{path_segment(remote_run_id)}#{suffix}"
  end

  defp path_segment(value) do
    value
    |> to_string()
    |> URI.encode(&URI.char_unreserved?/1)
  end

  defp terminal_event?(%{"type" => type}) when type in ["exit", "PROCESS_EXITED", "sidecar_exit"],
    do: true

  defp terminal_event?(_event), do: false

  defp stream_poll_ms(session), do: positive_integer(session[:stream_poll_ms], 100)

  defp stream_max_empty_polls(session), do: positive_integer(session[:stream_max_empty_polls], 60)

  defp connect(%URI{scheme: scheme, host: host} = uri)
       when scheme in ["http", "https"] and is_binary(host) do
    port = uri.port || default_port(scheme)

    if scheme == "https" do
      _ = Application.ensure_all_started(:ssl)

      case :ssl.connect(
             String.to_charlist(host),
             port,
             [:binary, active: false],
             @http_timeout_ms
           ) do
        {:ok, socket} -> {:ok, socket, :ssl}
        {:error, reason} -> {:error, reason}
      end
    else
      case :gen_tcp.connect(
             String.to_charlist(host),
             port,
             [
               :binary,
               packet: :raw,
               active: false
             ],
             @http_timeout_ms
           ) do
        {:ok, socket} -> {:ok, socket, :tcp}
        {:error, reason} -> {:error, reason}
      end
    end
  end

  defp connect(%URI{scheme: scheme}), do: {:error, {:unsupported_scheme, scheme}}

  defp default_port("http"), do: 80
  defp default_port("https"), do: 443

  defp send_request(socket, transport, uri, body) do
    request = [
      "POST ",
      request_target(uri),
      " HTTP/1.1\r\n",
      "host: ",
      host_header(uri),
      "\r\n",
      "content-type: application/json\r\n",
      "content-length: ",
      Integer.to_string(byte_size(body)),
      "\r\n",
      "connection: close\r\n",
      "\r\n",
      body
    ]

    send_data(socket, transport, request)
  end

  defp request_target(%URI{path: nil, query: nil}), do: "/"
  defp request_target(%URI{path: "", query: nil}), do: "/"
  defp request_target(%URI{path: nil, query: query}), do: "/?" <> query
  defp request_target(%URI{path: "", query: query}), do: "/?" <> query
  defp request_target(%URI{path: path, query: nil}), do: path
  defp request_target(%URI{path: path, query: query}), do: path <> "?" <> query

  defp host_header(%URI{scheme: scheme, host: host, port: port}) do
    if port in [nil, default_port(scheme)] do
      host
    else
      host <> ":" <> Integer.to_string(port)
    end
  end

  defp read_response(socket, transport, acc \\ "") do
    case recv_data(socket, transport) do
      {:ok, chunk} ->
        acc = acc <> chunk

        case parse_response(acc) do
          {:ok, status, body} -> {:ok, status, body}
          :more -> read_response(socket, transport, acc)
        end

      {:error, :closed} ->
        case parse_response(acc) do
          :more -> {:error, :incomplete_remote_response}
          parsed -> parsed
        end

      {:error, reason} ->
        {:error, {:remote_provider_unavailable, reason}}
    end
  end

  defp parse_response(bytes) do
    case :binary.match(bytes, "\r\n\r\n") do
      {header_size, 4} ->
        header = binary_part(bytes, 0, header_size)
        body_offset = header_size + 4
        body = binary_part(bytes, body_offset, byte_size(bytes) - body_offset)
        [status_line | header_lines] = String.split(header, "\r\n")

        with {:ok, status} <- status_code(status_line),
             content_length <- content_length(header_lines),
             true <- content_length == nil or byte_size(body) >= content_length do
          body =
            if is_integer(content_length) do
              binary_part(body, 0, content_length)
            else
              body
            end

          {:ok, status, body}
        else
          false -> :more
          {:error, reason} -> {:error, reason}
        end

      :nomatch ->
        :more
    end
  end

  defp status_code(status_line) do
    case String.split(status_line, " ", parts: 3) do
      [_http, status | _] ->
        case Integer.parse(status) do
          {status, ""} -> {:ok, status}
          _error -> {:error, :invalid_remote_status}
        end

      _other ->
        {:error, :invalid_remote_status}
    end
  end

  defp content_length(header_lines) do
    Enum.find_value(header_lines, fn line ->
      case String.split(line, ":", parts: 2) do
        [name, value] ->
          if String.downcase(name) == "content-length" do
            value |> String.trim() |> String.to_integer()
          end

        _other ->
          nil
      end
    end)
  end

  defp decode_response(status, response_body) when status in 200..299 do
    case Jason.decode(response_body) do
      {:ok, %{} = decoded} -> {:ok, decoded}
      {:ok, _other} -> {:error, :invalid_remote_response}
      {:error, error} -> {:error, {:invalid_remote_json, Exception.message(error)}}
    end
  end

  defp decode_response(status, response_body),
    do: {:error, {:remote_provider_error, status, response_body}}

  defp remote_ok(%{"ok" => false, "error" => error}), do: {:error, error}
  defp remote_ok(%{"ok" => false}), do: {:error, :remote_provider_rejected}
  defp remote_ok(_response), do: :ok

  defp payload_map(%{} = response, keys) do
    keys
    |> Enum.find_value(fn key ->
      case response[key] do
        %{} = payload -> payload
        _other -> nil
      end
    end)
    |> case do
      %{} = payload ->
        payload

      nil ->
        if Map.has_key?(response, "ok") do
          stripped = Map.drop(response, ["ok"])
          if map_size(stripped) > 0, do: stripped
        else
          response
        end
    end
  end

  defp remote_run_id(response) do
    case response["run_id"] || response["id"] do
      value when is_binary(value) ->
        value = String.trim(value)
        if value == "", do: {:error, :missing_remote_run_id}, else: {:ok, value}

      _other ->
        {:error, :missing_remote_run_id}
    end
  end

  defp egress_attestation(response, launch_spec) do
    attestation = response_egress_attestation(response)

    cond do
      not t2?(launch_spec) ->
        {:ok, if(is_map(attestation), do: normalize(attestation), else: nil)}

      not is_map(attestation) ->
        {:error, {:egress_attestation_required, "T2 provider must attest broker-only egress"}}

      true ->
        validate_t2_egress_attestation(attestation, launch_spec)
    end
  end

  defp response_egress_attestation(%{} = response) do
    cond do
      is_map(response["egress_attestation"]) ->
        response["egress_attestation"]

      is_map(response["network_attestation"]) ->
        response["network_attestation"]

      is_map(response["attestation"]) and is_map(response["attestation"]["egress"]) ->
        response["attestation"]["egress"]

      is_map(response["attestations"]) and is_map(response["attestations"]["egress"]) ->
        response["attestations"]["egress"]

      true ->
        nil
    end
  end

  defp response_egress_attestation(_response), do: nil

  defp validate_t2_egress_attestation(attestation, launch_spec) do
    attestation = normalize(attestation)
    posture = field(launch_spec, :posture, %{})
    expected_broker_endpoint = normalize_endpoint(field(posture, :broker_endpoint))
    attested_broker_endpoint = normalize_endpoint(field(attestation, :broker_endpoint))

    cond do
      not truthy?(field(attestation, :enforced)) ->
        invalid_egress_attestation("egress enforcement must be true")

      field(attestation, :egress) not in ["broker", :broker] ->
        invalid_egress_attestation("egress must be broker")

      field(attestation, :net_mode) != "deny_all_except_broker" ->
        invalid_egress_attestation("net_mode must be deny_all_except_broker")

      not truthy?(field(attestation, :deny_all_else)) ->
        invalid_egress_attestation("deny_all_else must be true")

      field(attestation, :host_network) != false ->
        invalid_egress_attestation("host_network must be false")

      field(attestation, :loopback_bypass) != false ->
        invalid_egress_attestation("loopback_bypass must be false")

      egress_mechanism(attestation) not in [
        "forced_proxy",
        "network_policy",
        "firewall_allowlist",
        "broker_proxy"
      ] ->
        invalid_egress_attestation("mechanism must be forced_proxy or network_policy")

      blank?(attested_broker_endpoint) ->
        invalid_egress_attestation("broker_endpoint is required")

      attested_broker_endpoint != expected_broker_endpoint ->
        invalid_egress_attestation("broker_endpoint must match launch posture")

      true ->
        {:ok, attestation}
    end
  end

  defp invalid_egress_attestation(reason), do: {:error, {:invalid_egress_attestation, reason}}

  defp egress_mechanism(attestation) do
    field(attestation, :mechanism) || field(attestation, :mode) || field(attestation, :net_mode)
  end

  defp normalize_endpoint(endpoint) when is_binary(endpoint) do
    endpoint
    |> String.trim()
    |> String.trim_trailing("/")
  end

  defp normalize_endpoint(_endpoint), do: nil

  defp send_data(socket, :tcp, request), do: :gen_tcp.send(socket, request)
  defp send_data(socket, :ssl, request), do: :ssl.send(socket, request)

  defp recv_data(socket, :tcp), do: :gen_tcp.recv(socket, 0, @http_timeout_ms)
  defp recv_data(socket, :ssl), do: :ssl.recv(socket, 0, @http_timeout_ms)

  defp close(socket, :tcp), do: :gen_tcp.close(socket)
  defp close(socket, :ssl), do: :ssl.close(socket)

  defp env_map(nil), do: %{}

  defp env_map(%{} = env) do
    Map.new(env, fn {key, value} -> {to_string(key), to_string(value)} end)
  end

  defp env_map(env) when is_list(env) do
    Enum.flat_map(env, fn
      {key, value} -> [{to_string(key), to_string(value)}]
      %{"name" => key, "value" => value} -> [{to_string(key), to_string(value)}]
      %{name: key, value: value} -> [{to_string(key), to_string(value)}]
      _ -> []
    end)
    |> Map.new()
  end

  defp env_map(_env), do: %{}

  defp list_or(list) when is_list(list), do: Enum.filter(list, &is_map/1)
  defp list_or(_other), do: []

  defp positive_integer(value, _default) when is_integer(value) and value > 0, do: value

  defp positive_integer(value, default) when is_binary(value) do
    case Integer.parse(value) do
      {integer, ""} when integer > 0 -> integer
      _other -> default
    end
  end

  defp positive_integer(_value, default), do: default

  defp truthy?(value), do: value in [true, "true", 1, "1", "yes", "done"]

  defp t2?(launch_spec), do: field(launch_spec, :trust_tier) in ["T2", :T2]

  defp field(map, key, default \\ nil)

  defp field(%{} = map, key, default) do
    Map.get(map, key, Map.get(map, to_string(key), default))
  end

  defp field(_value, _key, default), do: default

  defp blank?(value) when is_binary(value), do: String.trim(value) == ""
  defp blank?(value), do: value in [nil, ""]

  defp harvest_or(%{} = harvest), do: harvest
  defp harvest_or(_other), do: nil

  defp snapshot_or(%{} = snapshot), do: snapshot
  defp snapshot_or(_other), do: nil

  defp control_command(%{} = command), do: {:ok, normalize(command)}

  defp control_command({:write_stdin, bytes}) when is_binary(bytes) do
    {:ok,
     %{
       "cmd" => "write_stdin",
       "bytes" => Base.encode64(bytes),
       "encoding" => "base64"
     }}
  end

  defp control_command({:steer, interaction, opts})
       when (is_atom(interaction) or is_binary(interaction)) and is_list(opts) do
    {:ok,
     opts
     |> control_opts()
     |> Map.merge(%{"cmd" => "steer", "interaction" => to_string(interaction)})}
  end

  defp control_command({:resize, cols, rows}) when is_integer(cols) and is_integer(rows) do
    {:ok, %{"cmd" => "resize", "cols" => cols, "rows" => rows}}
  end

  defp control_command({:signal, signum}) when is_integer(signum) do
    {:ok, %{"cmd" => "signal", "signal" => signum}}
  end

  defp control_command({:shutdown, grace_ms}) when is_integer(grace_ms) do
    {:ok, %{"cmd" => "shutdown", "grace_ms" => grace_ms}}
  end

  defp control_command(_command), do: {:error, :unsupported_control}

  defp control_opts(opts) do
    opts
    |> Enum.flat_map(fn
      {key, value} -> [{to_string(key), normalize(value)}]
      _other -> []
    end)
    |> Map.new()
  end

  defp normalize_harvest(%{} = harvest) do
    %{
      diff: map_or(harvest["diff"] || harvest[:diff], %{"empty?" => true, "files_changed" => []}),
      result: map_or(harvest["result"] || harvest[:result], %{"signal" => "unknown"}),
      artifacts: list_or(harvest["artifacts"] || harvest[:artifacts] || [])
    }
  end

  defp map_or(%{} = map, _default), do: normalize(map)
  defp map_or(_other, default), do: default

  defp normalize(%{} = map),
    do: Map.new(map, fn {key, value} -> {to_string(key), normalize(value)} end)

  defp normalize(list) when is_list(list), do: Enum.map(list, &normalize/1)
  defp normalize(value), do: value
end
