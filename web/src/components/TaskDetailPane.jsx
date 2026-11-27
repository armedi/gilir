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

function focusEditor(editor, selectAll = false) {
  requestAnimationFrame(() => {
    if (!editor.isConnected || !editor.isContentEditable) return;
    editor.focus();
    const range = document.createRange();
    range.selectNodeContents(editor);
    if (!selectAll) range.collapse(false);
    const selection = window.getSelection();
    selection.removeAllRanges();
    selection.addRange(range);
  });
}

function focusSoon(target) {
  requestAnimationFrame(() => {
    const element = target();
    if (element?.isConnected) element.focus();
  });
}

function readDescription(editor) {
  const isParagraph = (node) => node.nodeName === 'DIV' || node.nodeName === 'P';
  const children = Array.from(editor.childNodes);
  if (children.some(isParagraph)) {
    // Native multiline paste can wrap lines in blocks. Read each block on its own
    // so an empty block's caret placeholder doesn't add a second line break.
    return children.map((child, index) => {
      const paragraph = isParagraph(child);
      const boundary = index > 0 && (paragraph || isParagraph(children[index - 1]));
      const text = paragraph ? readDescription(child) : child.nodeValue ?? child.innerText ?? '\n';
      return boundary ? `\n${text}` : text;
    }).join('');
  }
  const text = editor.innerText;
  const lastChild = editor.lastChild;
  // The browser adds a final break to keep the caret visible on an empty line.
  if (lastChild?.nodeName === 'BR' || lastChild?.nodeValue === '\n') {
    return text.replace(/\n$/, '');
  }
  return text;
}

