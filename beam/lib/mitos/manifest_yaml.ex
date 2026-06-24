defmodule Mitos.ManifestYaml do
  @moduledoc """
  Small YAML decoder for local structured manifests.

  This is intentionally not a general YAML implementation. It supports the
  subset used by mitos manifests: indentation-based maps/lists, flow maps/lists,
  quoted and plain scalars, booleans, numbers, nulls, and comments. Unsupported
  constructs return an error instead of being guessed.
  """

  @type decode_error :: {:unsupported_yaml, String.t()} | {:invalid_yaml, String.t()}

  @doc "Decode a YAML document into string-keyed maps."
  @spec decode(String.t()) :: {:ok, term()} | {:error, decode_error()}
  def decode(raw) when is_binary(raw) do
    with {:ok, lines} <- lines(raw),
         {:ok, value, rest} <- parse_node(lines, first_indent(lines)) do
      case rest do
        [] -> {:ok, value}
        [%{line: line} | _] -> {:error, {:invalid_yaml, "unexpected content at line #{line}"}}
      end
    end
  end

  def decode(_raw), do: {:error, {:invalid_yaml, "document must be a string"}}

  defp lines(raw) do
    raw
    |> String.split("\n")
    |> Enum.with_index(1)
    |> Enum.reduce_while({:ok, []}, fn {line, number}, {:ok, acc} ->
      cond do
        String.contains?(line, "\t") ->
          {:halt, {:error, {:unsupported_yaml, "tabs are not supported at line #{number}"}}}

        true ->
          line = strip_comment(line)

          if String.trim(line) == "" do
            {:cont, {:ok, acc}}
          else
            indent = leading_spaces(line)
            {:cont, {:ok, acc ++ [%{indent: indent, text: String.trim(line), line: number}]}}
          end
      end
    end)
  end

  defp first_indent([]), do: 0
  defp first_indent([%{indent: indent} | _lines]), do: indent

  defp parse_node([], _indent), do: {:ok, %{}, []}

  defp parse_node([%{indent: current} | _] = lines, indent) when current < indent,
    do: {:ok, %{}, lines}

  defp parse_node([%{indent: current} | _lines], indent) when current > indent,
    do: {:error, {:invalid_yaml, "unexpected indentation; expected #{indent}, got #{current}"}}

  defp parse_node([%{text: "- " <> _} | _] = lines, indent), do: parse_list(lines, indent)
  defp parse_node(lines, indent), do: parse_map(lines, indent)

  defp parse_map(lines, indent), do: parse_map(lines, indent, %{})

  defp parse_map([], _indent, acc), do: {:ok, acc, []}

  defp parse_map([%{indent: current} | _] = lines, indent, acc) when current < indent,
    do: {:ok, acc, lines}

  defp parse_map([%{text: "- " <> _} | _] = lines, _indent, acc),
    do: {:ok, acc, lines}

  defp parse_map([%{indent: current, line: line} | _], indent, _acc) when current > indent,
    do: {:error, {:invalid_yaml, "unexpected indentation at line #{line}"}}

  defp parse_map([%{text: text, line: line} | rest], indent, acc) do
    with {:ok, key, raw_value} <- key_value(text, line),
         {:ok, value, rest} <- parse_map_value(raw_value, rest, indent, line) do
      parse_map(rest, indent, Map.put(acc, key, value))
    end
  end

  defp parse_map_value("", [%{indent: child_indent} | _] = rest, indent, line) do
    if child_indent > indent do
      parse_node(rest, child_indent)
    else
      {:error, {:invalid_yaml, "missing value for key at line #{line}"}}
    end
  end

  defp parse_map_value("", [], _indent, line),
    do: {:error, {:invalid_yaml, "missing value for key at line #{line}"}}

  defp parse_map_value(raw_value, rest, _indent, _line) do
    with {:ok, value} <- parse_value(raw_value) do
      {:ok, value, rest}
    end
  end

  defp parse_list(lines, indent), do: parse_list(lines, indent, [])

  defp parse_list([], _indent, acc), do: {:ok, acc, []}

  defp parse_list([%{indent: current} | _] = lines, indent, acc) when current < indent,
    do: {:ok, acc, lines}

  defp parse_list([%{indent: current, line: line} | _], indent, _acc) when current > indent,
    do: {:error, {:invalid_yaml, "unexpected indentation at line #{line}"}}

  defp parse_list([%{text: "- " <> item, line: line} | rest], indent, acc) do
    with {:ok, value, rest} <- parse_list_item(String.trim(item), rest, indent, line) do
      parse_list(rest, indent, acc ++ [value])
    end
  end

  defp parse_list(lines, _indent, acc), do: {:ok, acc, lines}

  defp parse_list_item("", [%{indent: child_indent} | _] = rest, indent, line) do
    if child_indent > indent do
      parse_node(rest, child_indent)
    else
      {:error, {:invalid_yaml, "missing list item value at line #{line}"}}
    end
  end

  defp parse_list_item("", [], _indent, line),
    do: {:error, {:invalid_yaml, "missing list item value at line #{line}"}}

  defp parse_list_item(item, rest, indent, line) do
    with {:ok, value} <- parse_list_item_value(item, line),
         {:ok, value, rest} <- maybe_merge_nested_list_item(value, rest, indent) do
      {:ok, value, rest}
    end
  end

  defp parse_list_item_value(item, line) do
    cond do
      starts_flow?(item) ->
        parse_value(item)

      top_level_key_value?(item) ->
        with {:ok, key, raw_value} <- key_value(item, line),
             {:ok, value} <- parse_value(raw_value) do
          {:ok, %{key => value}}
        end

      true ->
        parse_value(item)
    end
  end

  defp maybe_merge_nested_list_item(%{} = value, [%{indent: child_indent} | _] = rest, indent)
       when child_indent > indent do
    with {:ok, nested, rest} <- parse_node(rest, child_indent),
         true <- is_map(nested) do
      {:ok, Map.merge(value, nested), rest}
    else
      false -> {:error, {:invalid_yaml, "nested list item content must be a map"}}
      {:error, _reason} = error -> error
    end
  end

  defp maybe_merge_nested_list_item(value, rest, _indent), do: {:ok, value, rest}

  defp parse_value(value) do
    value = String.trim(value)

    cond do
      value == "" ->
        {:ok, ""}

      String.starts_with?(value, "|") or String.starts_with?(value, ">") ->
        {:error, {:unsupported_yaml, "block scalar values are not supported"}}

      starts_flow?(value) ->
        parse_flow(value)

      quoted?(value) ->
        parse_quoted(value)

      value in ["null", "Null", "NULL", "~"] ->
        {:ok, nil}

      value in ["true", "True", "TRUE"] ->
        {:ok, true}

      value in ["false", "False", "FALSE"] ->
        {:ok, false}

      Regex.match?(~r/^-?\d+$/, value) ->
        {integer, ""} = Integer.parse(value)
        {:ok, integer}

      Regex.match?(~r/^-?\d+\.\d+$/, value) ->
        {float, ""} = Float.parse(value)
        {:ok, float}

      true ->
        {:ok, value}
    end
  end

  defp parse_flow("[" <> rest) do
    with {:ok, inner} <- closing_flow(rest, "]"),
         {:ok, parts} <- split_top_level(inner, ","),
         {:ok, values} <- parse_flow_values(parts) do
      {:ok, values}
    end
  end

  defp parse_flow("{" <> rest) do
    with {:ok, inner} <- closing_flow(rest, "}"),
         {:ok, parts} <- split_top_level(inner, ","),
         {:ok, entries} <- parse_flow_entries(parts) do
      {:ok, Map.new(entries)}
    end
  end

  defp parse_flow(_value), do: {:error, {:invalid_yaml, "invalid flow value"}}

  defp closing_flow(value, close) do
    if String.ends_with?(value, close) do
      {:ok, value |> String.slice(0, byte_size(value) - 1) |> String.trim()}
    else
      {:error, {:invalid_yaml, "unterminated flow value"}}
    end
  end

  defp parse_flow_values(parts) do
    parts
    |> Enum.reject(&(&1 == ""))
    |> Enum.reduce_while({:ok, []}, fn part, {:ok, acc} ->
      case parse_value(part) do
        {:ok, value} -> {:cont, {:ok, acc ++ [value]}}
        {:error, _reason} = error -> {:halt, error}
      end
    end)
  end

  defp parse_flow_entries(parts) do
    parts
    |> Enum.reject(&(&1 == ""))
    |> Enum.reduce_while({:ok, []}, fn part, {:ok, acc} ->
      with {:ok, key, raw_value} <- key_value(part, nil),
           {:ok, value} <- parse_value(raw_value) do
        {:cont, {:ok, acc ++ [{key, value}]}}
      else
        {:error, _reason} = error -> {:halt, error}
      end
    end)
  end

  defp key_value(text, line) do
    case split_key_value(text) do
      {:ok, key, value} ->
        key = trim_unquoted(key)

        cond do
          key == "" ->
            {:error, {:invalid_yaml, line_error("empty key", line)}}

          String.contains?(key, ["[", "]", "{", "}", ","]) ->
            {:error, {:unsupported_yaml, line_error("complex keys are not supported", line)}}

          true ->
            {:ok, key, String.trim(value)}
        end

      :error ->
        {:error, {:invalid_yaml, line_error("expected key: value", line)}}
    end
  end

  defp split_key_value(text) do
    chars = String.graphemes(text)

    chars
    |> Enum.reduce_while({0, nil, []}, fn char, {depth, quote, left} ->
      cond do
        quote && char == quote ->
          {:cont, {depth, nil, left ++ [char]}}

        quote ->
          {:cont, {depth, quote, left ++ [char]}}

        char in ["\"", "'"] ->
          {:cont, {depth, char, left ++ [char]}}

        char in ["[", "{"] ->
          {:cont, {depth + 1, nil, left ++ [char]}}

        char in ["]", "}"] ->
          {:cont, {max(depth - 1, 0), nil, left ++ [char]}}

        char == ":" and depth == 0 ->
          left_binary = Enum.join(left)
          rest_binary = chars |> Enum.drop(length(left) + 1) |> Enum.join()
          {:halt, {:ok, left_binary, rest_binary}}

        true ->
          {:cont, {depth, nil, left ++ [char]}}
      end
    end)
    |> case do
      {:ok, key, value} -> {:ok, key, value}
      {_depth, _quote, _left} -> :error
    end
  end

  defp split_top_level("", _separator), do: {:ok, []}

  defp split_top_level(text, separator) do
    text
    |> String.graphemes()
    |> Enum.reduce_while({:ok, 0, nil, [], []}, fn char, {:ok, depth, quote, current, parts} ->
      cond do
        quote && char == quote ->
          {:cont, {:ok, depth, nil, current ++ [char], parts}}

        quote ->
          {:cont, {:ok, depth, quote, current ++ [char], parts}}

        char in ["\"", "'"] ->
          {:cont, {:ok, depth, char, current ++ [char], parts}}

        char in ["[", "{"] ->
          {:cont, {:ok, depth + 1, nil, current ++ [char], parts}}

        char in ["]", "}"] and depth > 0 ->
          {:cont, {:ok, depth - 1, nil, current ++ [char], parts}}

        char in ["]", "}"] ->
          {:halt, {:error, {:invalid_yaml, "unbalanced flow value"}}}

        char == separator and depth == 0 ->
          part = current |> Enum.join() |> String.trim()
          {:cont, {:ok, depth, nil, [], parts ++ [part]}}

        true ->
          {:cont, {:ok, depth, nil, current ++ [char], parts}}
      end
    end)
    |> case do
      {:ok, 0, nil, current, parts} ->
        {:ok, parts ++ [current |> Enum.join() |> String.trim()]}

      {:ok, _depth, _quote, _current, _parts} ->
        {:error, {:invalid_yaml, "unterminated flow value"}}

      {:error, _reason} = error ->
        error
    end
  end

  defp parse_quoted(<<"\"", body::binary>>) do
    with true <- String.ends_with?(body, "\""),
         inner <- String.slice(body, 0, byte_size(body) - 1) do
      {:ok,
       inner
       |> String.replace(~s(\\n), "\n")
       |> String.replace(~s(\\"), ~s("))
       |> String.replace(~s(\\\\), ~s(\\))}
    else
      _ -> {:error, {:invalid_yaml, "unterminated quoted string"}}
    end
  end

  defp parse_quoted("'" <> body) do
    if String.ends_with?(body, "'") do
      {:ok, String.slice(body, 0, byte_size(body) - 1)}
    else
      {:error, {:invalid_yaml, "unterminated quoted string"}}
    end
  end

  defp parse_quoted(_value), do: {:error, {:invalid_yaml, "invalid quoted string"}}

  defp strip_comment(line), do: strip_comment(String.graphemes(line), nil, nil, [])

  defp strip_comment([], _quote, _previous, acc), do: Enum.join(acc)

  defp strip_comment(["#" | _rest], nil, previous, acc) when previous in [nil, " ", "\t"],
    do: Enum.join(acc)

  defp strip_comment([char | rest], quote, _previous, acc) when quote == char,
    do: strip_comment(rest, nil, char, acc ++ [char])

  defp strip_comment([char | rest], nil, _previous, acc) when char in ["\"", "'"],
    do: strip_comment(rest, char, char, acc ++ [char])

  defp strip_comment([char | rest], quote, _previous, acc),
    do: strip_comment(rest, quote, char, acc ++ [char])

  defp leading_spaces(line),
    do: line |> String.length() |> Kernel.-(String.length(String.trim_leading(line)))

  defp starts_flow?(value), do: String.starts_with?(value, ["[", "{"])
  defp quoted?(value), do: String.starts_with?(value, ["\"", "'"])
  defp top_level_key_value?(value), do: match?({:ok, _key, _value}, split_key_value(value))

  defp trim_unquoted(value) do
    value = String.trim(value)

    if quoted?(value) do
      case parse_quoted(value) do
        {:ok, parsed} -> parsed
        {:error, _reason} -> value
      end
    else
      value
    end
  end

  defp line_error(message, nil), do: message
  defp line_error(message, line), do: "#{message} at line #{line}"
end
