import { Show } from 'solid-js';
import TaskTable from './TaskTable';

export default function TaskListPanel(props) {
  return (
    <section
      id="tasks"
      class="panel list-panel"
      aria-labelledby="tasks-title"
      aria-busy={props.loading}
    >
      <div class="list-toolbar">
        <div class="list-title-row">
          <h2 id="tasks-title" class="list-title">
            Tasks
          </h2>
          <span class="task-count">
            {props.tasks.length}
            <span class="sr-only"> tasks</span>
          </span>
        </div>
        <button
          class="btn btn-primary"
          type="button"
          onClick={props.onNewTask}
          disabled={!props.canCreate}
          title={props.canCreate ? undefined : 'Add a repository before creating a task.'}
        >
          New task
        </button>
      </div>

      <Show when={props.loading && props.tasks.length > 0}>
        <div class="refresh-bar" aria-hidden="true" />
      </Show>

      <Show when={props.error}>
        <div class="banner warning" role="alert">
          <span>
            {props.tasks.length > 0 ? 'Live updates paused. ' : 'Could not load tasks. '}
            {props.error}
          </span>
          <button class="btn btn-ghost btn-sm" type="button" onClick={props.onRetry}>
            Retry
          </button>
        </div>
      </Show>

      <Show when={props.loading && props.tasks.length === 0}>
        <div class="loading-state" role="status">
          Loading tasks…
        </div>
      </Show>

      <Show when={!props.loading && props.tasks.length === 0}>
        <div class="empty-state">
          <Show
            when={props.hasFilters}
            fallback={
              <>
                <h3>No tasks yet</h3>
                <p>Add the first task to start the queue.</p>
                <button
                  class="btn btn-primary"
                  type="button"
                  onClick={props.onNewTask}
                  disabled={!props.canCreate}
                >
                  New task
                </button>
              </>
            }
          >
            <h3>No matching tasks</h3>
            <p>No tasks match the current filters.</p>
            <button class="btn btn-secondary" type="button" onClick={props.onClearFilters}>
              Clear filters
            </button>
          </Show>
        </div>
      </Show>

      <Show when={props.tasks.length > 0}>
        <TaskTable
          tasks={props.tasks}
          repos={props.repos}
          selectedTaskId={props.selectedTaskId}
          onOpen={props.onOpen}
        />
      </Show>
    </section>
  );
}
