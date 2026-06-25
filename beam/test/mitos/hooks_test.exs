defmodule Mitos.HooksTest do
  use ExUnit.Case, async: true

  test "run rejects cwd paths containing symlinked parents before executing hooks" do
    outside = unique_tmp_dir("hooks-cwd-symlink-outside")
    link = unique_tmp_dir("hooks-cwd-symlink-link")

    File.mkdir_p!(Path.join(outside, "workspace"))
    File.ln_s!(outside, link)

    on_exit(fn ->
      File.rm_rf!(outside)
      File.rm(link)
    end)

    [result] =
      Mitos.Hooks.run(
        :pre,
        %{
          hooks: %{
            pre: [
              %{
                name: "prepare",
                run: ["/bin/sh", "-c", "printf ran > hook-ran.txt"]
              }
            ]
          }
        },
        cwd: Path.join(link, "workspace")
      )

    refute result["success?"]
    assert result["blocked?"]
    assert result["exit_status"] == 127
    assert result["error"] =~ "hook cwd must not contain symlinks"
    refute File.exists?(Path.join([outside, "workspace", "hook-ran.txt"]))
  end

  test "run rejects malformed env before executing hooks" do
    cwd = unique_tmp_dir("hooks-env-invalid")
    File.mkdir_p!(cwd)

    on_exit(fn -> File.rm_rf!(cwd) end)

    [result] =
      Mitos.Hooks.run(
        :pre,
        hook_spec("printf ran > hook-ran.txt"),
        cwd: cwd,
        env: %{"PATH=/tmp" => "ignored"}
      )

    refute result["success?"]
    assert result["blocked?"]
    assert result["exit_status"] == 127
    assert result["error"] =~ "hook env name must not contain ="
    refute File.exists?(Path.join(cwd, "hook-ran.txt"))
  end

  test "run passes validated list-form env to hooks" do
    cwd = unique_tmp_dir("hooks-env-valid")
    File.mkdir_p!(cwd)

    on_exit(fn -> File.rm_rf!(cwd) end)

    [result] =
      Mitos.Hooks.run(
        :pre,
        hook_spec(~s(printf "%s" "$MITOS_HOOK_ENV" > hook-env.txt)),
        cwd: cwd,
        env: [%{"name" => "MITOS_HOOK_ENV", "value" => "ok"}]
      )

    assert result["success?"]
    refute result["blocked?"]
    assert File.read!(Path.join(cwd, "hook-env.txt")) == "ok"
  end

  defp unique_tmp_dir(prefix) do
    path = Path.join(System.tmp_dir!(), "mitos-#{prefix}-#{System.unique_integer([:positive])}")
    File.rm_rf!(path)
    path
  end

  defp hook_spec(command) do
    %{
      hooks: %{
        pre: [
          %{
            name: "prepare",
            run: ["/bin/sh", "-c", command]
          }
        ]
      }
    }
  end
end
