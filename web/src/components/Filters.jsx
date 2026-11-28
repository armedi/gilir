import { For, Show, createMemo, createSignal, createUniqueId } from 'solid-js';
import { PRIORITIES, PRIORITY_LABELS, TASK_GROUPS } from '../constants';
import { basename, repoGroups } from '../utils';

function FilterIcon() {
  return (
    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true">
      <path
        d="M2.5 4.25H13.5M4.5 8H11.5M6.5 11.75H9.5"
        stroke="currentColor"
        stroke-width="1.45"
        stroke-linecap="round"
      />
    </svg>
  );
}

export default function Filters(props) {
  const projectId = createUniqueId();
  const statusId = createUniqueId();
  const repoId = createUniqueId();
  const priorityId = createUniqueId();
  const markingId = createUniqueId();
  const markingListId = createUniqueId();

  const [moreOpen, setMoreOpen] = createSignal(false);

  const groups = createMemo(() =>
    repoGroups(props.projects, props.repos, props.filters.project),
  );
  const repoCount = createMemo(() =>
    groups().groups.reduce((sum, group) => sum + group.repos.length, 0),
  );
  const extraCount = createMemo(() => {
    let count = 0;
    if (props.filters.repo) count += 1;
    if (props.filters.priority) count += 1;
    if (props.filters.marking) count += 1;
    return count;
  });

  const showExtra = () => moreOpen() || extraCount() > 0;

  return (
    <div class="queue-panel-top" aria-label="Queue filters">
      <div class="queue-panel-bar">
        <label class="sr-only" for={projectId}>
          Project
        </label>
        <select
          id={projectId}
          class="select queue-scope-select"
          value={props.filters.project}
          onChange={(event) => props.onChange('project', event.currentTarget.value)}
        >
          <option value="">All projects</option>
          <For each={props.projects}>
            {(project) => <option value={String(project.id)}>{project.name}</option>}
          </For>
        </select>

        <label class="sr-only" for={statusId}>
          Status
        </label>
        <select
          id={statusId}
          class="select queue-scope-select"
          value={props.filters.status}
          onChange={(event) => props.onChange('status', event.currentTarget.value)}
        >
          <option value="">All statuses</option>
          <For each={TASK_GROUPS}>
            {(group) => <option value={group.status}>{group.label}</option>}
          </For>
        </select>

        <button
          type="button"
          class={['filter-icon-btn', { active: showExtra() }]}
          aria-expanded={showExtra() ? 'true' : 'false'}
          aria-label={
            extraCount() > 0
              ? `More filters (${extraCount()} active)`
              : 'More filters'
          }
          title={extraCount() > 0 ? `More filters (${extraCount()} active)` : 'More filters'}
          onClick={() => setMoreOpen((open) => !open)}
        >
          <FilterIcon />
          <Show when={extraCount() > 0}>
            <span class="filter-count">{extraCount()}</span>
          </Show>
        </button>

        <Show when={props.loading && props.taskCount > 0}>
          <span class="task-refresh" role="status">
            <span class="sr-only">Refreshing tasks</span>
          </span>
        </Show>
      </div>

      <Show when={showExtra()}>
        <div class="extra-filters">
          <div class="extra-filters-row">
            <div>
              <label class="sr-only" for={repoId}>
                Repository
              </label>
              <select
                id={repoId}
                class="select filter-control"
                value={props.filters.repo}
                onChange={(event) => props.onChange('repo', event.currentTarget.value)}
                disabled={repoCount() === 0}
                title={repoCount() === 0 ? 'No repositories match this project' : undefined}
              >
                <option value="">All repos</option>
                <For each={groups().groups}>
                  {(group) =>
                    groups().grouped ? (
                      <optgroup label={group.label}>
                        <For each={group.repos}>
                          {(repo) => (
                            <option value={String(repo.id)} title={repo.path}>
                              {basename(repo.path) || repo.path}
                            </option>
                          )}
                        </For>
                      </optgroup>
                    ) : (
                      <For each={group.repos}>
                        {(repo) => (
                          <option value={String(repo.id)} title={repo.path}>
                            {basename(repo.path) || repo.path}
                          </option>
                        )}
                      </For>
                    )
                  }
                </For>
              </select>
            </div>

            <div>
              <label class="sr-only" for={priorityId}>
                Priority
              </label>
              <select
                id={priorityId}
                class="select filter-control"
                value={props.filters.priority}
                onChange={(event) => props.onChange('priority', event.currentTarget.value)}
              >
                <option value="">All priorities</option>
                <For each={PRIORITIES}>
                  {(priority) => <option value={priority}>{PRIORITY_LABELS[priority]}</option>}
                </For>
              </select>
            </div>
          </div>

          <div class="extra-filters-foot">
            <div class="marking-filter">
              <label class="sr-only" for={markingId}>
                Marking
              </label>
              <input
                id={markingId}
                class="input filter-control"
                type="search"
                value={props.markingInput}
                onInput={(event) => props.onMarkingInput(event.currentTarget.value)}
                placeholder="Filter by marking…"
                autocomplete="off"
                list={markingListId}
              />
              <datalist id={markingListId}>
                <For each={props.knownMarkings}>{(marking) => <option value={marking} />}</For>
              </datalist>
            </div>

            <Show when={props.hasFilters}>
              <button class="btn btn-ghost btn-sm" type="button" onClick={props.onReset}>
                Reset
              </button>
            </Show>
          </div>
        </div>
      </Show>
    </div>
  );
}
