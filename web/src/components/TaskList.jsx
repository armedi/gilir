import { For, Show, createEffect, createMemo, createSignal, createUniqueId } from 'solid-js';
import { knownPriority, markingTone } from '../tone';
import {
  asArray,
  basename,
  formatFullDateTime,
  formatRelativeTime,
  groupTasks,
  priorityLabel,
  timeAttribute,
} from '../utils';

const PRIORITY_BARS = { urgent: 4, high: 3, medium: 2, low: 1 };

function PriorityMark(props) {
  const bars = () => PRIORITY_BARS[props.value] ?? 0;
  return (
    <span
      class={`priority-mark priority-${knownPriority(props.value)}`}
      title={`Priority: ${priorityLabel(props.value)}`}
    >
      <For each={[0, 1, 2, 3]}>
        {(index) => (
          <span class={['priority-bar', { on: index < bars() }]} aria-hidden="true" />
        )}
      </For>
      <span class="sr-only">Priority: {priorityLabel(props.value)}</span>
    </span>
  );
}

function TaskRow(props) {
  const task = () => props.task;
  const repo = () =>
    asArray(props.repos).find((item) => String(item.id) === String(task().repo_id));
  const project = () =>
    asArray(props.projects).find((item) => String(item.id) === String(task().project_id));
  const selected = () =>
    props.selectedTaskId != null && String(props.selectedTaskId) === String(task().id);
  const markings = () => asArray(task().markings);
  const repoName = () => {
    const found = repo();
    return found ? basename(found.path) || found.path : `Repo #${task().repo_id ?? '-'}`;
  };

  return (
    <li class={['task-row', { selected: selected() }]}>
      <button
        type="button"
        class="task-row-open"
        onClick={() => props.onOpen(task().id)}
        aria-current={selected() ? 'true' : undefined}
      >
        <span class="task-row-top">
          <PriorityMark value={task().priority} />
          <span class="task-id">{task().id}</span>
          <span class="task-title">{task().title || 'Untitled task'}</span>
        </span>

        <span class="task-row-meta">
          <span class="task-row-sub">
            <Show when={project()}>
              {(found) => <span>{found().name}</span>}
            </Show>
            <Show when={project() && repoName() && project().name !== repoName()}>
              <span aria-hidden="true">/</span>
              <span class="task-repo-mono" title={repo()?.path}>
                {repoName()}
              </span>
            </Show>
            <Show when={!project()}>
              <span class="task-repo-mono" title={repo()?.path}>
                {repoName()}
              </span>
            </Show>
            <Show when={markings().length > 0}>
              <For each={markings()}>
                {(marking) => (
                  <span class={`marking-inline tone-${markingTone(marking)}`}>{marking}</span>
                )}
              </For>
            </Show>
          </span>
          <time
            class="task-time"
            datetime={timeAttribute(task().updated_at)}
            title={formatFullDateTime(task().updated_at)}
          >
            {formatRelativeTime(task().updated_at)}
          </time>
        </span>
      </button>
    </li>
  );
}

function GroupSection(props) {
  const headingId = createUniqueId();
  const panelId = createUniqueId();
  const collapsed = () => props.isCollapsed(props.group.status);

  return (
    <section class="task-group" aria-labelledby={headingId}>
      <h3 class="task-group-heading">
        <button
          id={headingId}
          type="button"
          class="task-group-toggle"
          aria-expanded={collapsed() ? 'false' : 'true'}
          aria-controls={panelId}
          onClick={() => props.onToggle(props.group.status)}
        >
          <span
            class={['task-group-chevron', { collapsed: collapsed() }]}
            aria-hidden="true"
          >
            <svg width="12" height="12" viewBox="0 0 12 12" fill="none">
              <path
                d="M4.25 2.75L7.75 6L4.25 9.25"
                stroke="currentColor"
                stroke-width="1.5"
                stroke-linecap="round"
                stroke-linejoin="round"
              />
            </svg>
          </span>
          <span class={`task-group-dot status-${props.group.status}`} aria-hidden="true" />
          <span class="task-group-label">{props.group.label}</span>
          <span class="task-group-count">{props.group.tasks.length}</span>
        </button>
      </h3>
      <div id={panelId} hidden={collapsed()}>
        <ul class="task-rows">
          <For each={props.group.tasks}>
            {(task) => (
              <TaskRow
                task={task}
                repos={props.repos}
                projects={props.projects}
                selectedTaskId={props.selectedTaskId}
                onOpen={props.onOpen}
              />
            )}
          </For>
        </ul>
      </div>
    </section>
  );
}

export default function TaskList(props) {
  const [overrides, setOverrides] = createSignal({});
  const groups = createMemo(() => groupTasks(props.tasks));

  // Done starts collapsed; filtering to Done expands it. Other groups start open.
  function isCollapsed(status) {
    const override = overrides()[status];
    if (override !== undefined) return override;
    return status === 'done' && props.statusFilter !== 'done';
  }

  createEffect(
    () => props.statusFilter,
    (status) => {
      if (status !== 'done') return;
      setOverrides((previous) => {
        if (previous.done === undefined) return previous;
        const next = { ...previous };
        delete next.done;
        return next;
      });
    },
  );

  function toggle(status) {
    setOverrides((previous) => ({ ...previous, [status]: !isCollapsed(status) }));
  }

  return (
    <div class="task-list">
      <For each={groups()}>
        {(group) => (
          <GroupSection
            group={group}
            repos={props.repos}
            projects={props.projects}
            selectedTaskId={props.selectedTaskId}
            isCollapsed={isCollapsed}
            onToggle={toggle}
            onOpen={props.onOpen}
          />
        )}
      </For>
    </div>
  );
}
