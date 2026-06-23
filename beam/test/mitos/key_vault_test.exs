defmodule Mitos.KeyVaultTest do
  use ExUnit.Case, async: false

  import Plug.Conn
  import Plug.Test

  setup do
    previous_vault = Application.get_env(:mitos, :key_vault)
    previous_leases = Application.get_env(:mitos, :key_vault_leases)

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
    end)

    :ok
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
end
