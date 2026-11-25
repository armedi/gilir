import { For, createUniqueId } from 'solid-js';
import { PRIORITIES, PRIORITY_LABELS, STATUSES, STATUS_LABELS } from '../constants';
import { basename } from '../utils';

export default function Filters(props) {
  const repoId = createUniqueId();
  const statusId = createUniqueId();
  const priorityId = createUniqueId();
  const markingId = createUniqueId();
  const markingListId = createUniqueId();

  return (
    <section class="panel filters-panel" aria-labelledby="filters-title">
      <div class="panel-inner">
        <div class="panel-header">
          <h2 id="filters-title" class="panel-title">
            Filters
          </h2>
        </div>
        <div class="filters">
          <div class="field">
            <label class="field-label" for={repoId}>
              Repository
            </label>
            <select
              id={repoId}
              class="select"
              value={props.filters.repo}
              onChange={(event) => props.onChange('repo', event.currentTarget.value)}
            >
              <option value="">All repositories</option>
              <For each={props.repos}>
                {(repo) => (
                  <option value={String(repo.id)} title={repo.path}>
                    {basename(repo.path) || repo.path}
                  </option>
                )}
              </For>
            </select>
          </div>

          <div class="field">
            <label class="field-label" for={statusId}>
              Status
            </label>
            <select
              id={statusId}
              class="select"
              value={props.filters.status}
              onChange={(event) => props.onChange('status', event.currentTarget.value)}
            >
              <option value="">All statuses</option>
              <For each={STATUSES}>
                {(status) => <option value={status}>{STATUS_LABELS[status]}</option>}
              </For>
            </select>
          </div>

          <div class="field">
            <label class="field-label" for={priorityId}>
              Priority
            </label>
            <select
              id={priorityId}
              class="select"
              value={props.filters.priority}
              onChange={(event) => props.onChange('priority', event.currentTarget.value)}
            >
              <option value="">All priorities</option>
              <For each={PRIORITIES}>
                {(priority) => <option value={priority}>{PRIORITY_LABELS[priority]}</option>}
              </For>
            </select>
          </div>

          <div class="field">
            <label class="field-label" for={markingId}>
              Marking
            </label>
            <input
              id={markingId}
              class="input"
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

          <button
            class="btn btn-ghost btn-sm filter-reset"
            type="button"
            disabled={!props.hasFilters}
            onClick={props.onReset}
          >
            Clear filters
          </button>
        </div>
      </div>
    </section>
  );
}
