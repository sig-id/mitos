defmodule Mitos.Workspace do
  @moduledoc """
  What enters the sandbox as the run's subject, and whether it is copied at all.

  `05 §5.1` says the input repo is **cloned INTO** the sandbox, never bound.
  That rule exists for the cross-tenant case — a T2 run on a stranger's data
  must not be able to reach the original. It was implemented as an unconditional
  `File.cp_r!` of the whole source tree, which on a real repository is not an
  isolation boundary but a disk-exhaustion bug: a 302 GB checkout (VM images,
  `target/`, fuzz corpora) is copied in full, per stage, before a single token
  is spent.

  Two corrections, and they are different in kind:

    * **Honour `.gitignore`.** Build output, caches, and VM images are not the
      subject of the work and were never meant to cross the boundary. A git
      source is enumerated with `git ls-files --cached --others
      --exclude-standard` — tracked files plus untracked-but-not-ignored ones —
      so what lands in the sandbox is what a reviewer would call "the repo".
    * **Allow the sandbox to be turned off** (`mode: :in_place`). A Studio
      operator working on their own repo at T0/T1 already trusts the workspace;
      copying it buys nothing and can cost hundreds of gigabytes. This is a
      **deliberate, per-run, recorded** choice, not a default, and `05 §4`'s
      rule still holds absolutely at T2: distrust scales isolation, so T2 MUST
      refuse it (`compile/2`).

  A size ceiling backstops both: a clone that would exceed it is refused with a
  message naming the offenders, rather than started and discovered at 94% disk.
  """

  @default_max_bytes 2_000_000_000

  @type mode :: :clone | :in_place

  @doc """
  Resolve the workspace mode. T2 is never in-place — the sandbox boundary *is*
  the security boundary there (`01 §1.3`), and an operator convenience may not
  quietly relax it.
  """
  def mode(requested, trust_tier) do
    requested = normalize_mode(requested || configured_mode())

    cond do
      requested == :in_place and trust_tier == "T2" -> {:error, :in_place_forbidden_at_t2}
      requested in [:clone, :in_place] -> {:ok, requested}
      true -> {:error, {:unknown_workspace_mode, requested}}
    end
  end

  @doc """
  Materialize the workspace for a stage.

    * `:in_place` — the source dir *is* the workspace. Nothing is copied and
      nothing is created; the run works on the operator's real tree.
    * `:clone` — copy the gitignore-honouring file set into `dest`.

  Returns `{:ok, workspace_path}`.
  """
  def materialize(source, dest, opts \\ [])

  def materialize(nil, dest, _opts) do
    File.mkdir_p!(dest)
    {:ok, dest}
  end

  def materialize(source, dest, opts) do
    case opts[:mode] || :clone do
      :in_place ->
        source = Path.expand(source)
        if File.dir?(source), do: {:ok, source}, else: {:error, {:workspace_source_not_found, source}}

      :clone ->
        clone(source, dest, opts)
    end
  end

  @doc """
  The files that constitute the workspace, relative to `source`.

  For a git checkout this is `--cached` (tracked) plus `--others
  --exclude-standard` (untracked but not ignored) — i.e. everything except what
  `.gitignore` excludes. For a plain directory it is every file, since there is
  no ignore file to honour.
  """
  def files(source) do
    source = Path.expand(source)

    if git_repo?(source) do
      case git(source, ["ls-files", "-z", "--cached", "--others", "--exclude-standard"]) do
        {out, 0} -> {:ok, out |> String.split(<<0>>, trim: true) |> Enum.uniq()}
        {out, code} -> {:error, {:git_ls_files_failed, code, String.trim(out)}}
      end
    else
      {:ok,
       source
       |> Path.join("**")
       |> Path.wildcard(match_dot: true)
       |> Enum.reject(&File.dir?/1)
       |> Enum.map(&Path.relative_to(&1, source))}
    end
  end

  @doc """
  Total bytes of the workspace file set, and the largest contributors. Cheap:
  `File.stat` per file, never a read.
  """
  def measure(source) do
    with {:ok, rels} <- files(source) do
      sized =
        Enum.map(rels, fn rel ->
          case File.stat(Path.join(source, rel)) do
            {:ok, %{size: n}} -> {rel, n}
            _ -> {rel, 0}
          end
        end)

      {:ok,
       %{
         bytes: Enum.reduce(sized, 0, fn {_r, n}, acc -> acc + n end),
         count: length(sized),
         largest: sized |> Enum.sort_by(fn {_r, n} -> -n end) |> Enum.take(5)
       }}
    end
  end

  @doc """
  A content-independent fingerprint of the workspace: path, size, and mtime per
  file. Used by `no_progress` / `oscillation` (`04 §4.3`).

  It deliberately does **not** read file contents. Hashing the bytes of a real
  checkout on every iteration is an I/O storm, and the signal only needs to know
  whether the tree moved — not what it now says.
  """
  def fingerprint(source) do
    with {:ok, rels} <- files(source) do
      digest =
        rels
        |> Enum.sort()
        |> Enum.map_join("\n", fn rel ->
          case File.stat(Path.join(source, rel), time: :posix) do
            {:ok, %{size: size, mtime: mtime}} -> "#{rel}:#{size}:#{mtime}"
            _ -> "#{rel}:absent"
          end
        end)
        |> Mitos.sha256()

      {:ok, digest}
    end
  end

  @doc "Copy the workspace file set into `dest`, honouring `.gitignore`."
  def clone(source, dest, opts \\ []) do
    source = Path.expand(source)

    with true <- File.dir?(source) or {:error, {:workspace_source_not_found, source}},
         {:ok, rels} <- files(source),
         :ok <- check_ceiling(source, rels, opts) do
      File.mkdir_p!(dest)

      Enum.each(rels, fn rel ->
        src = Path.join(source, rel)
        dst = Path.join(dest, rel)

        if File.regular?(src) or File.exists?(src) do
          File.mkdir_p!(Path.dirname(dst))
          File.cp!(src, dst)
        end
      end)

      {:ok, dest}
    else
      false -> {:error, {:workspace_source_not_found, source}}
      {:error, _} = err -> err
    end
  end

  @doc "The configured clone ceiling in bytes."
  def max_bytes, do: config(:max_bytes, @default_max_bytes)

  @doc "Human-readable byte size."
  def human(bytes) when bytes >= 1_000_000_000, do: "#{Float.round(bytes / 1_000_000_000, 1)} GB"
  def human(bytes) when bytes >= 1_000_000, do: "#{Float.round(bytes / 1_000_000, 1)} MB"
  def human(bytes), do: "#{bytes} B"

  # --- internals ---

  # Refused with the offenders named, because "too large" alone leaves the
  # operator to go find the 135 GB qcow2 themselves.
  defp check_ceiling(source, rels, opts) do
    ceiling = opts[:max_bytes] || max_bytes()

    if ceiling <= 0 do
      :ok
    else
      {bytes, largest} = tally(source, rels)

      if bytes <= ceiling do
        :ok
      else
        {:error,
         {:workspace_too_large,
          %{
            bytes: bytes,
            human: human(bytes),
            ceiling: ceiling,
            ceiling_human: human(ceiling),
            largest: Enum.map(largest, fn {rel, n} -> "#{rel} (#{human(n)})" end),
            hint:
              "add these to .gitignore, or run this workspace unsandboxed " <>
                "(workspace_mode: :in_place), or raise :workspace, :max_bytes"
          }}}
      end
    end
  end

  defp tally(source, rels) do
    sized =
      Enum.map(rels, fn rel ->
        case File.stat(Path.join(source, rel)) do
          {:ok, %{size: n}} -> {rel, n}
          _ -> {rel, 0}
        end
      end)

    {Enum.reduce(sized, 0, fn {_r, n}, acc -> acc + n end),
     sized |> Enum.sort_by(fn {_r, n} -> -n end) |> Enum.take(5)}
  end

  defp git_repo?(dir), do: File.dir?(Path.join(dir, ".git")) or File.regular?(Path.join(dir, ".git"))

  defp git(dir, args) do
    System.cmd("git", args, cd: dir, stderr_to_stdout: true, env: Mitos.Hermetic.env(Mitos.Hermetic.git_env(dir)))
  rescue
    _ -> {"", 127}
  end

  defp normalize_mode(m) when is_atom(m), do: m
  defp normalize_mode("in_place"), do: :in_place
  defp normalize_mode("clone"), do: :clone
  defp normalize_mode(false), do: :in_place
  defp normalize_mode(true), do: :clone
  defp normalize_mode(other), do: other

  defp configured_mode, do: config(:mode, :clone)

  defp config(key, default),
    do: Application.get_env(:mitos, :workspace, []) |> Keyword.get(key, default)
end
