defmodule Mitos.Sidecar.Protocol do
  @moduledoc """
  Wire-format constants for the `mitos-term` sidecar.

  The Erlang Port is opened with `packet: 4`, so the length prefix is handled
  by the runtime. What we read/write is the payload: a 1-byte stream tag
  followed by the body (raw PTY bytes or MessagePack).

  Stream tag values are pinned to `src/sidecar/protocol.zig`:

      0x01 RAW       opaque PTY bytes               sidecar -> BEAM
      0x02 EVENT     MessagePack-encoded event map  sidecar -> BEAM
      0x03 SNAPSHOT  MessagePack-encoded snapshot   sidecar -> BEAM
      0x10 CONTROL   MessagePack-encoded command    BEAM    -> engine
  """

  @raw 0x01
  @event 0x02
  @snapshot 0x03
  @control 0x10

  def raw, do: @raw
  def event, do: @event
  def snapshot, do: @snapshot
  def control, do: @control

  @typedoc """
  A frame parsed from the sidecar's output stream: the stream tag plus the
  unframed body.
  """
  @type frame ::
          {:raw, binary()}
          | {:event, map()}
          | {:snapshot, map()}
          | {:unknown, byte(), binary()}

  @doc """
  Parse one frame payload emitted by the sidecar. The `{:packet, 4}` port
  driver has already stripped the length prefix.
  """
  @spec decode_frame(binary()) :: frame()
  def decode_frame(<<@raw, body::binary>>), do: {:raw, body}

  def decode_frame(<<@event, body::binary>>) do
    {:event, Msgpax.unpack!(body, binary: true)}
  end

  def decode_frame(<<@snapshot, body::binary>>) do
    {:snapshot, Msgpax.unpack!(body, binary: true)}
  end

  def decode_frame(<<tag, rest::binary>>), do: {:unknown, tag, rest}

  @doc """
  Encode a control command (map) into a payload ready for `Port.command/2`.
  The map must include the `"cmd"` key. Commands use the layering protocol
  vocabulary (`OPEN`, `INPUT`, `RESIZE`, ...); legacy lowercase helpers remain
  accepted by the sidecar while callers migrate.
  """
  @spec encode_control(map()) :: iodata()
  def encode_control(%{} = command) do
    [@control, Msgpax.pack!(stringify_keys(command), iodata: true)]
  end

  # Msgpax serializes atoms as strings only when we pre-stringify; otherwise
  # nested maps with atom keys would round-trip back as atoms on the wire and
  # the sidecar (which keys on strings) would not recognize them.
  defp stringify_keys(value) when is_map(value) and not is_struct(value) do
    Map.new(value, fn {k, v} -> {to_key(k), stringify_keys(v)} end)
  end

  defp stringify_keys(value) when is_list(value), do: Enum.map(value, &stringify_keys/1)
  defp stringify_keys(value), do: value

  defp to_key(k) when is_atom(k), do: Atom.to_string(k)
  defp to_key(k) when is_binary(k), do: k

  # -- helpers for building common commands ---------------------------------

  def hello, do: %{cmd: "hello"}

  def open(argv, opts \\ []) do
    %{
      cmd: "open",
      argv: argv,
      cols: Keyword.get(opts, :cols, 80),
      rows: Keyword.get(opts, :rows, 24)
    }
    |> maybe_put(:cwd, opts[:cwd])
    |> maybe_put(:env, opts[:env])
    |> maybe_put(:idle_ms, opts[:idle_ms])
  end

  def open_launch(%{} = launch_spec, opts \\ []) do
    launch_spec =
      launch_spec
      |> Map.put_new(:version, 1)
      |> maybe_put(:cols, opts[:cols])
      |> maybe_put(:rows, opts[:rows])
      |> maybe_put(:cwd, opts[:cwd])
      |> maybe_put(:env, opts[:env])
      |> maybe_put(:idle_ms, opts[:idle_ms])

    %{cmd: "OPEN", launch_spec: launch_spec}
  end

  def write_stdin(bytes) when is_binary(bytes),
    do: %{cmd: "write_stdin", bytes: %Msgpax.Bin{data: bytes}}

  def input(bytes) when is_binary(bytes),
    do: %{cmd: "INPUT", bytes: %Msgpax.Bin{data: bytes}}

  def resize(cols, rows), do: %{cmd: "resize", cols: cols, rows: rows}
  def signal(signum), do: %{cmd: "signal", signum: signum}
  def authorize(action_id, token), do: %{cmd: "AUTHORIZE", action_id: action_id, token: token}
  def deny(action_id, reason), do: %{cmd: "DENY", action_id: action_id, reason: reason}
  def snapshot_cmd(_opts \\ []), do: %{cmd: "snapshot"}
  def snapshot_request, do: %{cmd: "SNAPSHOT_REQUEST"}
  def cancel(reason \\ "cancelled"), do: %{cmd: "CANCEL", reason: reason}
  def shutdown(grace_ms \\ 100), do: %{cmd: "shutdown", grace_ms: grace_ms}

  defp maybe_put(map, _key, nil), do: map
  defp maybe_put(map, key, value), do: Map.put(map, key, value)
end
