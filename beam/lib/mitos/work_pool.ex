defmodule Mitos.WorkPool do
  @moduledoc """
  The Work Pool plane (06). Durable Tasks in pools, each picked by a pluggable
  discipline; a platform drainer empties it into dispatch → a run.

  MVP scope (09 §2 / 02 §7 non-goals): the pool is present as the single durable
  intake for every provenance (a buyer's paid request is a Task like any other),
  but deps/disciplines/invoke/services are deferred. This holds Tasks and their
  status transitions, each recorded as an Effect on the owning run's log where a
  run exists; standalone tasks keep an in-memory lifecycle.
  """
  use GenServer

  alias Mitos.Task, as: WorkTask

  def start_link(_opts \\ []), do: GenServer.start_link(__MODULE__, %{}, name: __MODULE__)

  @impl true
  def init(_), do: {:ok, %{tasks: %{}}}

  @doc "Enqueue a Task (01 §8.7). Returns the stored Task."
  def enqueue(%WorkTask{} = task), do: GenServer.call(__MODULE__, {:enqueue, task})

  def get(id), do: GenServer.call(__MODULE__, {:get, id})

  def update_status(id, status), do: GenServer.call(__MODULE__, {:update_status, id, status})

  def all, do: GenServer.call(__MODULE__, :all)

  @impl true
  def handle_call({:enqueue, task}, _from, state) do
    task = %{task | status: :queued}
    {:reply, task, put_in(state, [:tasks, task.id], task)}
  end

  def handle_call({:get, id}, _from, state), do: {:reply, get_in(state, [:tasks, id]), state}

  def handle_call({:update_status, id, status}, _from, state) do
    case get_in(state, [:tasks, id]) do
      nil -> {:reply, {:error, :not_found}, state}
      task -> {:reply, :ok, put_in(state, [:tasks, id], %{task | status: status})}
    end
  end

  def handle_call(:all, _from, state), do: {:reply, Map.values(state.tasks), state}
end
