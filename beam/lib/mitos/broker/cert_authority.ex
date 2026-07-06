defmodule Mitos.Broker.CertAuthority do
  @moduledoc """
  The MITM certificate authority for the model-egress TLS proxy (05 R1). A
  self-signed CA (generated once via `openssl`, persisted under `data_dir/mitos-mitm-ca/`)
  signs per-host leaf certs on demand so the proxy can terminate the harness's
  TLS, inject the model credential, and re-encrypt to the real upstream.

  We shell to `openssl` rather than hand-assemble X.509: Erlang's `:public_key`
  has no high-level cert generator, and openssl is universally present on the
  host. The CA cert is also exposed so it can be injected into the sandbox trust
  store (`SSL_CERT_FILE`, `NODE_EXTRA_CA_CERTS`, …) — the harness then trusts
  the proxy's leaf for `https://<real-host>` calls.

  The CA is host-side only; the model key it helps inject lives in the `Vault`.
  """
  use GenServer

  defstruct [:dir, :ca_cert, :ca_key, :leaves]

  ## client API

  def start_link(_opts \\ []), do: GenServer.start_link(__MODULE__, %{}, name: __MODULE__)

  @doc "Absolute path to the PEM CA cert (to inject into a sandbox trust store)."
  def ca_cert_path, do: GenServer.call(__MODULE__, :ca_cert_path)

  @doc """
  A per-host leaf cert+key (PEM paths) signed by the CA, cached per host.
  `host` is the SNI/Host the harness connects to (e.g. `open.bigmodel.cn`).
  """
  def leaf(host), do: GenServer.call(__MODULE__, {:leaf, host})

  ## server

  @impl true
  def init(_) do
    dir = Path.join(Mitos.data_dir(), "mitos-mitm-ca")
    File.mkdir_p!(dir)
    {ca_cert, ca_key} = ensure_ca(dir)
    {:ok, %__MODULE__{dir: dir, ca_cert: ca_cert, ca_key: ca_key, leaves: %{}}}
  end

  defp ensure_ca(dir) do
    File.mkdir_p!(dir)
    cert = Path.join(dir, "ca.crt")
    key = Path.join(dir, "ca.key")

    if File.exists?(cert) and File.exists?(key) do
      {cert, key}
    else
      openssl(
        ~w(req -x509 -newkey rsa:2048 -nodes -keyout #{key} -out #{cert}
           -subj /CN=mitos-mitm-ca -days 3650 -addext basicConstraints=critical,CA:TRUE
           -addext keyUsage=critical,keyCertSign,cRLSign,digitalSignature)
      )

      {cert, key}
    end
  end

  defp leaf_for(%__MODULE__{dir: dir} = st, host) do
    # Re-ensure the CA exists on disk (defensive against a wiped data_dir after init).
    {ca_cert, ca_key} = ensure_ca(dir)
    leaves = st.leaves

    case Map.get(leaves, host) do
      %{cert: c, key: k} when is_binary(c) and is_binary(k) and c != "" ->
        {c, k}

      _ ->
        # one dir per host (safe host char subset)
        sub = String.replace(host, ~r/[^a-zA-Z0-9.-]/, "_")
        hdir = Path.join(dir, sub)
        File.mkdir_p!(hdir)

        hkey = Path.join(hdir, "leaf.key")
        csr = Path.join(hdir, "leaf.csr")
        hcrt = Path.join(hdir, "leaf.crt")
        ext = Path.join(hdir, "leaf.ext")
        File.write!(ext, "subjectAltName=DNS:#{host}\nextendedKeyUsage=serverAuth\n")

        openssl(
          ~w(req -new -newkey rsa:2048 -nodes -keyout #{hkey} -out #{csr} -subj /CN=#{host})
        )

        openssl(
          ~w(x509 -req -in #{csr} -CA #{ca_cert} -CAkey #{ca_key} -CAcreateserial
             -out #{hcrt} -days 825 -extfile #{ext})
        )

        {hcrt, hkey}
    end
  end

  defp openssl(args) do
    case System.cmd("openssl", args, stderr_to_stdout: true) do
      {_, 0} -> :ok
      {out, code} -> raise "openssl failed (#{code}): #{out}"
    end
  end

  @impl true
  def handle_call(:ca_cert_path, _from, st), do: {:reply, st.ca_cert, st}

  def handle_call({:leaf, host}, _from, st) do
    {cert, key} = leaf_for(st, host)
    st = put_in(st.leaves[host], %{cert: cert, key: key})
    {:reply, {cert, key}, st}
  end
end
