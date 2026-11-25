import { For, Show } from 'solid-js';
import { PriorityBadge, StatusBadge } from './Badges';
import { formatDateTime, formatFullDateTime, timeAttribute } from '../utils';

export default function TaskTable(props) {
  function repoFor(task) {
    return props.repos.find((repo) => String(repo.id) === String(task.repo_id));
  }

  return (
    <div class="task-table-wrap">
      <table class="task-table">
        <caption class="sr-only">Tasks</caption>
        <thead>
          <tr>
            <th scope="col" class="col-task">
              Task
            </th>
            <th scope="col" class="col-repo">
              Repository
            </th>
            <th scope="col" class="col-status">
              Status
            </th>
            <th scope="col" class="col-priority">
              Priority
            </th>
            <th scope="col" class="col-updated">
              Updated
            </th>
          </tr>
        </thead>
        <tbody>
          <For each={props.tasks}>
            {(task) => {
              const selected = () =>
                props.selectedTaskId != null &&
                String(props.selectedTaskId) === String(task.id);
              const repo = () => repoFor(task);
              return (
                <tr
                  class={{ 'task-row': true, selected: selected() }}
                  onClick={() => props.onOpen(task.id)}
                >
                  <td class="col-task">
                    <button
                      class="task-title-button"
                      type="button"
                      title={task.title || 'Untitled task'}
                      onClick={(event) => {
                        event.stopPropagation();
                        props.onOpen(task.id);
                      }}
                    >
                      <span class="task-id">#{task.id}</span>
                      <span class="task-title-text">{task.title || 'Untitled task'}</span>
                    </button>
                    <Show when={task.markings?.length > 0}>
                      <div class="task-markings">
                        <For each={task.markings}>
                          {(marking) => <span class="marking-chip">{marking}</span>}
                        </For>
                      </div>
                    </Show>
                  </td>
                  <td class="col-repo">
                    <Show
                      when={repo()}
                      fallback={
                        <span class="repo-path muted-small">
                          {task.repo_id == null ? '—' : `Repo #${task.repo_id}`}
                        </span>
                      }
                    >
                      {(found) => (
                        <span class="repo-path" title={found().path}>
                          {found().path}
                        </span>
                      )}
                    </Show>
                  </td>
                  <td class="col-status">
                    <StatusBadge value={task.status} />
                  </td>
                  <td class="col-priority">
                    <PriorityBadge value={task.priority} />
                  </td>
                  <td class="col-updated">
                    <time
                      class="task-updated"
                      datetime={timeAttribute(task.updated_at)}
                      title={formatFullDateTime(task.updated_at)}
                    >
                      {formatDateTime(task.updated_at)}
                    </time>
                  </td>
                </tr>
              );
            }}
          </For>
        </tbody>
      </table>
    </div>
  );
}
