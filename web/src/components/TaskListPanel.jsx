import { Show, createMemo } from 'solid-js';
import TaskList from './TaskList';

export default function TaskListPanel(props) {
  const hasRepos = createMemo(() => props.repos.length > 0);
  const showList = createMemo(() => props.tasks.length > 0);

  return (
    <section
      id="tasks"
      class="task-panel"
      aria-labelledby="tasks-title"
      aria-busy={props.loading ? 'true' : 'false'}
    >
      <div class="task-panel-head">
        <div class="task-panel-title-row">
          <h2 id="tasks-title" class="task-panel-title">
            Tasks
          </h2>
          <span class="task-count" aria-live="polite">
            {props.tasks.length}
            <span class="sr-only"> tasks</span>
          </span>
        </div>
        <Show when={props.loading && props.tasks.length > 0}>
          <span class="task-refresh" role="status">
            <span class="sr-only">Refreshing tasks</span>
          </span>
        </Show>
      </div>

      <Show when={props.error}>
        <div class="banner warning task-banner" role="alert">
          <span>
            {props.tasks.length > 0 ? 'Live updates paused. ' : 'Could not load tasks. '}
            {props.error}
          </span>
          <button class="btn btn-ghost btn-sm" type="button" onClick={props.onRetry}>
            Retry
          </button>
        </div>
      </Show>

      <Show when={props.loading && props.tasks.length === 0 && props.repos.length > 0}>
        <div class="task-loading" role="status">
          Loading tasks…
        </div>
      </Show>

      <Show when={showList()}>
        <TaskList
          tasks={props.tasks}
          repos={props.repos}
          projects={props.projects}
          selectedTaskId={props.selectedTaskId}
          approvingId={props.approvingId}
          statusFilter={props.statusFilter}
          onOpen={props.onOpen}
          onApprove={props.onApprove}
        />
      </Show>

      <Show when={!props.loading && !showList()}>
        <div class="task-empty">
          <Show
            when={hasRepos()}
            fallback={
              <>
                <h3>No repositories yet</h3>
                <p>Add a Git repository to start queuing tasks.</p>
                <button class="btn btn-primary" type="button" onClick={props.onAddRepo}>
                  Add repository
                </button>
              </>
            }
          >
            <Show
              when={props.hasFilters}
              fallback={
                <>
                  <h3>No tasks yet</h3>
                  <p>Create the first task in this queue.</p>
                  <button class="btn btn-primary" type="button" onClick={props.onNewTask}>
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
          </Show>
        </div>
      </Show>
    </section>
  );
}
