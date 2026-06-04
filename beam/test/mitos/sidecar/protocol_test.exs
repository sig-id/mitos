defmodule Mitos.Sidecar.ProtocolTest do
  use ExUnit.Case, async: true

  alias Mitos.Sidecar.Protocol

  describe "decode_frame/1" do
    test "RAW frame yields raw bytes opaque to us" do
      assert {:raw, "hello"} = Protocol.decode_frame(<<0x01, "hello">>)
    end

    test "EVENT frame is unpacked from msgpack" do
      body = Msgpax.pack!(%{"type" => "idle", "ms" => 250}, iodata: true) |> IO.iodata_to_binary()
      assert {:event, %{"type" => "idle", "ms" => 250}} = Protocol.decode_frame(<<0x02>> <> body)
    end

    test "SNAPSHOT frame is unpacked from msgpack" do
      snap = %{"cols" => 80, "rows" => 24, "cells" => []}
      body = Msgpax.pack!(snap, iodata: true) |> IO.iodata_to_binary()
      assert {:snapshot, ^snap} = Protocol.decode_frame(<<0x03>> <> body)
    end

    test "unknown tag is preserved for logging" do
      assert {:unknown, 0x42, "junk"} = Protocol.decode_frame(<<0x42, "junk">>)
    end
  end

  describe "encode_control/1" do
    test "prefixes the CONTROL tag and msgpacks the body" do
      iodata = Protocol.encode_control(%{cmd: "hello"})
      bin = IO.iodata_to_binary(iodata)

      assert <<0x10, body::binary>> = bin
      assert %{"cmd" => "hello"} = Msgpax.unpack!(body)
    end

    test "stringifies atom keys so the sidecar can match on string keys" do
      iodata = Protocol.encode_control(%{cmd: "open", argv: ["echo", "hi"]})
      bin = IO.iodata_to_binary(iodata)
      <<0x10, body::binary>> = bin
      decoded = Msgpax.unpack!(body)
      assert decoded["cmd"] == "open"
      assert decoded["argv"] == ["echo", "hi"]
    end

    test "write_stdin packs bytes as msgpack bin so binary keys are preserved" do
      iodata = Protocol.encode_control(Protocol.write_stdin(<<0xFF, 0x00, "abc">>))
      bin = IO.iodata_to_binary(iodata)
      <<0x10, body::binary>> = bin
      # Msgpax distinguishes msgpack `str` (Unicode text) from `bin` (raw
      # bytes) on the wire; using `bin` for stdin avoids accidental UTF-8
      # validation. Msgpax 2.x returns msgpack `bin` as a raw Elixir binary
      # by default, which is what we assert here.
      decoded = Msgpax.unpack!(body)
      assert decoded["cmd"] == "write_stdin"
      assert decoded["bytes"] == <<0xFF, 0x00, "abc">>
    end

    test "open_launch carries a versioned launch spec" do
      iodata =
        Protocol.encode_control(
          Protocol.open_launch(
            %{
              provider: "fake",
              task: "hello",
              transport_order: ["stdio-json", "pty"],
              policy: %{tier2_fast_path_rules: ["Run tests?"], redaction_patterns: ["SECRET"]},
              authority: %{
                socket_path: "/tmp/mitos-authority.sock",
                project_id: "proj",
                run_id: "run",
                token: "tok"
              }
            },
            cols: 100,
            rows: 30,
            env: %{"TERM" => "xterm-256color"}
          )
        )

      bin = IO.iodata_to_binary(iodata)
      <<0x10, body::binary>> = bin
      decoded = Msgpax.unpack!(body)

      assert decoded["cmd"] == "OPEN"
      assert decoded["launch_spec"]["version"] == 1
      assert decoded["launch_spec"]["provider"] == "fake"
      assert decoded["launch_spec"]["task"] == "hello"
      assert decoded["launch_spec"]["transport_order"] == ["stdio-json", "pty"]
      assert decoded["launch_spec"]["cols"] == 100
      assert decoded["launch_spec"]["env"]["TERM"] == "xterm-256color"
      assert decoded["launch_spec"]["policy"]["tier2_fast_path_rules"] == ["Run tests?"]
      assert decoded["launch_spec"]["policy"]["redaction_patterns"] == ["SECRET"]
      assert decoded["launch_spec"]["authority"]["socket_path"] == "/tmp/mitos-authority.sock"
      assert decoded["launch_spec"]["authority"]["token"] == "tok"
    end

    test "authorize and deny use control protocol vocabulary" do
      encoded_authorize = Protocol.encode_control(Protocol.authorize("action-1", "tok"))
      <<0x10, authorize_body::binary>> = IO.iodata_to_binary(encoded_authorize)
      assert %{"cmd" => "AUTHORIZE", "action_id" => "action-1", "token" => "tok"} =
               Msgpax.unpack!(authorize_body)

      encoded_deny = Protocol.encode_control(Protocol.deny("action-1", "no"))
      <<0x10, deny_body::binary>> = IO.iodata_to_binary(encoded_deny)
      assert %{"cmd" => "DENY", "action_id" => "action-1", "reason" => "no"} =
               Msgpax.unpack!(deny_body)
    end
  end
end
