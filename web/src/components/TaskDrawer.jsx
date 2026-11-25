import {
  For,
  Show,
  createEffect,
  createMemo,
  createSignal,
  createStore,
  createUniqueId,
  snapshot,
} from 'solid-js';
import { command } from '../api';
import { PRIORITIES, PRIORITY_LABELS, STATUSES, STATUS_LABELS } from '../constants';
import { clearDraft, draftKey, loadDraft, saveDraft } from '../drafts';
import {
  asArray,
  asText,
  errorText,
  formatDateTime,
  formatFullDateTime,
  humanize,
  safeHttpUrl,
  timeAttribute,
} from '../utils';
import { PriorityBadge, StatusBadge } from './Badges';
import ActivityFeed from './ActivityFeed';
import Modal from './Modal';

function formFromTask(task) {
  return {
    title: asText(task?.title),
    description: asText(task?.description),
    status: asText(task?.status) || 'todo',
    priority: asText(task?.priority) || 'medium',
    repo_id: task?.repo_id == null ? '' : String(task.repo_id),
  };
}

function storedForm(stored, fallback) {
  const next = { ...fallback };
  if (!stored || typeof stored !== 'object') return next;
  if (typeof stored.title === 'string') next.title = stored.title;
  if (typeof stored.description === 'string') next.description = stored.description;
  if (typeof stored.status === 'string' && stored.status) next.status = stored.status;
  if (typeof stored.priority === 'string' && stored.priority) next.priority = stored.priority;
  if (stored.repo_id != null) next.repo_id = String(stored.repo_id);
  return next;
}

