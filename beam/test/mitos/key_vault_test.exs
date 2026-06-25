defmodule Mitos.KeyVaultTest do
  use ExUnit.Case, async: false

  import Plug.Conn
  import Plug.Test

  setup do
    previous_vault = Application.get_env(:mitos, :key_vault)
    previous_leases = Application.get_env(:mitos, :key_vault_leases)
    previous_trace_base_dir = Application.get_env(:mitos, :trace_base_dir)

    trace_base_dir =
      Path.join(System.tmp_dir!(), "mitos-key-vault-trace-#{System.unique_integer([:positive])}")

    File.mkdir_p!(trace_base_dir)
    Application.put_env(:mitos, :trace_base_dir, trace_base_dir)

    on_exit(fn ->
      if previous_vault do
        Application.put_env(:mitos, :key_vault, previous_vault)
      else
        Application.delete_env(:mitos, :key_vault)
      end

      if previous_leases do
        Application.put_env(:mitos, :key_vault_leases, previous_leases)
      else
        Application.delete_env(:mitos, :key_vault_leases)
      end

      if previous_trace_base_dir do
        Application.put_env(:mitos, :trace_base_dir, previous_trace_base_dir)
      else
        Application.delete_env(:mitos, :trace_base_dir)
      end

      File.rm_rf!(trace_base_dir)
    end)

    {:ok, trace_base_dir: trace_base_dir}
  end

  test "resolves seller-scoped credentials without exposing secret material" do
    Application.put_env(:mitos, :key_vault, %{
      "seller-a" => %{
        "credentials" => %{
          "provider-key" => %{
            "type" => "bearer",
            "destinations" => ["api.provider.test"],
            "secret" => "seller-a-secret"
          }
        }
      },
      "wrong-seller-key" => %{
        "seller_id" => "seller-b",
        "type" => "bearer",
        "destinations" => ["api.provider.test"],
        "secret" => "seller-b-secret"
      }
    })

    assert {:ok,
            %{
              "credential_id" => "provider-key",
              "credential_type" => "bearer",
              "seller_id" => "seller-a",
              "dest" => "api.provider.test"
            } = ref} =
             Mitos.KeyVault.resolve("provider-key", "api.provider.test", seller_id: "seller-a")

    refute inspect(ref) =~ "seller-a-secret"

    assert {:error, "credential seller mismatch"} =
             Mitos.KeyVault.resolve("wrong-seller-key", "api.provider.test",
               seller_id: "seller-a"
             )
  end

  test "keeps unscoped dev credentials supported while enforcing destination scope" do
    Application.put_env(:mitos, :key_vault, %{
      "legacy-key" => %{
        "type" => "bearer",
        "destinations" => ["api.allowed.test"],
        "secret" => "legacy-secret"
      }
    })

    assert {:ok, %{"credential_id" => "legacy-key", "dest" => "api.allowed.test"} = ref} =
             Mitos.KeyVault.resolve("legacy-key", "api.allowed.test")

    refute Map.has_key?(ref, "seller_id")
    refute inspect(ref) =~ "legacy-secret"

    assert {:error, "credential destination mismatch"} =
             Mitos.KeyVault.resolve("legacy-key", "api.denied.test")
  end

  test "registers seller credentials in the broker vault and lists only redacted refs" do
    assert {:ok,
            %{
              "credential_id" => "provider-key",
              "credential_type" => "bearer",
              "seller_id" => "seller-a",
              "destinations" => ["api.provider.test"]
            } = ref} =
             Mitos.KeyVault.register(%{
               seller_id: "seller-a",
               credential_id: "provider-key",
               type: "bearer",
               destinations: ["api.provider.test"],
               secret: "seller-a-secret"
             })

    refute inspect(ref) =~ "seller-a-secret"

    assert Mitos.KeyVault.list("seller-a") == [ref]
    refute inspect(Mitos.KeyVault.list("seller-a")) =~ "seller-a-secret"

    assert {:ok,
            %{
              "credential_id" => "provider-key",
              "credential_type" => "bearer",
              "seller_id" => "seller-a",
              "dest" => "api.provider.test"
            }} =
             Mitos.KeyVault.resolve("provider-key", "api.provider.test", seller_id: "seller-a")

    assert {:error, "credential unavailable"} =
             Mitos.KeyVault.resolve("provider-key", "api.provider.test", seller_id: "seller-b")
  end

  test "rejects seller key registration without explicit destination scope" do
    assert {:error, "registered seller keys must be scoped to explicit destinations"} =
             Mitos.KeyVault.register(%{
               seller_id: "seller-a",
               credential_id: "too-broad",
               destinations: ["*"],
               secret: "seller-a-secret"
             })
  end

  test "rejects secret-bearing credential ids and destinations without echoing secret material" do
    assert {:error, id_error} =
             Mitos.KeyVault.register(%{
               seller_id: "seller-a",
               credential_id: "sk-live-must-not-be-here",
               destinations: ["api.provider.test"],
               secret: "seller-a-secret"
             })

    assert id_error == "credential_id must reference an id, not secret material"
    refute id_error =~ "sk-live-must-not-be-here"

    assert {:error, url_error} =
             Mitos.KeyVault.register(%{
               seller_id: "seller-a",
               credential_id: "provider-key",
               destinations: ["https://user:pass@api.provider.test/callback"],
               secret: "seller-a-secret"
             })

    assert url_error == "registered seller key destination must not contain secret material"
    refute url_error =~ "user:pass"

    assert {:error, query_error} =
             Mitos.KeyVault.register(%{
               seller_id: "seller-a",
               credential_id: "provider-key",
               destinations: ["https://api.provider.test/callback?signature=must-not-be-here"],
               secret: "seller-a-secret"
             })

    assert query_error == "registered seller key destination must not contain secret material"
    refute query_error =~ "must-not-be-here"

    assert Mitos.KeyVault.list("seller-a") == []
  end

  test "leases registered credentials per run and revokes them on completion" do
    assert {:ok, _ref} =
             Mitos.KeyVault.register(%{
               seller_id: "seller-a",
               credential_id: "provider-key",
               type: "bearer",
               destinations: ["api.provider.test"],
               secret: "seller-a-secret"
             })

    launch_spec = %{
      seller_id: "seller-a",
      egress_policy: [
        %{
          dest: "api.provider.test",
          credential_id: "provider-key",
          requires_approval: false
        }
      ]
    }

    assert {:error, "credential lease unavailable"} =
             Mitos.KeyVault.resolve("provider-key", "api.provider.test",
               seller_id: "seller-a",
               run_id: "run-lease"
             )

    assert {:ok,
            [
              %{
                "credential_id" => "provider-key",
                "credential_type" => "bearer",
                "dest" => "api.provider.test",
                "seller_id" => "seller-a"
              }
            ] = leases} = Mitos.KeyVault.lease_run("run-lease", launch_spec)

    refute inspect(leases) =~ "seller-a-secret"
    assert Mitos.KeyVault.leases("run-lease") == leases

    assert {:ok,
            %{
              "credential_id" => "provider-key",
              "dest" => "api.provider.test",
              "seller_id" => "seller-a"
            }} =
             Mitos.KeyVault.resolve("provider-key", "api.provider.test",
               seller_id: "seller-a",
               run_id: "run-lease"
             )

    assert :ok = Mitos.KeyVault.revoke_run("run-lease")
    assert Mitos.KeyVault.leases("run-lease") == []

    assert {:error, "credential lease unavailable"} =
             Mitos.KeyVault.resolve("provider-key", "api.provider.test",
               seller_id: "seller-a",
               run_id: "run-lease"
             )
  end

  test "strict run leases fail before storing partial unavailable credential bindings" do
    assert {:ok, _ref} =
             Mitos.KeyVault.register(%{
               seller_id: "seller-a",
               credential_id: "provider-key",
               type: "bearer",
               destinations: ["api.provider.test"],
               secret: "seller-a-secret"
             })

    launch_spec = %{
      seller_id: "seller-a",
      egress_policy: [
        %{id: "ok", dest: "api.provider.test", credential_id: "provider-key"},
        %{id: "missing", dest: "api.missing.test", credential_id: "missing-key"}
      ]
    }

    assert {:error,
            {:credential_lease_failed,
             [
               %{
                 "rule" => "missing",
                 "dest" => "api.missing.test",
                 "credential_id" => "missing-key",
                 "reason" => "credential unavailable"
               }
             ]}} = Mitos.KeyVault.lease_run_strict("run-strict", launch_spec)

    assert Mitos.KeyVault.leases("run-strict") == []

    assert {:ok, [_lease]} =
             Mitos.KeyVault.lease_run_strict("run-strict-ok", %{
               seller_id: "seller-a",
               egress_policy: [
                 %{id: "ok", dest: "api.provider.test", credential_id: "provider-key"}
               ]
             })
  end

  test "broker egress route resolves only active run-scoped credential leases", %{
    trace_base_dir: trace_base_dir
  } do
    assert {:ok, _ref} =
             Mitos.KeyVault.register(%{
               seller_id: "seller-a",
               credential_id: "provider-key",
               type: "bearer",
               destinations: ["api.provider.test"],
               secret: "seller-a-secret"
             })

    launch_spec = %{
      seller_id: "seller-a",
      egress_policy: [
        %{dest: "api.provider.test", credential_id: "provider-key", requires_approval: false}
      ]
    }

    assert {:ok, [_lease]} = Mitos.KeyVault.lease_run("run-broker", launch_spec)

    assert {:ok,
            %{
              "credential_id" => "provider-key",
              "credential_type" => "bearer",
              "seller_id" => "seller-a",
              "dest" => "api.provider.test"
            } = ref} =
             Mitos.KeyVault.resolve_run("run-broker", "provider-key", "api.provider.test")

    refute inspect(ref) =~ "seller-a-secret"

    allowed =
      post_json("/broker/runs/run-broker/egress", %{
        credential_id: "provider-key",
        dest: "api.provider.test",
        request_id: "egress-allow-1"
      })

    assert allowed.status == 200
    refute allowed.resp_body =~ "seller-a-secret"

    assert Jason.decode!(allowed.resp_body) == %{
             "decision" => "allow",
             "run_id" => "run-broker",
             "dest" => "api.provider.test",
             "credential" => ref
           }

    events = Mitos.Trace.read_events("run-broker", base_dir: trace_base_dir)

    assert Enum.any?(events, fn
             %{
               "kind" => "effect",
               "type" => "egress_attempted",
               "ref" => %{
                 "dest" => "api.provider.test",
                 "credential_id" => "provider-key",
                 "request_id" => "egress-allow-1"
               }
             } ->
               true

             _event ->
               false
           end)

    assert Enum.any?(events, fn
             %{
               "kind" => "effect",
               "type" => "egress_allowed",
               "payload" => %{
                 "decision" => "allow",
                 "credential" => %{"credential_id" => "provider-key"}
               }
             } ->
               true

             _event ->
               false
           end)

    assert Enum.any?(events, fn
             %{
               "kind" => "effect",
               "type" => "egress_completed",
               "ref" => %{"request_id" => "egress-allow-1"}
             } ->
               true

             _event ->
               false
           end)

    refute Mitos.Trace.fold_run("run-broker", base_dir: trace_base_dir)[
             "egress_replay_requires_approval?"
           ]

    denied =
      post_json("/broker/runs/run-broker/egress", %{
        credential_id: "provider-key",
        dest: "api.other.test",
        request_id: "egress-deny-1"
      })

    assert denied.status == 403
    denied_body = Jason.decode!(denied.resp_body)
    assert denied_body["decision"] == "deny"
    assert denied_body["reason"] == "credential lease unavailable"
    refute denied.resp_body =~ "seller-a-secret"

    events = Mitos.Trace.read_events("run-broker", base_dir: trace_base_dir)

    assert Enum.any?(events, fn
             %{
               "kind" => "effect",
               "type" => "egress_denied",
               "ref" => %{"dest" => "api.other.test", "request_id" => "egress-deny-1"},
               "payload" => %{"reason" => "credential lease unavailable"}
             } ->
               true

             _event ->
               false
           end)

    refute Mitos.Trace.fold_run("run-broker", base_dir: trace_base_dir)[
             "egress_replay_requires_approval?"
           ]

    missing = post_json("/broker/runs/run-broker/egress", %{dest: "api.provider.test"})
    assert missing.status == 422

    assert :ok = Mitos.KeyVault.revoke_run("run-broker")

    revoked =
      post_json("/broker/runs/run-broker/egress", %{
        credential_id: "provider-key",
        dest: "api.provider.test"
      })

    assert revoked.status == 403
    assert Jason.decode!(revoked.resp_body)["reason"] == "credential lease unavailable"
  end

  test "broker egress route parks approval-required leases before forwarding", %{
    trace_base_dir: trace_base_dir
  } do
    parent = self()

    upstream =
      start_broker_upstream_server(fn method, path, headers, body ->
        send(parent, {:broker_upstream_request, method, path, headers, body})
        {200, [{"content-type", "application/json"}], Jason.encode!(%{ok: true})}
      end)

    dest = broker_upstream_dest(upstream)

    assert {:ok, _ref} =
             Mitos.KeyVault.register(%{
               seller_id: "seller-a",
               credential_id: "provider-key",
               type: "bearer",
               destinations: [dest],
               secret: "seller-a-secret"
             })

    assert {:ok,
            [
              %{
                "credential_id" => "provider-key",
                "credential_type" => "bearer",
                "dest" => ^dest,
                "seller_id" => "seller-a",
                "requires_approval" => true
              } = lease
            ]} =
             Mitos.KeyVault.lease_run("run-broker-approval", %{
               seller_id: "seller-a",
               egress_policy: [
                 %{dest: dest, credential_id: "provider-key", requires_approval: true}
               ]
             })

    refute inspect(lease) =~ "seller-a-secret"

    assert {:ok,
            %{
              "credential_id" => "provider-key",
              "credential_type" => "bearer",
              "seller_id" => "seller-a",
              "dest" => ^dest,
              "requires_approval" => true
            } = credential_ref} =
             Mitos.KeyVault.resolve_run("run-broker-approval", "provider-key", dest)

    refute inspect(credential_ref) =~ "seller-a-secret"

    assert {:error, "approval required"} =
             Mitos.KeyVault.resolve_run_attachment("run-broker-approval", "provider-key", dest)

    pending =
      post_json("/broker/runs/run-broker-approval/egress", %{
        credential_id: "provider-key",
        dest: dest,
        url: upstream <> "/v1/approval",
        method: "POST",
        headers: %{"authorization" => "Bearer sandbox-supplied"},
        body: %{"hello" => "approval"},
        request_id: "egress-approval-1"
      })

    assert pending.status == 202
    refute pending.resp_body =~ "seller-a-secret"

    assert Jason.decode!(pending.resp_body) == %{
             "decision" => "pending_approval",
             "run_id" => "run-broker-approval",
             "dest" => dest,
             "request_id" => "egress-approval-1",
             "approval_id" => "egress-approval-1",
             "reason" => "egress approval required"
           }

    refute_receive {:broker_upstream_request, _method, _path, _headers, _body}, 100

    events = Mitos.Trace.read_events("run-broker-approval", base_dir: trace_base_dir)
    assert Enum.map(events, & &1["type"]) == ["egress_attempted", "approval_requested"]

    assert Enum.any?(events, fn
             %{
               "kind" => "effect",
               "type" => "approval_requested",
               "ref" => %{
                 "dest" => ^dest,
                 "credential_id" => "provider-key",
                 "request_id" => "egress-approval-1",
                 "approval_id" => "egress-approval-1"
               },
               "payload" => %{
                 "kind" => "egress",
                 "decision" => "pending",
                 "credential" => %{
                   "credential_id" => "provider-key",
                   "requires_approval" => true
                 }
               }
             } ->
               true

             _event ->
               false
           end)

    refute Enum.any?(
             events,
             &(&1["type"] in ["egress_allowed", "egress_completed", "egress_denied"])
           )

    folded = Mitos.Trace.fold_run("run-broker-approval", base_dir: trace_base_dir)
    assert folded["egress_replay_requires_approval?"]

    assert [
             %{
               "type" => "egress_attempted",
               "ref" => %{"request_id" => "egress-approval-1", "dest" => ^dest}
             }
           ] = folded["incomplete_egress_attempts"]
  end

  test "broker egress route forwards requests with credentials attached only at the edge", %{
    trace_base_dir: trace_base_dir
  } do
    parent = self()

    upstream =
      start_broker_upstream_server(fn method, path, headers, body ->
        send(parent, {:broker_upstream_request, method, path, headers, body})

        {202, [{"content-type", "application/json"}],
         Jason.encode!(%{
           ok: true,
           authorization: headers["authorization"],
           api_key: headers["x-api-key"],
           cookie: headers["cookie"],
           client: headers["x-client"],
           body: body
         })}
      end)

    dest = broker_upstream_dest(upstream)

    assert {:ok, _ref} =
             Mitos.KeyVault.register(%{
               seller_id: "seller-a",
               credential_id: "provider-key",
               type: "bearer",
               destinations: [dest],
               secret: "seller-a-secret"
             })

    assert {:ok, [_lease]} =
             Mitos.KeyVault.lease_run("run-broker-forward", %{
               seller_id: "seller-a",
               egress_policy: [
                 %{dest: dest, credential_id: "provider-key", requires_approval: false}
               ]
             })

    forwarded =
      post_json("/broker/runs/run-broker-forward/egress", %{
        credential_id: "provider-key",
        dest: dest,
        url: upstream <> "/v1/echo?mode=auth",
        method: "POST",
        headers: %{
          "authorization" => "Bearer sandbox-supplied",
          "cookie" => "session=sandbox-secret",
          "x-api-key" => "sandbox-api-key",
          "x-client" => "backend-test"
        },
        body: %{"hello" => "world"},
        request_id: "egress-forward-1"
      })

    assert forwarded.status == 202
    refute forwarded.resp_body =~ "sandbox-secret"
    refute forwarded.resp_body =~ "sandbox-api-key"
    refute forwarded.resp_body =~ "seller-a-secret"
    refute forwarded.resp_body =~ "Bearer seller-a-secret"

    assert Jason.decode!(forwarded.resp_body) == %{
             "ok" => true,
             "authorization" => "Bearer [redacted]",
             "api_key" => nil,
             "cookie" => nil,
             "client" => "backend-test",
             "body" => Jason.encode!(%{"hello" => "world"})
           }

    assert_receive {:broker_upstream_request, "POST", "/v1/echo?mode=auth", headers, request_body}

    assert headers["authorization"] == "Bearer seller-a-secret"
    refute headers["authorization"] == "Bearer sandbox-supplied"
    refute Map.has_key?(headers, "cookie")
    refute Map.has_key?(headers, "x-api-key")
    assert headers["x-client"] == "backend-test"
    assert headers["content-type"] == "application/json"
    assert Jason.decode!(request_body) == %{"hello" => "world"}

    events = Mitos.Trace.read_events("run-broker-forward", base_dir: trace_base_dir)

    assert Enum.any?(events, fn
             %{
               "kind" => "effect",
               "type" => "egress_allowed",
               "ref" => %{"request_id" => "egress-forward-1"},
               "payload" => %{
                 "status" => 202,
                 "credential" => %{"credential_id" => "provider-key"},
                 "request_bytes" => request_bytes,
                 "response_bytes" => response_bytes
               }
             }
             when request_bytes > 0 and response_bytes > 0 ->
               true

             _event ->
               false
           end)

    assert Enum.any?(events, fn
             %{
               "kind" => "effect",
               "type" => "egress_completed",
               "ref" => %{"request_id" => "egress-forward-1"},
               "payload" => %{"status" => 202, "response_bytes" => response_bytes}
             }
             when response_bytes > 0 ->
               true

             _event ->
               false
           end)

    mismatched =
      post_json("/broker/runs/run-broker-forward/egress", %{
        credential_id: "provider-key",
        dest: dest,
        url: "http://example.invalid/v1/echo",
        request_id: "egress-forward-mismatch"
      })

    assert mismatched.status == 403
    assert Jason.decode!(mismatched.resp_body)["reason"] == "broker forward destination mismatch"
  end

  test "broker egress route rejects and redacts credential-bearing forward URLs", %{
    trace_base_dir: trace_base_dir
  } do
    parent = self()

    upstream =
      start_broker_upstream_server(fn method, path, headers, body ->
        send(parent, {:broker_upstream_request, method, path, headers, body})
        {200, [{"content-type", "application/json"}], %{"ok" => true}}
      end)

    uri = URI.parse(upstream)
    dest = broker_upstream_dest(upstream)

    assert {:ok, _ref} =
             Mitos.KeyVault.register(%{
               seller_id: "seller-a",
               credential_id: "provider-key",
               type: "bearer",
               destinations: [dest],
               secret: "seller-a-secret"
             })

    assert {:ok, [_lease]} =
             Mitos.KeyVault.lease_run("run-broker-redacted-url", %{
               seller_id: "seller-a",
               egress_policy: [
                 %{dest: dest, credential_id: "provider-key", requires_approval: false}
               ]
             })

    credentialed_url =
      "#{uri.scheme}://user:sandbox-pass@#{uri.host}:#{uri.port}/v1/echo?token=sandbox-token&visible=ok"

    denied =
      post_json("/broker/runs/run-broker-redacted-url/egress", %{
        credential_id: "provider-key",
        dest: dest,
        url: credentialed_url,
        request_id: "egress-redacted-url-1"
      })

    assert denied.status == 403

    assert Jason.decode!(denied.resp_body)["reason"] ==
             "broker forward URL must not contain credentials"

    query_denied =
      post_json("/broker/runs/run-broker-redacted-url/egress", %{
        credential_id: "provider-key",
        dest: dest,
        url: upstream <> "/v1/echo?signature=sandbox-signature&visible=ok",
        request_id: "egress-redacted-url-2"
      })

    assert query_denied.status == 403

    assert Jason.decode!(query_denied.resp_body)["reason"] ==
             "broker forward URL must not contain credential query parameters"

    refute_receive {:broker_upstream_request, _method, _path, _headers, _body}, 100

    events = Mitos.Trace.read_events("run-broker-redacted-url", base_dir: trace_base_dir)
    refute inspect(events) =~ "sandbox-pass"
    refute inspect(events) =~ "sandbox-token"
    refute inspect(events) =~ "sandbox-signature"

    assert Enum.any?(events, fn
             %{
               "kind" => "effect",
               "type" => "egress_attempted",
               "payload" => %{"url" => traced_url}
             } ->
               traced_url ==
                 "#{uri.scheme}://#{uri.host}:#{uri.port}/v1/echo?token=[redacted]&visible=ok"

             _event ->
               false
           end)

    assert Enum.any?(events, fn
             %{
               "kind" => "effect",
               "type" => "egress_attempted",
               "payload" => %{"url" => traced_url}
             } ->
               traced_url == upstream <> "/v1/echo?signature=[redacted]&visible=ok"

             _event ->
               false
           end)
  end

  test "HTTP key registration stores secrets server-side and returns redacted refs" do
    conn =
      post_json("/sellers/seller-a/keys", %{
        credential_id: "openai-key",
        type: "bearer",
        destinations: ["api.openai.test"],
        secret: "sk-test-secret"
      })

    assert conn.status == 201
    refute conn.resp_body =~ "sk-test-secret"

    body = Jason.decode!(conn.resp_body)

    assert body["credential"] == %{
             "credential_id" => "openai-key",
             "credential_type" => "bearer",
             "seller_id" => "seller-a",
             "destinations" => ["api.openai.test"]
           }

    assert {:ok,
            %{
              "credential_id" => "openai-key",
              "seller_id" => "seller-a",
              "dest" => "api.openai.test"
            }} =
             Mitos.KeyVault.resolve("openai-key", "api.openai.test", seller_id: "seller-a")

    list_conn = conn(:get, "/sellers/seller-a/keys") |> Mitos.Web.Router.call([])
    assert list_conn.status == 200
    refute list_conn.resp_body =~ "sk-test-secret"

    assert Jason.decode!(list_conn.resp_body)["credentials"] == [body["credential"]]
  end

  defp post_json(path, body) do
    conn(:post, path, Jason.encode!(body))
    |> put_req_header("content-type", "application/json")
    |> Mitos.Web.Router.call([])
  end

  defp start_broker_upstream_server(handler) when is_function(handler, 4) do
    {:ok, listen_socket} =
      :gen_tcp.listen(0, [
        :binary,
        packet: :raw,
        active: false,
        ip: {127, 0, 0, 1},
        reuseaddr: true
      ])

    {:ok, {{127, 0, 0, 1}, port}} = :inet.sockname(listen_socket)
    server = spawn_link(fn -> broker_upstream_accept_loop(listen_socket, handler) end)

    on_exit(fn ->
      :gen_tcp.close(listen_socket)
      Process.exit(server, :shutdown)
    end)

    "http://127.0.0.1:#{port}"
  end

  defp broker_upstream_accept_loop(listen_socket, handler) do
    case :gen_tcp.accept(listen_socket) do
      {:ok, socket} ->
        broker_upstream_handle_socket(socket, handler)
        broker_upstream_accept_loop(listen_socket, handler)

      {:error, :closed} ->
        :ok

      {:error, _reason} ->
        broker_upstream_accept_loop(listen_socket, handler)
    end
  end

  defp broker_upstream_handle_socket(socket, handler) do
    case read_http_request(socket) do
      {:ok, method, path, headers, body} ->
        {status, response_headers, response_body} =
          broker_upstream_response(handler.(method, path, headers, body))

        send_http_response(socket, status, response_headers, response_body)

      {:error, reason} ->
        send_http_response(
          socket,
          500,
          [{"content-type", "application/json"}],
          Jason.encode!(%{"error" => inspect(reason)})
        )
    end

    :gen_tcp.close(socket)
  end

  defp read_http_request(socket, acc \\ "") do
    case :gen_tcp.recv(socket, 0, 1_000) do
      {:ok, chunk} ->
        acc = acc <> chunk

        case parse_http_request(acc) do
          {:ok, method, path, headers, body} -> {:ok, method, path, headers, body}
          :more -> read_http_request(socket, acc)
        end

      {:error, reason} ->
        {:error, reason}
    end
  end

  defp parse_http_request(bytes) do
    case :binary.match(bytes, "\r\n\r\n") do
      {header_size, 4} ->
        header = binary_part(bytes, 0, header_size)
        body_offset = header_size + 4
        body = binary_part(bytes, body_offset, byte_size(bytes) - body_offset)
        [request_line | header_lines] = String.split(header, "\r\n")
        [method, path | _] = String.split(request_line, " ")
        headers = request_headers(header_lines)
        content_length = request_content_length(headers)

        if byte_size(body) >= content_length do
          {:ok, method, path, headers, binary_part(body, 0, content_length)}
        else
          :more
        end

      :nomatch ->
        :more
    end
  end

  defp request_headers(header_lines) do
    Map.new(header_lines, fn line ->
      [name, value] = String.split(line, ":", parts: 2)
      {String.downcase(name), String.trim(value)}
    end)
  end

  defp request_content_length(headers) do
    headers
    |> Map.get("content-length", "0")
    |> String.to_integer()
  end

  defp broker_upstream_response({status, headers, body}) when is_list(headers),
    do: {status, headers, to_response_body(body)}

  defp broker_upstream_response({status, body}), do: {status, [], to_response_body(body)}

  defp to_response_body(body) when is_binary(body), do: body
  defp to_response_body(body), do: Jason.encode!(body)

  defp send_http_response(socket, status, headers, body) do
    headers =
      headers
      |> Enum.reject(fn {name, _value} -> String.downcase(name) == "content-length" end)
      |> Kernel.++([{"content-length", Integer.to_string(byte_size(body))}])

    response = [
      "HTTP/1.1 ",
      Integer.to_string(status),
      " OK\r\n",
      Enum.map(headers, fn {name, value} -> [name, ": ", value, "\r\n"] end),
      "connection: close\r\n",
      "\r\n",
      body
    ]

    :ok = :gen_tcp.send(socket, response)
  end

  defp broker_upstream_dest(endpoint) do
    uri = URI.parse(endpoint)
    uri.host <> ":" <> Integer.to_string(uri.port)
  end
end
