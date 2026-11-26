import { For, Show, createEffect, createMemo, createSignal, createUniqueId } from 'solid-js';
import { command } from '../api';
import { PRIORITIES, PRIORITY_LABELS, STATUSES, STATUS_LABELS } from '../constants';
import {
  asArray,
  asText,
  basename,
  errorText,
  formatDateTime,
  formatFullDateTime,
  humanize,
  repoLinkedTo,
  safeHttpUrl,
  timeAttribute,
} from '../utils';

function SmallCloseIcon() {
  return (
    <svg width="12" height="12" viewBox="0 0 12 12" fill="none" aria-hidden="true">
      <path
        d="M3 3L9 9M9 3L3 9"
        stroke="currentColor"
        stroke-width="1.5"
        stroke-linecap="round"
      />
    </svg>
  );
}

function sessionTone(state) {
  const value = asText(state).toLowerCase();
  if (value === 'success' || value === 'succeeded' || value === 'completed' || value === 'done') {
    return 'done';
  }
  if (value === 'running' || value === 'active' || value === 'in-progress') {
    return 'in-progress';
  }
  return 'neutral';
}

export default function TaskInspectorPanel(props) {
  const statusId = createUniqueId();
  const priorityId = createUniqueId();
  const repoId = createUniqueId();
  const markingInputId = createUniqueId();
  const blockerSelectId = createUniqueId();

  const [propBusy, setPropBusy] = createSignal(false);
  const [propError, setPropError] = createSignal(null);
  const [markingInput, setMarkingInput] = createSignal('');
  const [markingBusy, setMarkingBusy] = createSignal(null);
  const [markingError, setMarkingError] = createSignal(null);
  const [blockerChoice, setBlockerChoice] = createSignal('');
  const [blockerBusy, setBlockerBusy] = createSignal(null);
  const [blockerError, setBlockerError] = createSignal(null);

  const task = () => props.detail?.task;

  const scopedRepos = createMemo(() => {
    const current = task();
    if (!current) return asArray(props.repos);
    return asArray(props.repos).filter((repo) => repoLinkedTo(repo, current.project_id));
  });

  const markings = () => {
    const values = asArray(task()?.markings).map((marking) => asText(marking).trim());
    return [...new Set(values.filter(Boolean))];
  };

  const blockerIds = () => asArray(props.detail?.blockers);
  const blockerIdSet = createMemo(() => new Set(blockerIds().map((id) => String(id))));
  const blockerSource = () => props.blockerSource || { tasks: [], loading: false, error: null };
  const blockerTasks = () => asArray(blockerSource().tasks);
  const blockerTaskMap = createMemo(
    () => new Map(blockerTasks().map((item) => [String(item.id), item])),
  );
  const availableBlockers = createMemo(() =>
    blockerTasks()
      .filter(
        (item) =>
          String(item.id) !== String(props.taskId) && !blockerIdSet().has(String(item.id)),
      )
      .sort((left, right) =>
        asText(left.title).localeCompare(asText(right.title), undefined, { sensitivity: 'base' }),
      ),
  );
  const sessions = createMemo(() => asArray(props.detail?.sessions));

  createEffect(
    () => props.taskId,
    () => {
      setPropError(null);
      setMarkingInput('');
      setMarkingError(null);
      setBlockerChoice('');
      setBlockerError(null);
    },
  );

  createEffect(
    () => ({ available: availableBlockers(), choice: blockerChoice() }),
    ({ available, choice }) => {
      if (choice && !available.some((item) => String(item.id) === String(choice))) {
        setBlockerChoice('');
      }
    },
  );

  async function patchProperty(patch, message) {
    const current = task();
    if (!current || propBusy()) return;
    setPropBusy(true);
    setPropError(null);
    try {
      await command('task.update', { id: current.id, ...patch });
      await props.onRefresh();
      props.notify(message);
    } catch (requestError) {
      setPropError(errorText(requestError));
    } finally {
      setPropBusy(false);
    }
  }

  async function addMarking(event) {
    event.preventDefault();
    if (markingBusy()) return;
    const current = task();
    if (!current) return;
    const marking = markingInput().trim();
    if (!marking) {
      setMarkingError('Enter a marking.');
      return;
    }
    if (markings().includes(marking)) {
      setMarkingError('That marking is already present.');
      return;
    }
    setMarkingBusy('add');
    setMarkingError(null);
    try {
      await command('task.mark', { id: current.id, marking, present: true });
      await props.onRefresh();
      setMarkingInput('');
      props.notify('Marking added');
    } catch (requestError) {
      setMarkingError(errorText(requestError));
    } finally {
      setMarkingBusy(null);
    }
  }

  async function removeMarking(marking) {
    if (markingBusy()) return;
    const current = task();
    if (!current) return;
    setMarkingBusy(marking);
    setMarkingError(null);
    try {
      await command('task.mark', { id: current.id, marking, present: false });
      await props.onRefresh();
      props.notify('Marking removed');
    } catch (requestError) {
      setMarkingError(errorText(requestError));
    } finally {
      setMarkingBusy(null);
    }
  }

  async function addBlocker(event) {
    event.preventDefault();
    if (blockerBusy()) return;
    const current = task();
    if (!current) return;
    const blocker = availableBlockers().find(
      (item) => String(item.id) === String(blockerChoice()),
    );
    if (!blocker) {
      setBlockerError('Choose a task to add as a blocker.');
      return;
    }
    setBlockerBusy('add');
    setBlockerError(null);
    try {
      await command('task.depend', { id: current.id, blocker_id: blocker.id, present: true });
      await props.onRefresh();
      setBlockerChoice('');
      props.notify('Blocker added');
    } catch (requestError) {
      setBlockerError(errorText(requestError));
    } finally {
      setBlockerBusy(null);
    }
  }

  async function removeBlocker(blockerId) {
    if (blockerBusy()) return;
    const current = task();
    if (!current) return;
    setBlockerBusy(String(blockerId));
    setBlockerError(null);
    try {
      await command('task.depend', {
        id: current.id,
        blocker_id: blockerId,
        present: false,
      });
      await props.onRefresh();
      props.notify('Blocker removed');
    } catch (requestError) {
      setBlockerError(errorText(requestError));
    } finally {
      setBlockerBusy(null);
    }
  }

  return (
    <aside
      class="inspector-panel"
      style={{ width: `${props.width}px` }}
      aria-label="Task inspector"
    >
      <div class="mobile-back-bar">
        <button class="btn btn-ghost btn-sm" type="button" onClick={props.onCloseMobileInspector}>
          ← Back to task
        </button>
      </div>

      <div class="inspector-scroll">
        <Show
          when={task()}
          fallback={<p class="muted-small">Select a task to view its metadata and sessions.</p>}
        >
          {(current) => (
            <>
              <section class="rail-section" aria-labelledby="rail-props-heading">
                <div class="rail-head">
                  <h3 id="rail-props-heading">Properties</h3>
                  <Show when={propBusy()}>
                    <span class="rail-saving">Saving…</span>
                  </Show>
                </div>

                <div class="stack">
                  <div class="field">
                    <label class="field-label" for={statusId}>
                      Status
                    </label>
                    <select
                      id={statusId}
                      class="select select-sm"
                      value={current().status}
                      onChange={(event) =>
                        patchProperty({ status: event.currentTarget.value }, 'Status updated')
                      }
                      disabled={propBusy()}
                    >
                      <For each={STATUSES}>
                        {(status) => (
                          <option
                            value={status}
                            disabled={status === 'todo' && current().status === 'in-review'}
                          >
                            {STATUS_LABELS[status]}
                          </option>
                        )}
                      </For>
                    </select>
                  </div>

                  <div class="field">
                    <label class="field-label" for={priorityId}>
                      Priority
                    </label>
                    <select
                      id={priorityId}
                      class="select select-sm"
                      value={current().priority}
                      onChange={(event) =>
                        patchProperty({ priority: event.currentTarget.value }, 'Priority updated')
                      }
                      disabled={propBusy()}
                    >
                      <For each={PRIORITIES}>
                        {(priority) => (
                          <option value={priority}>{PRIORITY_LABELS[priority]}</option>
                        )}
                      </For>
                    </select>
                  </div>

                  <div class="field">
                    <label class="field-label" for={repoId}>
                      Repository
                    </label>
                    <select
                      id={repoId}
                      class="select select-sm"
                      value={current().repo_id == null ? '' : String(current().repo_id)}
                      onChange={(event) =>
                        patchProperty(
                          { repo_id: Number(event.currentTarget.value) },
                          'Repository updated',
                        )
                      }
                      disabled={propBusy()}
                    >
                      <For each={scopedRepos()}>
                        {(repo) => (
                          <option value={String(repo.id)} title={repo.path}>
                            {basename(repo.path) || repo.path}
                          </option>
                        )}
                      </For>
                    </select>
                  </div>

                  <Show when={propError()}>
                    <p class="field-error" role="alert">
                      {propError()}
                    </p>
                  </Show>
                </div>
              </section>

              <section class="rail-section" aria-labelledby="rail-sessions-heading">
                <div class="rail-head">
                  <h3 id="rail-sessions-heading">OpenCode sessions</h3>
                  <span class="muted-small">{sessions().length}</span>
                </div>
                <Show
                  when={sessions().length > 0}
                  fallback={<p class="muted-small">No sessions recorded yet.</p>}
                >
                  <ul class="flat-list">
                    <For each={sessions()}>
                      {(session) => {
                        const href = () => safeHttpUrl(session.url);
                        return (
                          <li class="flat-item">
                            <div class="session-main">
                              <span class="session-id">
                                {session.id == null ? 'Session' : String(session.id)}
                              </span>
                              <div class="session-meta">
                                <span
                                  class={`semantic-tag semantic-status-${sessionTone(session.state)}`}
                                >
                                  <span class="semantic-dot" aria-hidden="true" />
                                  {humanize(session.state || 'unknown')}
                                </span>
                                <time
                                  datetime={timeAttribute(session.created_at)}
                                  title={formatFullDateTime(session.created_at)}
                                >
                                  {formatDateTime(session.created_at)}
                                </time>
                              </div>
                              <Show when={href()}>
                                {(url) => (
                                  <a
                                    class="session-link"
                                    href={url()}
                                    target="_blank"
                                    rel="noopener noreferrer"
                                  >
                                    Open session ↗
                                  </a>
                                )}
                              </Show>
                            </div>
                          </li>
                        );
                      }}
                    </For>
                  </ul>
                </Show>
              </section>

              <section class="rail-section" aria-labelledby="rail-markings-heading">
                <h3 id="rail-markings-heading">Markings</h3>
                <Show
                  when={markings().length > 0}
                  fallback={<p class="muted-small" style="margin-bottom: 10px">No markings.</p>}
                >
                  <div class="token-list">
                    <For each={markings()}>
                      {(marking) => (
                        <span class="token">
                          <span class="token-name">{marking}</span>
                          <button
                            class="token-remove"
                            type="button"
                            aria-label={`Remove marking ${marking}`}
                            onClick={() => removeMarking(marking)}
                            disabled={markingBusy() !== null}
                          >
                            <SmallCloseIcon />
                          </button>
                        </span>
                      )}
                    </For>
                  </div>
                </Show>

                <form class="inline-add-form" onSubmit={addMarking}>
                  <div class="field">
                    <label class="sr-only" for={markingInputId}>
                      Add marking
                    </label>
                    <input
                      id={markingInputId}
                      class="input input-sm"
                      type="text"
                      value={markingInput()}
                      onInput={(event) => {
                        setMarkingInput(event.currentTarget.value);
                        setMarkingError(null);
                      }}
                      placeholder="Add marking…"
                      disabled={markingBusy() !== null}
                    />
                  </div>
                  <button
                    class="btn btn-secondary btn-sm"
                    type="submit"
                    disabled={markingBusy() !== null || !markingInput().trim()}
                  >
                    {markingBusy() === 'add' ? '…' : 'Add'}
                  </button>
                </form>
                <Show when={markingError()}>
                  <p class="field-error" role="alert">
                    {markingError()}
                  </p>
                </Show>
              </section>

              <section class="rail-section" aria-labelledby="rail-blockers-heading">
                <h3 id="rail-blockers-heading">Blockers</h3>
                <Show
                  when={blockerIds().length > 0}
                  fallback={<p class="muted-small" style="margin-bottom: 10px">No blockers.</p>}
                >
                  <ul class="flat-list">
                    <For each={blockerIds()}>
                      {(blockerId) => {
                        const blocker = () => blockerTaskMap().get(String(blockerId));
                        return (
                          <li class="flat-item">
                            <div class="blocker-main">
                              <span class="blocker-id">#{blockerId}</span>
                              <span class="blocker-title">
                                {blocker()?.title || 'Unknown task'}
                              </span>
                            </div>
                            <button
                              class="btn btn-ghost btn-sm"
                              type="button"
                              onClick={() => removeBlocker(blockerId)}
                              disabled={blockerBusy() !== null}
                              aria-label={`Remove blocker ${blocker()?.title || `#${blockerId}`}`}
                            >
                              Remove
                            </button>
                          </li>
                        );
                      }}
                    </For>
                  </ul>
                </Show>

                <Show when={blockerSource().loading}>
                  <p class="muted-small">Loading project tasks…</p>
                </Show>
                <Show when={blockerSource().error}>
                  <div class="inline-alert" role="alert">
                    <span>{blockerSource().error}</span>
                  </div>
                </Show>
                <Show
                  when={
                    !blockerSource().loading &&
                    !blockerSource().error &&
                    availableBlockers().length > 0
                  }
                >
                  <form class="inline-add-form" onSubmit={addBlocker}>
                    <div class="field">
                      <label class="sr-only" for={blockerSelectId}>
                        Add blocker
                      </label>
                      <select
                        id={blockerSelectId}
                        class="select select-sm"
                        value={blockerChoice()}
                        onChange={(event) => {
                          setBlockerChoice(event.currentTarget.value);
                          setBlockerError(null);
                        }}
                        disabled={blockerBusy() !== null}
                      >
                        <option value="">Add blocker…</option>
                        <For each={availableBlockers()}>
                          {(item) => (
                            <option value={String(item.id)}>
                              #{item.id} · {item.title || 'Untitled task'}
                            </option>
                          )}
                        </For>
                      </select>
                    </div>
                    <button
                      class="btn btn-secondary btn-sm"
                      type="submit"
                      disabled={blockerBusy() !== null || !blockerChoice()}
                    >
                      {blockerBusy() === 'add' ? '…' : 'Add'}
                    </button>
                  </form>
                </Show>
                <Show
                  when={
                    !blockerSource().loading &&
                    !blockerSource().error &&
                    availableBlockers().length === 0
                  }
                >
                  <p class="muted-small">No other tasks in this project.</p>
                </Show>
                <Show when={blockerError()}>
                  <p class="field-error" role="alert">
                    {blockerError()}
                  </p>
                </Show>
              </section>
            </>
          )}
        </Show>
      </div>
    </aside>
  );
}