export default function TaskDrawer(props) {
  const dialogTitleId = createUniqueId();
  const titleId = createUniqueId();
  const descriptionId = createUniqueId();
  const statusId = createUniqueId();
  const priorityId = createUniqueId();
  const repoId = createUniqueId();
  const formErrorId = createUniqueId();
  const markingInputId = createUniqueId();
  const markingErrorId = createUniqueId();
  const blockerSelectId = createUniqueId();
  const blockerErrorId = createUniqueId();
  const commentId = createUniqueId();
  const commentErrorId = createUniqueId();
  let titleInput;
  let loadedTaskId = null;
  let activeTaskKey = null;
  let activeCommentKey = null;

  const [form, setForm] = createStore(formFromTask(null));
  const [draftActive, setDraftActive] = createSignal(false);
  const [saveBusy, setSaveBusy] = createSignal(false);
  const [formError, setFormError] = createSignal(null);
  const [markingInput, setMarkingInput] = createSignal('');
  const [markingBusy, setMarkingBusy] = createSignal(null);
  const [markingError, setMarkingError] = createSignal(null);
  const [blockerChoice, setBlockerChoice] = createSignal('');
  const [blockerBusy, setBlockerBusy] = createSignal(null);
  const [blockerError, setBlockerError] = createSignal(null);
  const [reviewBusy, setReviewBusy] = createSignal(null);
  const [reviewError, setReviewError] = createSignal(null);
  const [comment, setComment] = createSignal('');
  const [commentBusy, setCommentBusy] = createSignal(false);
  const [commentError, setCommentError] = createSignal(null);

  const task = () => props.detail?.task;
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

  const dirty = createMemo(() => {
    const current = task();
    if (!current) return false;
    return (
      form.title !== asText(current.title) ||
      form.description !== asText(current.description) ||
      form.status !== asText(current.status) ||
      form.priority !== asText(current.priority) ||
      String(form.repo_id) !== String(current.repo_id ?? '')
    );
  });

  function loadTaskState(current) {
    loadedTaskId = String(current.id);
    activeTaskKey = draftKey('task', current.id);
    activeCommentKey = draftKey('comment', current.id);
    const base = formFromTask(current);
    const stored = loadDraft(activeTaskKey);
    const hasDraft = Boolean(stored && typeof stored === 'object');
    setForm((draft) => {
      Object.assign(draft, storedForm(stored, base));
    });
    setDraftActive(hasDraft);
    const storedComment = loadDraft(activeCommentKey);
    setComment(typeof storedComment === 'string' ? storedComment : '');
    setFormError(null);
    setMarkingError(null);
    setBlockerError(null);
    setReviewError(null);
    setCommentError(null);
    setMarkingInput('');
    setBlockerChoice('');
    setSaveBusy(false);
    setMarkingBusy(null);
    setBlockerBusy(null);
    setReviewBusy(null);
    setCommentBusy(false);
  }

  createEffect(
    () => {
      if (!props.open) return;
      const current = task();
      if (!current) return;
      return { current, draftActive: draftActive() };
    },
    (value) => {
      if (!value) return;
      const { current } = value;
      if (loadedTaskId !== String(current.id)) {
        loadTaskState(current);
      } else if (!value.draftActive) {
        setForm((draft) => {
          Object.assign(draft, formFromTask(current));
        });
      }
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

  function updateField(field, value) {
    setForm((draft) => {
      draft[field] = value;
      if (activeTaskKey) saveDraft(activeTaskKey, snapshot(draft));
    });
    setDraftActive(true);
    setFormError(null);
  }

  function discardDraft() {
    if (activeTaskKey) clearDraft(activeTaskKey);
    setDraftActive(false);
    setForm((draft) => {
      Object.assign(draft, formFromTask(task()));
    });
    setFormError(null);
  }

  async function saveTask(event) {
    event.preventDefault();
    if (saveBusy()) return;
    const current = task();
    if (!current) return;
    const title = form.title.trim();
    if (!title) {
      setFormError('Enter a task title.');
      titleInput?.focus();
      return;
    }
    const repo = props.repos.find((item) => String(item.id) === String(form.repo_id));
    if (!repo) {
      setFormError('Choose a repository.');
      return;
    }
    const taskId = current.id;
    const taskKey = activeTaskKey;
    setSaveBusy(true);
    setFormError(null);
    try {
      await command('task.update', {
        id: taskId,
        title,
        description: form.description,
        repo_id: repo.id,
        status: form.status,
        priority: form.priority,
      });
      if (taskKey) clearDraft(taskKey);
      await props.onRefresh();
      if (activeTaskKey === taskKey) {
        setDraftActive(false);
        props.notify('Task updated');
      }
    } catch (requestError) {
      if (activeTaskKey === taskKey) {
        setFormError(errorText(requestError));
      }
    } finally {
      if (activeTaskKey === taskKey) setSaveBusy(false);
    }
  }

  async function runReview(approve) {
    if (reviewBusy()) return;
    const current = task();
    if (!current) return;
    const taskId = current.id;
    const taskKey = activeTaskKey;
    setReviewBusy(approve ? 'approve' : 'changes');
    setReviewError(null);
    try {
      await command('task.review', { id: taskId, approve });
      await props.onRefresh();
      if (activeTaskKey === taskKey) {
        props.notify(approve ? 'Task approved' : 'Changes requested');
      }
    } catch (requestError) {
      if (activeTaskKey === taskKey) {
        setReviewError(errorText(requestError));
      }
    } finally {
      if (activeTaskKey === taskKey) setReviewBusy(null);
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
    const taskId = current.id;
    const taskKey = activeTaskKey;
    setMarkingBusy('add');
    setMarkingError(null);
    try {
      await command('task.mark', { id: taskId, marking, present: true });
      await props.onRefresh();
      if (activeTaskKey === taskKey) {
        setMarkingInput('');
        props.notify('Marking added');
      }
    } catch (requestError) {
      if (activeTaskKey === taskKey) {
        setMarkingError(errorText(requestError));
      }
    } finally {
      if (activeTaskKey === taskKey) setMarkingBusy(null);
    }
  }

  async function removeMarking(marking) {
    if (markingBusy()) return;
    const current = task();
    if (!current) return;
    const taskId = current.id;
    const taskKey = activeTaskKey;
    setMarkingBusy(marking);
    setMarkingError(null);
    try {
      await command('task.mark', { id: taskId, marking, present: false });
      await props.onRefresh();
      if (activeTaskKey === taskKey) props.notify('Marking removed');
    } catch (requestError) {
      if (activeTaskKey === taskKey) {
        setMarkingError(errorText(requestError));
      }
    } finally {
      if (activeTaskKey === taskKey) setMarkingBusy(null);
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
    const taskId = current.id;
    const taskKey = activeTaskKey;
    setBlockerBusy('add');
    setBlockerError(null);
    try {
      await command('task.depend', { id: taskId, blocker_id: blocker.id, present: true });
      await props.onRefresh();
      if (activeTaskKey === taskKey) {
        setBlockerChoice('');
        props.notify('Blocker added');
      }
    } catch (requestError) {
      if (activeTaskKey === taskKey) {
        setBlockerError(errorText(requestError));
      }
    } finally {
      if (activeTaskKey === taskKey) setBlockerBusy(null);
    }
  }

  async function removeBlocker(blockerId) {
    if (blockerBusy()) return;
    const current = task();
    if (!current) return;
    const taskId = current.id;
    const taskKey = activeTaskKey;
    setBlockerBusy(String(blockerId));
    setBlockerError(null);
    try {
      await command('task.depend', {
        id: taskId,
        blocker_id: blockerId,
        present: false,
      });
      await props.onRefresh();
      if (activeTaskKey === taskKey) props.notify('Blocker removed');
    } catch (requestError) {
      if (activeTaskKey === taskKey) {
        setBlockerError(errorText(requestError));
      }
    } finally {
      if (activeTaskKey === taskKey) setBlockerBusy(null);
    }
  }

  function updateComment(value) {
    setComment(value);
    setCommentError(null);
    if (!activeCommentKey) return;
    if (value) {
      saveDraft(activeCommentKey, value);
    } else {
      clearDraft(activeCommentKey);
    }
  }

  async function submitComment(event) {
    event.preventDefault();
    if (commentBusy()) return;
    const current = task();
    if (!current) return;
    const body = comment().trim();
    if (!body) {
      setCommentError('Enter a comment.');
      return;
    }
    const taskId = current.id;
    const taskKey = activeTaskKey;
    const commentKey = activeCommentKey;
    setCommentBusy(true);
    setCommentError(null);
    try {
      await command('task.comment', { id: taskId, body });
      if (commentKey) clearDraft(commentKey);
      await props.onRefresh();
      if (activeTaskKey === taskKey) {
        setComment('');
        props.notify('Comment added');
      }
    } catch (requestError) {
      if (activeTaskKey === taskKey) {
        setCommentError(errorText(requestError));
      }
    } finally {
      if (activeTaskKey === taskKey) setCommentBusy(false);
    }
  }

  return (
    <Modal
      open={props.open}
      class="drawer-dialog"
      labelledby={dialogTitleId}
      onClose={props.onClose}
    >
      <header class="drawer-header">
        <div class="drawer-head-main">
          <div class="drawer-kicker">Task #{props.taskId}</div>
          <h2 id={dialogTitleId} class="drawer-title">
            {task()?.title || 'Task details'}
          </h2>
          <Show when={task()}>
            {(current) => (
              <div class="drawer-head-meta">
                <StatusBadge value={current().status} />
                <PriorityBadge value={current().priority} />
                <span class="drawer-updated">
                  Updated{' '}
                  <time
                    datetime={timeAttribute(current().updated_at)}
                    title={formatFullDateTime(current().updated_at)}
                  >
                    {formatDateTime(current().updated_at)}
                  </time>
                </span>
              </div>
            )}
          </Show>
        </div>
        <button
          class="icon-button"
          type="button"
          aria-label="Close task details"
          onClick={props.onClose}
        >
          ×
        </button>
      </header>

      <div class="drawer-body">
        <Show when={props.error}>
          <div class="banner warning drawer-banner" role="alert">
            <span>
              {props.detail ? 'Live updates paused. ' : 'Could not load this task. '}
              {props.error}
            </span>
            <button class="btn btn-ghost btn-sm" type="button" onClick={props.onRefresh}>
              Retry
            </button>
          </div>
        </Show>

        <Show when={props.loading && !props.detail}>
          <div class="loading-state" role="status">
            Loading task…
          </div>
        </Show>

        <Show when={!props.loading && !props.detail && !props.error}>
          <div class="empty-state">
            <h3>Task unavailable</h3>
            <p>This task could not be loaded.</p>
            <button class="btn btn-secondary" type="button" onClick={props.onRefresh}>
              Retry
            </button>
          </div>
        </Show>

        <Show when={props.detail}>
          {(detail) => {
            const current = () => detail().task;
            return (
              <>
                <section class="drawer-section" aria-labelledby="drawer-details-heading">
                  <h3 id="drawer-details-heading">Details</h3>
                  <form class="stack drawer-form" onSubmit={saveTask}>
                    <div class="field">
                      <label class="field-label" for={titleId}>
                        Title
                      </label>
                      <input
                        ref={titleInput}
                        id={titleId}
                        class="input"
                        type="text"
                        value={form.title}
                        onInput={(event) => updateField('title', event.currentTarget.value)}
                        disabled={saveBusy()}
                        aria-required="true"
                      />
                    </div>

                    <div class="field">
                      <label class="field-label" for={descriptionId}>
                        Description (Markdown)
                      </label>
                      <textarea
                        id={descriptionId}
                        class="textarea"
                        rows="6"
                        value={form.description}
                        onInput={(event) => updateField('description', event.currentTarget.value)}
                        disabled={saveBusy()}
                      />
                      <p class="field-hint">Markdown is supported.</p>
                    </div>

                    <div class="field-row">
                      <div class="field">
                        <label class="field-label" for={statusId}>
                          Status
                        </label>
                        <select
                          id={statusId}
                          class="select"
                          value={form.status}
                          onChange={(event) => updateField('status', event.currentTarget.value)}
                          disabled={saveBusy()}
                        >
                          <For each={STATUSES}>
                            {(status) => (
                              <option value={status}>{STATUS_LABELS[status]}</option>
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
                          class="select"
                          value={form.priority}
                          onChange={(event) => updateField('priority', event.currentTarget.value)}
                          disabled={saveBusy()}
                        >
                          <For each={PRIORITIES}>
                            {(priority) => (
                              <option value={priority}>{PRIORITY_LABELS[priority]}</option>
                            )}
                          </For>
                        </select>
                      </div>
                    </div>

                    <div class="field">
                      <label class="field-label" for={repoId}>
                        Repository
                      </label>
                      <select
                        id={repoId}
                        class="select"
                        value={form.repo_id}
                        onChange={(event) => updateField('repo_id', event.currentTarget.value)}
                        disabled={saveBusy()}
                        aria-required="true"
                      >
                        <For each={props.repos}>
                          {(repo) => <option value={String(repo.id)}>{repo.path}</option>}
                        </For>
                      </select>
                    </div>

                    {formError() && (
                      <p id={formErrorId} class="field-error" role="alert">
                        {formError()}
                      </p>
                    )}

                    <div class="drawer-form-actions">
                      <span class="draft-note" aria-live="polite">
                        {dirty() ? 'Unsaved changes' : ''}
                      </span>
                      <div class="action-group">
                        <button
                          class="btn btn-secondary"
                          type="button"
                          onClick={discardDraft}
                          disabled={(!draftActive() && !dirty()) || saveBusy()}
                        >
                          Discard
                        </button>
                        <button
                          class="btn btn-primary"
                          type="submit"
                          disabled={!dirty() || saveBusy()}
                        >
                          {saveBusy() ? 'Saving…' : 'Save changes'}
                        </button>
                      </div>
                    </div>
                  </form>
                </section>

                <Show when={current().status === 'in-review'}>
                  <section class="drawer-section review-section" aria-labelledby="drawer-review-heading">
                    <h3 id="drawer-review-heading">Review</h3>
                    <p class="review-copy">This task is waiting for a review decision.</p>
                    <div class="review-actions">
                      <button
                        class="btn btn-primary"
                        type="button"
                        onClick={() => runReview(true)}
                        disabled={reviewBusy() !== null}
                      >
                        {reviewBusy() === 'approve' ? 'Approving…' : 'Approve'}
                      </button>
                      <button
                        class="btn btn-secondary"
                        type="button"
                        onClick={() => runReview(false)}
                        disabled={reviewBusy() !== null}
                      >
                        {reviewBusy() === 'changes' ? 'Requesting…' : 'Request changes'}
                      </button>
                    </div>
                    <Show when={reviewError()}>
                      <p class="field-error" role="alert">
                        {reviewError()}
                      </p>
                    </Show>
                  </section>
                </Show>

                <section class="drawer-section" aria-labelledby="drawer-markings-heading">
                  <h3 id="drawer-markings-heading">Markings</h3>
                  <Show
                    when={markings().length > 0}
                    fallback={<p class="muted-small">No markings.</p>}
                  >
                    <div class="chip-list">
                      <For each={markings()}>
                        {(marking) => (
                          <span class="chip">
                            {marking}
                            <button
                              class="chip-remove"
                              type="button"
                              aria-label={`Remove marking ${marking}`}
                              onClick={() => removeMarking(marking)}
                              disabled={markingBusy() !== null}
                            >
                              ×
                            </button>
                          </span>
                        )}
                      </For>
                    </div>
                  </Show>

                  <form class="inline-form marking-form" onSubmit={addMarking}>
                    <div class="field">
                      <label class="field-label" for={markingInputId}>
                        Add marking
                      </label>
                      <input
                        id={markingInputId}
                        class="input"
                        type="text"
                        value={markingInput()}
                        onInput={(event) => {
                          setMarkingInput(event.currentTarget.value);
                          setMarkingError(null);
                        }}
                        disabled={markingBusy() !== null}
                      />
                    </div>
                    <button
                      class="btn btn-secondary"
                      type="submit"
                      disabled={markingBusy() !== null || !markingInput().trim()}
                    >
                      {markingBusy() === 'add' ? 'Adding…' : 'Add'}
                    </button>
                  </form>
                  <Show when={markingError()}>
                    <p id={markingErrorId} class="field-error" role="alert">
                      {markingError()}
                    </p>
                  </Show>
                </section>

                <section class="drawer-section" aria-labelledby="drawer-blockers-heading">
                  <h3 id="drawer-blockers-heading">Blockers</h3>
                  <Show
                    when={blockerIds().length > 0}
                    fallback={<p class="muted-small">No blockers.</p>}
                  >
                    <ul class="blocker-list">
                      <For each={blockerIds()}>
                        {(blockerId) => {
                          const blocker = () => blockerTaskMap().get(String(blockerId));
                          return (
                            <li class="blocker-item">
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
                    <form class="blocker-add" onSubmit={addBlocker}>
                      <div class="field">
                        <label class="field-label" for={blockerSelectId}>
                          Add blocker
                        </label>
                        <select
                          id={blockerSelectId}
                          class="select"
                          value={blockerChoice()}
                          onChange={(event) => {
                            setBlockerChoice(event.currentTarget.value);
                            setBlockerError(null);
                          }}
                          disabled={blockerBusy() !== null}
                        >
                          <option value="">Select a task</option>
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
                        class="btn btn-secondary"
                        type="submit"
                        disabled={blockerBusy() !== null || !blockerChoice()}
                      >
                        {blockerBusy() === 'add' ? 'Adding…' : 'Add'}
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
                    <p class="muted-small">No other tasks available.</p>
                  </Show>
                  <Show when={blockerError()}>
                    <p id={blockerErrorId} class="field-error" role="alert">
                      {blockerError()}
                    </p>
                  </Show>
                </section>

                <section class="drawer-section" aria-labelledby="drawer-sessions-heading">
                  <h3 id="drawer-sessions-heading">OpenCode sessions</h3>
                  <Show
                    when={sessions().length > 0}
                    fallback={<p class="muted-small">No sessions recorded.</p>}
                  >
                    <ul class="session-list">
                      <For each={sessions()}>
                        {(session) => {
                          const href = () => safeHttpUrl(session.url);
                          return (
                            <li class="session-item">
                              <div class="session-main">
                                <span class="session-id">
                                  {session.id == null ? 'Session' : String(session.id)}
                                </span>
                                <div class="session-meta">
                                  <span class="badge status-neutral">
                                    {humanize(session.state || 'unknown')}
                                  </span>
                                  <Show when={session.attempt_id != null}>
                                    <span>Attempt {String(session.attempt_id)}</span>
                                  </Show>
                                  <time
                                    datetime={timeAttribute(session.created_at)}
                                    title={formatFullDateTime(session.created_at)}
                                  >
                                    {formatDateTime(session.created_at)}
                                  </time>
                                </div>
                              </div>
                              <Show
                                when={href()}
                                fallback={<span class="session-link muted-small">No link</span>}
                              >
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
                            </li>
                          );
                        }}
                      </For>
                    </ul>
                  </Show>
                </section>

                <section class="drawer-section" aria-labelledby="drawer-activity-heading">
                  <h3 id="drawer-activity-heading">Activity</h3>
                  <ActivityFeed activity={detail().activity} />
                  <form class="stack comment-form" onSubmit={submitComment}>
                    <div class="field">
                      <label class="field-label" for={commentId}>
                        Add a comment
                      </label>
                      <textarea
                        id={commentId}
                        class="textarea"
                        rows="3"
                        value={comment()}
                        onInput={(event) => updateComment(event.currentTarget.value)}
                        disabled={commentBusy()}
                      />
                      <p class="field-hint">Markdown is supported.</p>
                    </div>
                    <Show when={commentError()}>
                      <p id={commentErrorId} class="field-error" role="alert">
                        {commentError()}
                      </p>
                    </Show>
                    <div class="form-actions">
                      <button
                        class="btn btn-secondary"
                        type="submit"
                        disabled={commentBusy() || !comment().trim()}
                      >
                        {commentBusy() ? 'Adding…' : 'Add comment'}
                      </button>
                    </div>
                  </form>
                </section>
              </>
            );
          }}
        </Show>
      </div>
    </Modal>
  );
}
