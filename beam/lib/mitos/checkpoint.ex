defmodule Mitos.Checkpoint do
  @moduledoc """
  Segments and rewind (`16 §3`).

  The build rule this exists to serve: **do not run a twelve-hour task.** Run
  forty twenty-minute segments, each ending green, and on failure rewind to the
  last green state with a failure memo rather than repairing forward from a
  corrupted one. Repair-in-place is how long runs die.

  A checkpoint pairs four things taken at the same quiescence edge (`14 §2.2`):

      workspace_commit   the tree, committed
      sandbox_snapshot   the provider snapshot, or an archived tree when the
                         provider returns :unsupported (05 §8)
      seq                the log offset — the fold prefix this state belongs to
      predicate          the acceptance verdict, which is what makes it `green`

  `05 §8`'s snapshot/resume is **crash** recovery; this is **wrongness**
  recovery, layered on the same primitive. Rewind never rewrites the log — it
  appends — so the abandoned segment stays on the record and attribution
  (`16 §6.1`) remains possible.
  """

  @enforce_keys [:checkpoint_id, :run_id, :stage_id, :seq]
  defstruct checkpoint_id: nil,
            run_id: nil,
            stage_id: nil,
            seq: nil,
            workspace_commit: nil,
            sandbox_snapshot: nil,
            archive: nil,
            predicate: %{verdict: :skipped, delta: nil, score: nil, evidence_ref: nil},
            green: false,
            ts: nil

  @type t :: %__MODULE__{}

  @doc """
  Take a checkpoint at a step boundary.

  Required ctx: `run_id`, `stage_id`, `seq`, `workspace`. Optional: `provider`,
  `session`, `acceptance` (an `Mitos.Acceptance.run/1` outcome), `append`,
  `caused_by`.
  """
  def take(ctx) do
    id = Mitos.id()
    acc = ctx[:acceptance] || %{verdict: :skipped, delta: nil, score: nil}

    cp = %__MODULE__{
      checkpoint_id: id,
      run_id: ctx.run_id,
      stage_id: ctx[:stage_id],
      seq: ctx[:seq],
      workspace_commit: commit(ctx[:workspace], ctx[:workspace_mode] || :clone, id),
      sandbox_snapshot: snapshot(ctx[:provider], ctx[:session]),
      archive: archive(ctx, id),
      predicate: %{
        verdict: acc[:verdict] || :skipped,
        delta: acc[:delta],
        score: acc[:score],
        evidence_ref: acc[:verify_dir]
      },
      # `green` is the predicate's verdict and nothing else. A step that
      # "looks done" but has no passing oracle is not a state worth rewinding
      # to, so it must not be recorded as one (16 §3.2).
      green: (acc[:verdict] || :skipped) == :pass,
      ts: Mitos.ts()
    }

    emit(ctx, cp)
    cp
  end

  @doc """
  Restore a checkpoint's workspace into `dest`. Prefers the provider snapshot
  where one exists; otherwise replays the archived tree — slower and equally
  correct (`16 §3.1`).
  """
  def restore(%__MODULE__{} = cp, dest) do
    cond do
      cp.archive && File.dir?(cp.archive) ->
        File.rm_rf!(dest)
        File.mkdir_p!(Path.dirname(dest))
        File.cp_r!(cp.archive, dest)
        {:ok, :archive}

      cp.sandbox_snapshot ->
        {:ok, {:snapshot, cp.sandbox_snapshot}}

      true ->
        {:error, :nothing_to_restore}
    end
  end

  @doc """
  Restore **in place** — the operator's own tree, rewound to a checkpoint's
  commit object (`16 §3.3`).

  Deliberately restores tracked content only (`git checkout <sha> -- .`) and
  leaves untracked files alone. A rewind is allowed to undo the run's edits; it
  is not allowed to delete a file the operator created and git never knew about.
  That asymmetry is the price of working without a sandbox, and it is stated
  rather than hidden: an in-place rewind is not a perfect inverse.
  """
  def restore_in_place(%__MODULE__{workspace_commit: nil}, _workspace),
    do: {:error, :nothing_to_restore}

  def restore_in_place(%__MODULE__{workspace_commit: sha}, workspace) do
    case git(workspace, ["checkout", sha, "--", "."]) do
      {_, 0} -> {:ok, :in_place}
      {out, code} -> {:error, {:git_restore_failed, code, String.trim(out)}}
    end
  end

  @doc """
  The last green checkpoint at or before `seq`, from the folded run state. This
  is the default `rewind` target: rewinding to a non-green checkpoint requires
  an explicit `allow_non_green` (`16 §3.3`).
  """
  def last_green(checkpoints, seq \\ nil) do
    checkpoints
    |> Enum.filter(&(&1.green and (is_nil(seq) or &1.seq <= seq)))
    |> Enum.max_by(& &1.seq, fn -> nil end)
  end

  @doc "Fold a `checkpoint_taken` Effect back into a struct."
  def from_effect(%Mitos.Event.Effect{type: :checkpoint_taken} = e) do
    p = e.payload

    %__MODULE__{
      checkpoint_id: e.ref["checkpoint_id"],
      run_id: e.run_id,
      stage_id: e.ref["stage_id"],
      seq: p["seq"],
      workspace_commit: p["workspace_commit"],
      sandbox_snapshot: p["sandbox_snapshot"],
      archive: p["archive"],
      predicate: %{
        verdict: atomize(p["predicate"]["verdict"]),
        delta: p["predicate"]["delta"],
        score: p["predicate"]["score"],
        evidence_ref: p["predicate"]["evidence_ref"]
      },
      green: !!p["green"],
      ts: e.ts
    }
  end

  # --- internals ---

  defp commit(nil, _mode, _id), do: nil

  # `:clone` — the sandbox repo is ours; commit into it freely.
  defp commit(workspace, :clone, _id) do
    with true <- git_repo?(workspace),
         {_, 0} <- git(workspace, ["add", "-A"]),
         {_, _} <- git(workspace, ["commit", "-q", "-m", "mitos checkpoint", "--allow-empty"]),
         {sha, 0} <- git(workspace, ["rev-parse", "HEAD"]) do
      String.trim(sha)
    else
      _ -> nil
    end
  end

  # `:in_place` — the repository belongs to the operator. `git add -A` would
  # stage their whole tree and `git commit` would move their branch; a
  # checkpoint must be invisible to them.
  #
  # `git stash create` builds a commit object for the current working tree and
  # touches nothing — not HEAD, not the index, not the stash ref. Pinning it
  # under `refs/mitos/checkpoints/` keeps it from being collected, and leaves a
  # namespace the operator can inspect or delete wholesale.
  defp commit(workspace, :in_place, id) do
    with true <- git_repo?(workspace),
         {out, 0} <- git(workspace, ["stash", "create"]),
         sha when sha != "" <- String.trim(out),
         {_, 0} <- git(workspace, ["update-ref", "refs/mitos/checkpoints/" <> id, sha]) do
      sha
    else
      # A clean tree has nothing to stash: HEAD already is the state.
      _ ->
        case git(workspace, ["rev-parse", "HEAD"]) do
          {sha, 0} -> String.trim(sha)
          _ -> nil
        end
    end
  end

  defp snapshot(nil, _session), do: nil
  defp snapshot(_provider, nil), do: nil

  defp snapshot(provider, session) do
    case provider.snapshot(session) do
      {:ok, snap} -> snap
      :unsupported -> nil
      _ -> nil
    end
  rescue
    _ -> nil
  end

  # When the provider has no snapshot, an archive is the recovery path — but it
  # is a COPY OF THE WORKSPACE, once per step. Taken unconditionally it turns a
  # large checkout into a per-stage disk multiplier, so it honours `.gitignore`
  # and the same ceiling a clone does (`Mitos.Workspace`), and declines rather
  # than fills the disk.
  #
  # Declining is safe here in a way it would not be elsewhere: `workspace_commit`
  # is taken first, so a rewind still has a git object to restore from. The
  # archive is the belt to that braces.
  defp archive(ctx, id) do
    with workspace when is_binary(workspace) <- ctx[:workspace],
         true <- File.dir?(workspace),
         dest = Path.join([Mitos.run_dir(ctx.run_id), "checkpoints", id, "workspace"]),
         {:ok, _} <- Mitos.Workspace.clone(workspace, dest) do
      dest
    else
      _ -> nil
    end
  end

  defp emit(%{append: append} = ctx, cp) when is_function(append, 1) do
    fields = [
      type: :checkpoint_taken,
      ref: %{"checkpoint_id" => cp.checkpoint_id, "stage_id" => cp.stage_id},
      payload: %{
        "seq" => cp.seq,
        "workspace_commit" => cp.workspace_commit,
        "sandbox_snapshot" => cp.sandbox_snapshot && inspect(cp.sandbox_snapshot),
        "archive" => cp.archive,
        "green" => cp.green,
        "predicate" => %{
          "verdict" => to_string(cp.predicate.verdict),
          "delta" => cp.predicate.delta,
          "score" => cp.predicate.score,
          "evidence_ref" => cp.predicate.evidence_ref
        }
      }
    ]

    append.(if ctx[:caused_by], do: [{:caused_by, ctx[:caused_by]} | fields], else: fields)
  end

  defp emit(_ctx, _cp), do: :ok

  defp git_repo?(dir), do: File.dir?(Path.join(dir, ".git")) or File.regular?(Path.join(dir, ".git"))

  defp git(dir, args) do
    System.cmd("git", ["-c", "commit.gpgsign=false" | args],
      cd: dir,
      stderr_to_stdout: true,
      env: Mitos.Hermetic.env(Mitos.Hermetic.git_env(dir))
    )
  rescue
    _ -> {"", 127}
  end

  defp atomize(nil), do: :skipped
  defp atomize(s) when is_binary(s), do: String.to_existing_atom(s)
  defp atomize(a) when is_atom(a), do: a
end
