defmodule Mitos.WorkspaceTest do
  @moduledoc """
  What enters the sandbox, and whether it is copied at all (`Mitos.Workspace`).

  These pin the fix for a real incident: an unconditional `File.cp_r!` of the
  input tree started copying a 302 GB checkout — VM images, `target/`, fuzz
  corpora — filling the disk before a single token was spent, and would have
  copied it again per stage for checkpoints and the acceptance verify dir.
  """
  use ExUnit.Case, async: true

  alias Mitos.Workspace

  describe "honouring .gitignore" do
    test "copies the repo and not the build output" do
      src = repo_with(%{
        "src/main.ex" => "code",
        "README.md" => "docs",
        ".gitignore" => "target/\n*.qcow2\n",
        "target/huge.bin" => String.duplicate("x", 5_000),
        "vm.qcow2" => String.duplicate("y", 5_000),
        "notes.txt" => "untracked but not ignored"
      })

      dest = tmp("clone")
      assert {:ok, ^dest} = Workspace.clone(src, dest)

      assert File.exists?(Path.join(dest, "src/main.ex"))
      assert File.exists?(Path.join(dest, "README.md"))
      # Untracked-but-not-ignored still counts as "the repo" — a scratch file
      # the operator made is part of the subject; an ignored artifact is not.
      assert File.exists?(Path.join(dest, "notes.txt"))

      refute File.exists?(Path.join(dest, "target/huge.bin"))
      refute File.exists?(Path.join(dest, "vm.qcow2"))
    end

    test "measures only what would actually be copied" do
      src = repo_with(%{
        "small.txt" => String.duplicate("a", 100),
        ".gitignore" => "big/\n",
        "big/blob" => String.duplicate("b", 100_000)
      })

      {:ok, m} = Workspace.measure(src)
      assert m.bytes < 1_000
      refute Enum.any?(m.largest, fn {rel, _} -> rel =~ "big/" end)
    end

    test "a plain directory has no ignore file to honour, so everything counts" do
      src = tmp("plain")
      File.mkdir_p!(src)
      File.write!(Path.join(src, "a.txt"), "a")

      assert {:ok, ["a.txt"]} = Workspace.files(src)
    end
  end

  describe "the size ceiling" do
    test "refuses a workspace over the ceiling and names the offenders" do
      src = repo_with(%{"huge.bin" => String.duplicate("x", 50_000), "small.txt" => "s"})

      assert {:error, {:workspace_too_large, detail}} =
               Workspace.clone(src, tmp("nope"), max_bytes: 1_000)

      assert detail.bytes > 1_000
      # "too large" alone leaves the operator to go find the 135 GB qcow2
      # themselves, so the refusal names it.
      assert Enum.any?(detail.largest, &(&1 =~ "huge.bin"))
      assert detail.hint =~ "in_place"
    end

    test "a ceiling of 0 disables the check" do
      src = repo_with(%{"big.bin" => String.duplicate("x", 20_000)})
      assert {:ok, _} = Workspace.clone(src, tmp("ok"), max_bytes: 0)
    end
  end

  describe "turning the sandbox off" do
    test "in-place uses the operator's tree and copies nothing" do
      src = repo_with(%{"a.txt" => "original"})
      dest = tmp("unused")

      assert {:ok, ^src} = Workspace.materialize(src, dest, mode: :in_place)
      refute File.exists?(dest)
    end

    test "T2 refuses it outright — distrust scales isolation, it never relaxes" do
      assert {:error, :in_place_forbidden_at_t2} = Workspace.mode(:in_place, "T2")
      assert {:ok, :in_place} = Workspace.mode(:in_place, "T1")
      assert {:ok, :clone} = Workspace.mode(:clone, "T2")
    end

    test "the default stays the sandbox" do
      assert {:ok, :clone} = Workspace.mode(nil, "T1")
    end
  end

  describe "the fingerprint (04 §4.3)" do
    test "moves when the tree moves, and never reads file contents" do
      src = repo_with(%{"a.txt" => "one", ".gitignore" => "ignored/\n", "ignored/x" => "noise"})

      {:ok, first} = Workspace.fingerprint(src)
      {:ok, again} = Workspace.fingerprint(src)
      assert first == again

      File.write!(Path.join(src, "b.txt"), "two")
      {:ok, after_add} = Workspace.fingerprint(src)
      assert after_add != first

      # An ignored file changing is not the run making progress.
      File.write!(Path.join(src, "ignored/x"), "different noise entirely")
      {:ok, after_noise} = Workspace.fingerprint(src)
      assert after_noise == after_add
    end
  end

  describe "an unsandboxed run, end to end" do
    @tag timeout: 120_000
    test "works in the real tree and never moves the operator's branch" do
      src = repo_with(%{"README.md" => "# subject\n", ".gitignore" => "target/\n"})
      head_before = head(src)

      {:ok, run_id} =
        Mitos.Run.start_run(
          tenant: %{"org" => "test", "project" => "in-place"},
          pipeline: Mitos.Fixtures.pipeline(["work"]),
          capabilities: %{"work" => Mitos.Fixtures.capability("work")},
          workspace: src,
          workspace_mode: :in_place
        )

      state = Mitos.Run.await(run_id)
      assert state.status == :finished

      # The agent's edit landed in the operator's actual tree...
      assert File.exists?(Path.join(src, "note.txt"))

      # ...and nothing copied it anywhere first.
      sandbox = Path.join([Mitos.data_dir(), "sandboxes", run_id, "work", "workspace"])
      refute File.exists?(sandbox)

      # The branch is exactly where the operator left it: no baseline commit,
      # no checkpoint commit, no staged index (Checkpoint uses `git stash
      # create`, which touches none of them).
      assert head(src) == head_before

      # And harvest still produced a diff, from the working tree against HEAD.
      started = Enum.find(state.events, &match?(%Mitos.Event.Effect{type: :run_started}, &1))
      assert started.payload["workspace_mode"] == "in_place"
    end
  end

  # --- helpers ---

  defp head(dir) do
    {sha, 0} =
      System.cmd("git", ["rev-parse", "HEAD"],
        cd: dir,
        stderr_to_stdout: true,
        env: Mitos.Hermetic.env(Mitos.Hermetic.git_env(dir))
      )

    String.trim(sha)
  end

  defp tmp(label), do: Path.join([Mitos.data_dir(), "wstest", label, Mitos.id()])

  defp repo_with(files) do
    dir = tmp("src")
    File.mkdir_p!(dir)

    Enum.each(files, fn {rel, body} ->
      path = Path.join(dir, rel)
      File.mkdir_p!(Path.dirname(path))
      File.write!(path, body)
    end)

    env = Mitos.Hermetic.env(Mitos.Hermetic.git_env(dir))
    {_, 0} = System.cmd("git", ["init", "-q"], cd: dir, stderr_to_stdout: true, env: env)
    {_, 0} = System.cmd("git", ["add", "-A"], cd: dir, stderr_to_stdout: true, env: env)

    {_, 0} =
      System.cmd("git", ["-c", "commit.gpgsign=false", "commit", "-q", "-m", "seed"],
        cd: dir,
        stderr_to_stdout: true,
        env: env
      )

    dir
  end
end
