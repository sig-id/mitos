defmodule MitosWeb.ErrorHTML do
  @moduledoc false
  use MitosWeb, :html

  def render("404.html", _assigns) do
    "Not found"
  end

  def render("500.html", _assigns) do
    "Internal server error"
  end

  def template_not_found(_template, _assigns) do
    "Internal server error"
  end
end
