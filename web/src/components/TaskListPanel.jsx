import { Show, createMemo } from 'solid-js';
import TaskList from './TaskList';

export default function TaskListPanel(props) {
  const hasRepos = createMemo(() => props.repos.length > 0);
  const showList = createMemo(() => props.tasks.length > 0);

  return (
    <div
      id="tasks"
      class="queue-scroll"
      aria-labelledby="tasks-title"
      aria-busy={props.loading ? 'true' : 'false'}
    >
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

      <Show when={props.loading && props.tasks.length === 0 && props.repos.length > 0}>
        <div class="queue-loading" role="status">
          Loading tasks…
        </div>
      </Show>

      <Show when={showList()}>
        <TaskList
          tasks={props.tasks}
          repos={props.repos}
          projects={props.projects}
          selectedTaskId={props.selectedTaskId}
          statusFilter={props.statusFilter}
          onOpen={props.onOpen}
        />
      </Show>

      <Show when={!props.loading && !showList()}>
        <div class="queue-empty">
          <Show
            when={hasRepos()}
            fallback={
              <>
                <h3>No repositories connected</h3>
                <p>Connect a local Git repository to start queuing work.</p>
                <button class="btn btn-primary btn-sm" type="button" onClick={props.onAddRepo}>
                  Add repository
                </button>
              </>
            }
          >
            <Show
              when={props.hasFilters}
              fallback={
                <>
                  <h3>Queue is clear</h3>
                  <p>Create a task to place work in the local dispatch queue.</p>
                  <button class="btn btn-primary btn-sm" type="button" onClick={props.onNewTask}>
                    New task
                  </button>
                </>
              }
            >
              <h3>No matching tasks</h3>
              <p>Widen your active filters to see other tasks in the queue.</p>
              <button class="btn btn-secondary btn-sm" type="button" onClick={props.onClearFilters}>
                Clear filters
              </button>
            </Show>
          </Show>
        </div>
      </Show>
    </div>
  );
}
