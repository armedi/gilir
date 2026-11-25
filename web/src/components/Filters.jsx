import { For, Show, createMemo, createUniqueId } from 'solid-js';
import { PRIORITIES, PRIORITY_LABELS, STATUSES, STATUS_LABELS } from '../constants';
import { basename, repoGroups } from '../utils';

export default function Filters(props) {
  const projectId = createUniqueId();
  const repoId = createUniqueId();
  const statusId = createUniqueId();
  const priorityId = createUniqueId();
  const markingId = createUniqueId();
  const markingListId = createUniqueId();

  const groups = createMemo(() =>
    repoGroups(props.projects, props.repos, props.filters.project),
  );
  const repoCount = createMemo(() => groups().groups.reduce((sum, group) => sum + group.repos.length, 0));

  return (
    <section class="filter-bar" aria-label="Task filters">
      <div class="filter-field">
        <label class="filter-label" for={projectId}>
          Project
        </label>
        <select
          id={projectId}
          class="select filter-select"
          value={props.filters.project}
          onChange={(event) => props.onChange('project', event.currentTarget.value)}
        >
          <option value="">All projects</option>
          <For each={props.projects}>
            {(project) => <option value={String(project.id)}>{project.name}</option>}
          </For>
        </select>
      </div>

      <div class="filter-field filter-field-repo">
        <label class="filter-label" for={repoId}>
          Repository
        </label>
        <select
          id={repoId}
          class="select filter-select"
          value={props.filters.repo}
          onChange={(event) => props.onChange('repo', event.currentTarget.value)}
          disabled={repoCount() === 0}
          title={repoCount() === 0 ? 'No repositories match this project' : undefined}
        >
          <option value="">All repositories</option>
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

      <div class="filter-field">
        <label class="filter-label" for={statusId}>
          Status
        </label>
        <select
          id={statusId}
          class="select filter-select"
          value={props.filters.status}
          onChange={(event) => props.onChange('status', event.currentTarget.value)}
        >
          <option value="">All statuses</option>
          <For each={STATUSES}>
            {(status) => <option value={status}>{STATUS_LABELS[status]}</option>}
          </For>
        </select>
      </div>

      <div class="filter-field">
        <label class="filter-label" for={priorityId}>
          Priority
        </label>
        <select
          id={priorityId}
          class="select filter-select"
          value={props.filters.priority}
          onChange={(event) => props.onChange('priority', event.currentTarget.value)}
        >
          <option value="">All priorities</option>
          <For each={PRIORITIES}>
            {(priority) => <option value={priority}>{PRIORITY_LABELS[priority]}</option>}
          </For>
        </select>
      </div>

      <div class="filter-field filter-field-marking">
        <label class="filter-label" for={markingId}>
          Marking
        </label>
        <input
          id={markingId}
          class="input filter-select"
          type="search"
          value={props.markingInput}
          onInput={(event) => props.onMarkingInput(event.currentTarget.value)}
          placeholder="Any marking"
          autocomplete="off"
          list={markingListId}
        />
        <datalist id={markingListId}>
          <For each={props.knownMarkings}>{(marking) => <option value={marking} />}</For>
        </datalist>
      </div>

      <Show when={props.hasFilters}>
        <button class="btn btn-ghost btn-sm filter-clear" type="button" onClick={props.onReset}>
          Clear filters
        </button>
      </Show>
    </section>
  );
}
