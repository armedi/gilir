import {
  Show,
  createEffect,
  createMemo,
  createSignal,
  createUniqueId,
} from 'solid-js';
import { command } from '../api';
import { clearDraft, draftKey, loadDraft, saveDraft } from '../drafts';
import {
  asArray,
  asText,
  errorText,
  formatDateTime,
  formatFullDateTime,
  timeAttribute,
} from '../utils';
import { PriorityBadge, StatusBadge } from './Badges';
import ActivityFeed from './ActivityFeed';
import Markdown from './Markdown';

export default function TaskDetailPane(props) {
  const titleId = createUniqueId();
  const descriptionId = createUniqueId();
  const titleErrorId = createUniqueId();
  const descErrorId = createUniqueId();
  const commentId = createUniqueId();
  const commentErrorId = createUniqueId();
  let titleInput;
  let descriptionInput;
  let commentInput;
  let loadedTaskId = null;
  let activeTaskKey = null;
  let activeDescKey = null;
  let activeCommentKey = null;

  const [editingTitle, setEditingTitle] = createSignal(false);
  const [titleDraft, setTitleDraft] = createSignal('');
  const [titleBusy, setTitleBusy] = createSignal(false);
  const [titleError, setTitleError] = createSignal(null);

  const [editingDesc, setEditingDesc] = createSignal(false);
  const [descMode, setDescMode] = createSignal('write');
  const [descDraft, setDescDraft] = createSignal('');
  const [descBusy, setDescBusy] = createSignal(false);
  const [descError, setDescError] = createSignal(null);

  const [actionBusy, setActionBusy] = createSignal(null);
  const [comment, setComment] = createSignal('');
  const [commentError, setCommentError] = createSignal(null);

  const task = () => props.detail?.task;
  const inReview = () => task()?.status === 'in-review';
  const hasComment = () => comment().trim().length > 0;
  const descDirty = () => {
    const current = task();
    if (!current) return false;
    return descDraft() !== asText(current.description);
  };

  const projectName = createMemo(() => {
    const current = task();
    if (!current) return '';
    const found = asArray(props.projects).find(
      (project) => String(project.id) === String(current.project_id),
    );
    return found ? found.name : `Project #${current.project_id}`;
  });

  function loadTaskState(current) {
    loadedTaskId = String(current.id);
    activeTaskKey = `task:${current.id}`;
    activeDescKey = draftKey('task-description', current.id);
    activeCommentKey = draftKey('comment', current.id);

    setEditingTitle(false);
    setTitleDraft(asText(current.title));
    setTitleBusy(false);
    setTitleError(null);

    const baseDesc = asText(current.description);
    const storedDesc = loadDraft(activeDescKey);
    const hasDescDraft = typeof storedDesc === 'string' && storedDesc !== baseDesc;
    setDescDraft(hasDescDraft ? storedDesc : baseDesc);
    setEditingDesc(hasDescDraft);
    setDescMode('write');
    setDescBusy(false);
    setDescError(null);

    const storedComment = loadDraft(activeCommentKey);
    setComment(typeof storedComment === 'string' ? storedComment : '');
    setCommentError(null);
    setActionBusy(null);
  }

  createEffect(
    () => ({
      current: task(),
      isEditingTitle: editingTitle(),
      isEditingDesc: editingDesc(),
    }),
    ({ current, isEditingTitle, isEditingDesc }) => {
      if (!current) return;
      if (loadedTaskId !== String(current.id)) {
        loadTaskState(current);
        return;
      }
      if (!isEditingTitle) setTitleDraft(asText(current.title));
      if (!isEditingDesc) setDescDraft(asText(current.description));
    },
  );

  function startTitleEdit() {
    const current = task();
    if (!current || titleBusy()) return;
    setTitleDraft(asText(current.title));
    setTitleError(null);
    setEditingTitle(true);
    requestAnimationFrame(() => {
      titleInput?.focus();
      titleInput?.select();
    });
  }

  function cancelTitleEdit() {
    setTitleDraft(asText(task()?.title));
    setTitleError(null);
    setEditingTitle(false);
  }

  async function saveTitle() {
    if (titleBusy() || !editingTitle()) return;
    const current = task();
    if (!current) return;
    const next = titleDraft().trim();
    if (!next) {
      cancelTitleEdit();
      return;
    }
    if (next === asText(current.title)) {
      setEditingTitle(false);
      setTitleError(null);
      return;
    }
    const taskId = current.id;
    const taskKey = activeTaskKey;
    setTitleBusy(true);
    setTitleError(null);
    try {
      await command('task.update', { id: taskId, title: next });
      await props.onRefresh();
      if (activeTaskKey === taskKey) {
        setEditingTitle(false);
        props.notify('Title updated');
      }
    } catch (requestError) {
      if (activeTaskKey === taskKey) {
        setTitleError(errorText(requestError));
        titleInput?.focus();
      }
    } finally {
      if (activeTaskKey === taskKey) setTitleBusy(false);
    }
  }

  function handleTitleKeyDown(event) {
    if (event.key === 'Enter') {
      event.preventDefault();
      saveTitle();
    } else if (event.key === 'Escape') {
      event.preventDefault();
      cancelTitleEdit();
    }
  }

  function startDescEdit() {
    const current = task();
    if (!current || descBusy()) return;
    setDescDraft(asText(current.description));
    setDescMode('write');
    setDescError(null);
    setEditingDesc(true);
    requestAnimationFrame(() => {
      if (!descriptionInput) return;
      descriptionInput.focus();
      const end = descriptionInput.value.length;
      descriptionInput.setSelectionRange(end, end);
    });
  }

  function handleDescriptionClick(event) {
    if (event.target.closest('a')) return;
    if (window.getSelection && window.getSelection().toString()) return;
    startDescEdit();
  }

  function handleDescriptionKeyDown(event) {
    if (event.key === 'Enter' || event.key === ' ') {
      event.preventDefault();
      startDescEdit();
    }
  }

  function updateDesc(value) {
    setDescDraft(value);
    setDescError(null);
    if (!activeDescKey) return;
    if (value !== asText(task()?.description)) {
      saveDraft(activeDescKey, value);
    } else {
      clearDraft(activeDescKey);
    }
  }

  function cancelDescEdit() {
    if (activeDescKey) clearDraft(activeDescKey);
    setDescDraft(asText(task()?.description));
    setDescError(null);
    setEditingDesc(false);
  }

  async function saveDesc(event) {
    event?.preventDefault();
    if (descBusy()) return;
    const current = task();
    if (!current) return;
    const next = descDraft();
    if (next === asText(current.description)) {
      cancelDescEdit();
      return;
    }
    const taskId = current.id;
    const taskKey = activeTaskKey;
    const descKey = activeDescKey;
    setDescBusy(true);
    setDescError(null);
    try {
      await command('task.update', { id: taskId, description: next });
      if (descKey) clearDraft(descKey);
      await props.onRefresh();
      if (activeTaskKey === taskKey) {
        setEditingDesc(false);
        props.notify('Description updated');
      }
    } catch (requestError) {
      if (activeTaskKey === taskKey) {
        setDescError(errorText(requestError));
      }
    } finally {
      if (activeTaskKey === taskKey) setDescBusy(false);
    }
  }

  function handleDescTextareaKeyDown(event) {
    if ((event.metaKey || event.ctrlKey) && event.key === 'Enter') {
      event.preventDefault();
      saveDesc();
    } else if (event.key === 'Escape') {
      event.preventDefault();
      cancelDescEdit();
    }
  }

  function updateComment(value) {
    setComment(value);
    setCommentError(null);
    if (!activeCommentKey) return;
    if (value) saveDraft(activeCommentKey, value);
    else clearDraft(activeCommentKey);
  }

  async function submitComment(event) {
    event?.preventDefault();
    if (actionBusy() !== null) return;
    const current = task();
    if (!current) return;
    const body = comment().trim();
    if (!body) {
      setCommentError('Enter a comment.');
      commentInput?.focus();
      return;
    }
    const taskId = current.id;
    const taskKey = activeTaskKey;
    const commentKey = activeCommentKey;
    setActionBusy('comment');
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
      if (activeTaskKey === taskKey) setActionBusy(null);
    }
  }

  async function runReview(approve) {
    if (actionBusy() !== null) return;
    const current = task();
    if (!current) return;
    const body = comment().trim();

    if (!approve && !body) {
      setCommentError('Explain what needs to change before requesting changes.');
      commentInput?.focus();
      return;
    }

    const taskId = current.id;
    const taskKey = activeTaskKey;
    const commentKey = activeCommentKey;
    setActionBusy(approve ? 'approve' : 'changes');
    setCommentError(null);
    try {
      // Post the comment before task.review so the dispatched agent immediately
      // receives the review instructions when the task re-enters todo.
      if (body) {
        await command('task.comment', { id: taskId, body });
        if (commentKey) clearDraft(commentKey);
        if (activeTaskKey === taskKey) setComment('');
      }
      await command('task.review', { id: taskId, approve });
      await props.onRefresh();
      if (activeTaskKey === taskKey) {
        props.notify(approve ? 'Task approved' : 'Changes requested');
      }
    } catch (requestError) {
      if (activeTaskKey === taskKey) {
        setCommentError(errorText(requestError));
      }
    } finally {
      if (activeTaskKey === taskKey) setActionBusy(null);
    }
  }

  function handleCommentKeyDown(event) {
    if ((event.metaKey || event.ctrlKey) && event.key === 'Enter') {
      event.preventDefault();
      if (hasComment()) submitComment();
    }
  }

  return (
    <section class="center-panel" aria-label="Task stream">
      <div class="mobile-back-bar">
        <button class="btn btn-ghost btn-sm" type="button" onClick={props.onBack}>
          ← Queue
        </button>
        <button class="btn btn-secondary btn-sm" type="button" onClick={props.onOpenMobileInspector}>
          Inspector
        </button>
      </div>

      <header class="center-header">
        <div class="center-header-inner">
          <div class="center-head-main">
            <Show
              when={editingTitle()}
              fallback={
                <Show
                  when={task()}
                  fallback={<h2 class="center-title">Task #{props.taskId ?? ''}</h2>}
                >
                  {(current) => (
                    <button
                      type="button"
                      class="inline-title-trigger"
                      title="Click to edit title"
                      onClick={startTitleEdit}
                    >
                      <h2 class="center-title">{current().title || 'Untitled task'}</h2>
                    </button>
                  )}
                </Show>
              }
            >
              <div class="inline-title-edit">
                <label class="sr-only" for={titleId}>
                  Task title
                </label>
                <input
                  ref={titleInput}
                  id={titleId}
                  class="inline-title-input"
                  type="text"
                  value={titleDraft()}
                  onInput={(event) => {
                    setTitleDraft(event.currentTarget.value);
                    setTitleError(null);
                  }}
                  onKeyDown={handleTitleKeyDown}
                  onBlur={saveTitle}
                  disabled={titleBusy()}
                  aria-describedby={titleError() ? titleErrorId : undefined}
                />
                <Show when={titleError()}>
                  <p id={titleErrorId} class="field-error" role="alert">
                    {titleError()}
                  </p>
                </Show>
              </div>
            </Show>

            <Show when={task()}>
              {(current) => (
                <div class="center-meta">
                  <span class="center-id">#{props.taskId}</span>
                  <StatusBadge value={current().status} />
                  <PriorityBadge value={current().priority} />
                  <Show when={projectName()}>
                    <span class="center-project">{projectName()}</span>
                  </Show>
                  <span>
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

          <div class="center-head-actions">
            <button
              class="panel-icon-btn"
              type="button"
              aria-label="Close task"
              title="Close task"
              onClick={props.onClose}
            >
              <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                <path
                  d="M4 4L12 12M12 4L4 12"
                  stroke="currentColor"
                  stroke-width="1.6"
                  stroke-linecap="round"
                />
              </svg>
            </button>
          </div>
        </div>
      </header>

      <Show when={props.error}>
        <div class="banner warning" role="alert">
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
        <div class="pane-empty" role="status">
          <p>Loading task…</p>
        </div>
      </Show>

      <Show when={!props.loading && !props.detail && !props.error}>
        <div class="pane-empty">
          <h2>Task unavailable</h2>
          <p>This task could not be loaded from the local queue.</p>
          <button class="btn btn-secondary" type="button" onClick={props.onRefresh}>
            Retry
          </button>
        </div>
      </Show>

      <Show when={props.detail}>
        {(detail) => {
          const current = () => detail().task;
          return (
            <div class="center-scroll">
              <div class="stream-column">
                <section class="description-block" aria-label="Task description">
                  <Show
                    when={editingDesc()}
                    fallback={
                      <div
                        class="description-display"
                        role="button"
                        tabindex="0"
                        aria-label="Edit task description"
                        title="Click to edit description"
                        onClick={handleDescriptionClick}
                        onKeyDown={handleDescriptionKeyDown}
                      >
                        <Show
                          when={current().description.trim()}
                          fallback={<p class="description-empty">Add a description…</p>}
                        >
                          <Markdown source={current().description} />
                        </Show>
                      </div>
                    }
                  >
                    <form class="description-editor" onSubmit={saveDesc}>
                      <label class="sr-only" for={descriptionId}>
                        Task description
                      </label>
                      <div class="description-editor-box">
                        <div class="description-editor-top">
                          <div
                            class="segmented segmented-compact"
                            role="tablist"
                            aria-label="Description mode"
                          >
                            <button
                              type="button"
                              role="tab"
                              aria-selected={descMode() === 'write' ? 'true' : 'false'}
                              class={['segmented-item', { active: descMode() === 'write' }]}
                              onClick={() => setDescMode('write')}
                            >
                              Write
                            </button>
                            <button
                              type="button"
                              role="tab"
                              aria-selected={descMode() === 'preview' ? 'true' : 'false'}
                              class={['segmented-item', { active: descMode() === 'preview' }]}
                              onClick={() => setDescMode('preview')}
                            >
                              Preview
                            </button>
                          </div>
                        </div>

                        <Show
                          when={descMode() === 'write'}
                          fallback={
                            <div class="description-preview-area">
                              <Show
                                when={descDraft().trim()}
                                fallback={<p class="muted-small">Nothing to preview.</p>}
                              >
                                <Markdown source={descDraft()} />
                              </Show>
                            </div>
                          }
                        >
                          <textarea
                            ref={descriptionInput}
                            id={descriptionId}
                            class="description-textarea"
                            rows="5"
                            value={descDraft()}
                            onInput={(event) => updateDesc(event.currentTarget.value)}
                            onKeyDown={handleDescTextareaKeyDown}
                            placeholder="Write a Markdown description…"
                            disabled={descBusy()}
                          />
                        </Show>

                        <div class="composer-toolbar">
                          <span class="composer-hint">Markdown supported · ⌘↵ to save</span>
                          <div class="action-group">
                            <button
                              class="btn btn-ghost btn-sm"
                              type="button"
                              onClick={cancelDescEdit}
                              disabled={descBusy()}
                            >
                              Cancel
                            </button>
                            <button
                              class="btn btn-primary btn-sm"
                              type="submit"
                              disabled={!descDirty() || descBusy()}
                            >
                              {descBusy() ? 'Saving…' : 'Save'}
                            </button>
                          </div>
                        </div>
                      </div>
                      <Show when={descError()}>
                        <p id={descErrorId} class="field-error" role="alert">
                          {descError()}
                        </p>
                      </Show>
                    </form>
                  </Show>
                </section>

                <section
                  class={['composer-section', { 'is-review': inReview() }]}
                  aria-label={inReview() ? 'Review and comment' : 'Add comment'}
                >
                  <Show when={inReview()}>
                    <div class="composer-review-head">
                      <span class="composer-review-title">Ready for review</span>
                      <span class="composer-review-sub">
                        Approve as-is, or describe what needs to change to re-queue for dispatch.
                      </span>
                    </div>
                  </Show>

                  <form class="composer-form" onSubmit={submitComment}>
                    <label class="sr-only" for={commentId}>
                      {inReview() ? 'Review comment' : 'Add a comment'}
                    </label>
                    <div class={['composer-box', { 'is-review': inReview() }]}>
                      <textarea
                        ref={commentInput}
                        id={commentId}
                        class="composer-textarea"
                        rows="3"
                        value={comment()}
                        onInput={(event) => updateComment(event.currentTarget.value)}
                        onKeyDown={handleCommentKeyDown}
                        placeholder={
                          inReview()
                            ? 'Write review notes or explain what needs to change…'
                            : 'Add a comment or instructions for the next agent run…'
                        }
                        disabled={actionBusy() !== null}
                      />
                      <div class="composer-toolbar">
                        <span class="composer-hint">
                          <Show
                            when={inReview() && !hasComment()}
                            fallback="Markdown supported · ⌘↵ to comment"
                          >
                            Add a comment to request changes
                          </Show>
                        </span>
                        <div class="action-group">
                          <Show
                            when={inReview()}
                            fallback={
                              <button
                                class="btn btn-primary btn-sm"
                                type="submit"
                                disabled={actionBusy() !== null || !hasComment()}
                              >
                                {actionBusy() === 'comment' ? 'Sending…' : 'Comment'}
                              </button>
                            }
                          >
                            <button
                              class="btn btn-ghost btn-sm"
                              type="submit"
                              disabled={actionBusy() !== null || !hasComment()}
                            >
                              {actionBusy() === 'comment' ? 'Sending…' : 'Comment'}
                            </button>
                            <button
                              class="btn btn-secondary btn-sm"
                              type="button"
                              onClick={() => runReview(false)}
                              disabled={actionBusy() !== null || !hasComment()}
                              title={
                                hasComment()
                                  ? 'Send comment and re-queue task'
                                  : 'Write what needs to change first'
                              }
                            >
                              {actionBusy() === 'changes' ? 'Requesting…' : 'Request changes'}
                            </button>
                            <button
                              class="btn btn-primary btn-sm"
                              type="button"
                              onClick={() => runReview(true)}
                              disabled={actionBusy() !== null}
                            >
                              {actionBusy() === 'approve' ? 'Approving…' : 'Approve'}
                            </button>
                          </Show>
                        </div>
                      </div>
                    </div>
                    <Show when={commentError()}>
                      <p id={commentErrorId} class="field-error" role="alert">
                        {commentError()}
                      </p>
                    </Show>
                  </form>
                </section>

                <section class="stream-section" aria-labelledby="stream-activity-heading">
                  <div class="stream-section-head">
                    <h3 id="stream-activity-heading" class="stream-heading">
                      Activity & comments
                    </h3>
                    <span class="muted-small">
                      {detail().activity.length}{' '}
                      {detail().activity.length === 1 ? 'event' : 'events'}
                    </span>
                  </div>
                  <ActivityFeed activity={detail().activity} />
                </section>
              </div>
            </div>
          );
        }}
      </Show>
    </section>
  );
}