export default function TaskDetailPane(props) {
  const titleErrorId = createUniqueId();
  const descErrorId = createUniqueId();
  const commentId = createUniqueId();
  const commentErrorId = createUniqueId();
  let commentInput;
  let activeTaskKey = null;
  let activeDescKey = null;
  let activeCommentKey = null;
  // Solid 2 commits signal writes on flush, so a read right after a write can
  // see the old value. Guard delete requests with a plain flag to stop double
  // submits from a double click.
  let deletePending = false;

  const [loadedTaskId, setLoadedTaskId] = createSignal(null);

  const [editingTitle, setEditingTitle] = createSignal(false);
  const [titleBusy, setTitleBusy] = createSignal(false);
  const [titleError, setTitleError] = createSignal(null);

  const [editingDesc, setEditingDesc] = createSignal(false);
  const [descDraft, setDescDraft] = createSignal('');
  const [descBusy, setDescBusy] = createSignal(false);
  const [descError, setDescError] = createSignal(null);

  const [actionBusy, setActionBusy] = createSignal(null);
  const [comment, setComment] = createSignal('');
  const [commentError, setCommentError] = createSignal(null);

  const [confirmingDelete, setConfirmingDelete] = createSignal(false);
  const [deleteBusy, setDeleteBusy] = createSignal(false);
  const [deleteError, setDeleteError] = createSignal(null);
  let deleteButton;
  let confirmDeleteButton;

  const task = () => props.detail?.task;
  const inReview = () => task()?.status === 'in-review';
  const hasComment = () => comment().trim().length > 0;

  const projectName = createMemo(() => {
    const current = task();
    if (!current) return '';
    const found = asArray(props.projects).find(
      (project) => String(project.id) === String(current.project_id),
    );
    return found ? found.name : `Project #${current.project_id}`;
  });

  function loadTaskState(current) {
    activeTaskKey = `task:${current.id}`;
    activeDescKey = draftKey('task-description', current.id);
    activeCommentKey = draftKey('comment', current.id);

    setEditingTitle(false);
    setTitleBusy(false);
    setTitleError(null);

    const baseDesc = asText(current.description);
    const storedDesc = loadDraft(activeDescKey);
    const hasDescDraft = typeof storedDesc === 'string' && storedDesc !== baseDesc;
    setDescDraft(hasDescDraft ? storedDesc : baseDesc);
    setEditingDesc(hasDescDraft);
    setDescBusy(false);
    setDescError(null);

    const storedComment = loadDraft(activeCommentKey);
    setComment(typeof storedComment === 'string' ? storedComment : '');
    setCommentError(null);
    setActionBusy(null);
    setConfirmingDelete(false);
    setDeleteBusy(false);
    setDeleteError(null);
    setLoadedTaskId(String(current.id));
  }

  createEffect(
    () => ({
      current: task(),
      isEditingDesc: editingDesc(),
    }),
    ({ current, isEditingDesc }) => {
      if (!current) return;
      if (loadedTaskId() !== String(current.id)) {
        loadTaskState(current);
        return;
      }
      if (!isEditingDesc) setDescDraft(asText(current.description));
    },
  );

  function startTitleEdit() {
    const current = task();
    if (!current || titleBusy()) return;
    setTitleError(null);
    setEditingTitle(true);
  }

  function cancelTitleEdit() {
    setTitleError(null);
    setEditingTitle(false);
  }

  async function saveTitle(event) {
    if (titleBusy() || !editingTitle()) return;
    const current = task();
    if (!current || loadedTaskId() !== String(current.id)) return;
    const editor = event.currentTarget;
    const next = editor.innerText.replace(/[\r\n]+/g, ' ').trim();
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
        requestAnimationFrame(() => {
          if (editor.isConnected) editor.focus();
        });
      }
    } finally {
      if (activeTaskKey === taskKey) setTitleBusy(false);
    }
  }

  function handleTitleKeyDown(event) {
    if (titleBusy() || event.isComposing || event.keyCode === 229) return;
    if (event.key === 'Enter') {
      event.preventDefault();
      saveTitle(event);
    } else if (event.key === 'Escape') {
      event.preventDefault();
      cancelTitleEdit();
    }
  }

  function startDescEdit() {
    const current = task();
    if (!current || descBusy()) return;
    setDescDraft(asText(current.description));
    setDescError(null);
    setEditingDesc(true);
  }

  function handleDescriptionClick() {
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
    if (descBusy() || !editingDesc()) return;
    const current = task();
    if (!current || loadedTaskId() !== String(current.id)) return;
    const next = readDescription(event.currentTarget);
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

  function handleDescEditorKeyDown(event) {
    if (descBusy() || event.isComposing || event.keyCode === 229) return;
    if ((event.metaKey || event.ctrlKey) && event.key === 'Enter') {
      event.preventDefault();
      saveDesc(event);
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
      // The review message is part of the review command so the server records
      // it as a comment and enforces it for request-changes from every client.
      const args = { id: taskId, approve };
      if (body) args.message = body;
      await command('task.review', args);
      if (body && commentKey) clearDraft(commentKey);
      if (body && activeTaskKey === taskKey) setComment('');
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

  // Comment rows own their editor state; this runs the command, refetches and
  // toasts. Errors propagate so the row can keep its draft and show them inline.
  async function mutateComment(name, commentId, args, notice) {
    const current = task();
    if (!current || loadedTaskId() !== String(current.id)) {
      throw new Error('This task is not loaded yet.');
    }
    const taskKey = activeTaskKey;
    await command(name, { id: current.id, comment_id: commentId, ...args });
    await props.onRefresh();
    if (activeTaskKey === taskKey) props.notify(notice);
  }

  const editComment = (commentId, body) =>
    mutateComment('task.comment.edit', commentId, { body }, 'Comment updated');
  const removeComment = (commentId) =>
    mutateComment('task.comment.remove', commentId, {}, 'Comment removed');

  function startDelete() {
    const current = task();
    if (!current || deleteBusy() || deletePending) return;
    setDeleteError(null);
    setConfirmingDelete(true);
    focusSoon(() => confirmDeleteButton);
  }

  function cancelDelete() {
    if (deleteBusy() || deletePending) return;
    setDeleteError(null);
    setConfirmingDelete(false);
    focusSoon(() => deleteButton);
  }

  // Deletes are irreversible, so the request runs against the ID captured when
  // the action started: a poll that swaps in another task mid-request must not
  // retarget it. Errors stay attached to that task and only surface while it is
  // still the loaded one.
  async function removeTask() {
    if (deletePending) return;
    const current = task();
    if (!current || loadedTaskId() !== String(current.id)) return;
    const taskId = current.id;
    const taskKey = activeTaskKey;
    deletePending = true;
    setDeleteBusy(true);
    setDeleteError(null);
    try {
      await props.onDelete(taskId);
    } catch (requestError) {
      if (activeTaskKey === taskKey) {
        setDeleteError(errorText(requestError));
        focusSoon(() => confirmDeleteButton);
      }
    } finally {
      deletePending = false;
      if (activeTaskKey === taskKey) setDeleteBusy(false);
    }
  }

  function handleConfirmDeleteKeyDown(event) {
    if (deleteBusy() || event.key !== 'Escape') return;
    event.preventDefault();
    cancelDelete();
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
              when={editingTitle() && loadedTaskId() === String(task()?.id)}
              fallback={
                <Show
                  when={task()}
                  fallback={<h2 class="center-title">Task {props.taskId ?? ''}</h2>}
                >
                  {(current) => (
                    <button
                      type="button"
                      class="center-title inline-title-trigger"
                      title="Click to edit title"
                      onClick={startTitleEdit}
                    >
                      {current().title || 'Untitled task'}
                    </button>
                  )}
                </Show>
              }
            >
              <div class="inline-title-edit">
                <div
                  ref={(editor) => {
                    editor.textContent = asText(task()?.title).replace(/[\r\n]+/g, ' ');
                    focusEditor(editor, true);
                  }}
                  class="center-title inline-title-textbox"
                  data-placeholder="Untitled task"
                  role="textbox"
                  aria-label="Task title"
                  aria-multiline="false"
                  contenteditable={titleBusy() ? 'false' : 'plaintext-only'}
                  aria-readonly={titleBusy()}
                  onInput={() => setTitleError(null)}
                  onKeyDown={handleTitleKeyDown}
                  onBlur={saveTitle}
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
                  <span class="center-id">{props.taskId}</span>
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
            <Show when={task() && !confirmingDelete()}>
              <button
                ref={deleteButton}
                class="btn btn-ghost btn-sm"
                type="button"
                aria-label={`Delete task ${props.taskId}`}
                onClick={startDelete}
                disabled={deleteBusy()}
              >
                Delete
              </button>
            </Show>

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

        <Show when={confirmingDelete()}>
          <div class="center-header-inner task-delete-confirm">
            <div
              class="confirm-inline"
              role="group"
              aria-label={`Confirm deletion of task ${props.taskId}`}
              onKeyDown={handleConfirmDeleteKeyDown}
            >
              <span class="confirm-text">Delete task {props.taskId}?</span>
              <button
                ref={confirmDeleteButton}
                class="btn btn-danger btn-sm"
                type="button"
                aria-label={`Confirm deletion of task ${props.taskId}`}
                onClick={removeTask}
                disabled={deleteBusy()}
              >
                {deleteBusy() ? 'Deleting…' : 'Confirm delete'}
              </button>
              <button
                class="btn btn-ghost btn-sm"
                type="button"
                onClick={cancelDelete}
                disabled={deleteBusy()}
              >
                Cancel
              </button>
            </div>
          </div>
        </Show>

        <Show when={deleteError()}>
          <div class="inline-alert task-delete-error" role="alert">
            <span>{deleteError()}</span>
          </div>
        </Show>
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
                    when={editingDesc() && loadedTaskId() === String(current().id)}
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
                        <span class={{ 'description-empty': !current().description }}>
                          {current().description || 'Add a description…'}
                        </span>
                      </div>
                    }
                  >
                    <div
                      ref={(editor) => {
                        const value = descDraft();
                        editor.textContent = value;
                        if (value.endsWith('\n')) editor.append(document.createElement('br'));
                        focusEditor(editor);
                      }}
                      class={['description-display description-textbox', { 'is-empty': descDraft() === '' }]}
                      role="textbox"
                      aria-label="Task description"
                      aria-multiline="true"
                      aria-readonly={descBusy()}
                      aria-describedby={descError() ? descErrorId : undefined}
                      contenteditable={descBusy() ? 'false' : 'plaintext-only'}
                      data-placeholder="Add a description…"
                      onInput={(event) => updateDesc(readDescription(event.currentTarget))}
                      onKeyDown={handleDescEditorKeyDown}
                      onBlur={saveDesc}
                    />
                    <Show when={descError()}>
                      <p id={descErrorId} class="field-error" role="alert">
                        {descError()}
                      </p>
                    </Show>
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
                  <ActivityFeed
                    activity={detail().activity}
                    onEditComment={editComment}
                    onRemoveComment={removeComment}
                  />
                </section>
              </div>
            </div>
          );
        }}
      </Show>
    </section>
  );
}
