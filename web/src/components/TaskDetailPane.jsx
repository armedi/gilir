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
import { PriorityBadge, StatusBadge, BlockedBadge } from './Badges';
import ActivityFeed from './ActivityFeed';
import { isBlocked } from '../tone';

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

function readBody(editor) {
  const isParagraph = (node) => node.nodeName === 'DIV' || node.nodeName === 'P';
  const children = Array.from(editor.childNodes);
  if (children.some(isParagraph)) {
    // Native multiline paste can wrap lines in blocks. Read each block on its own
    // so an empty block's caret placeholder doesn't add a second line break.
    return children.map((child, index) => {
      const paragraph = isParagraph(child);
      const boundary = index > 0 && (paragraph || isParagraph(children[index - 1]));
      const text = paragraph ? readBody(child) : child.nodeValue ?? child.innerText ?? '\n';
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
  const bodyErrorId = createUniqueId();
  const commentId = createUniqueId();
  const commentErrorId = createUniqueId();
  let commentInput;
  let activeTaskKey = null;
  let activeBodyKey = null;
  let activeCommentKey = null;
  // Solid 2 commits signal writes on flush, so a read right after a write can
  // see the old value. Guard delete requests with a plain flag to stop double
  // submits from a double click.
  let deletePending = false;

  const [loadedTaskId, setLoadedTaskId] = createSignal(null);

  const [editingTitle, setEditingTitle] = createSignal(false);
  const [titleBusy, setTitleBusy] = createSignal(false);
  const [titleError, setTitleError] = createSignal(null);

  const [editingBody, setEditingBody] = createSignal(false);
  const [bodyDraft, setBodyDraft] = createSignal('');
  const [bodyBusy, setBodyBusy] = createSignal(false);
  const [bodyError, setBodyError] = createSignal(null);

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
  const blocked = () => isBlocked(task());
  const hasComment = () => comment().trim().length > 0;
  // Blocked tasks are actionable like reviews, so the same composer serves a
  // Block or an Unblock instead of an ordinary comment.
  const blockable = () =>
    ['backlog', 'todo', 'in-progress'].includes(task()?.status) && !blocked();
  // An idle in-progress task has no open dispatch attempt: unblocking it puts it
  // back in the queue rather than only clearing the marking.
  const idleInProgress = () => task()?.status === 'in-progress' && !props.detail?.attempt;

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
    activeBodyKey = draftKey('task-body', current.id);
    activeCommentKey = draftKey('comment', current.id);

    setEditingTitle(false);
    setTitleBusy(false);
    setTitleError(null);

    const baseBody = asText(current.body);
    const storedBody = loadDraft(activeBodyKey);
    const hasBodyDraft = typeof storedBody === 'string' && storedBody !== baseBody;
    setBodyDraft(hasBodyDraft ? storedBody : baseBody);
    setEditingBody(hasBodyDraft);
    setBodyBusy(false);
    setBodyError(null);

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
      isEditingBody: editingBody(),
    }),
    ({ current, isEditingBody }) => {
      if (!current) return;
      if (loadedTaskId() !== String(current.id)) {
        loadTaskState(current);
        return;
      }
      if (!isEditingBody) setBodyDraft(asText(current.body));
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

  function startBodyEdit() {
    const current = task();
    if (!current || bodyBusy()) return;
    setBodyDraft(asText(current.body));
    setBodyError(null);
    setEditingBody(true);
  }

  function handleBodyClick() {
    if (window.getSelection && window.getSelection().toString()) return;
    startBodyEdit();
  }

  function handleBodyKeyDown(event) {
    if (event.key === 'Enter' || event.key === ' ') {
      event.preventDefault();
      startBodyEdit();
    }
  }

  function updateBody(value) {
    setBodyDraft(value);
    setBodyError(null);
    if (!activeBodyKey) return;
    if (value !== asText(task()?.body)) {
      saveDraft(activeBodyKey, value);
    } else {
      clearDraft(activeBodyKey);
    }
  }

  function cancelBodyEdit() {
    if (activeBodyKey) clearDraft(activeBodyKey);
    setBodyDraft(asText(task()?.body));
    setBodyError(null);
    setEditingBody(false);
  }

  async function saveBody(event) {
    if (bodyBusy() || !editingBody()) return;
    const current = task();
    if (!current || loadedTaskId() !== String(current.id)) return;
    const next = readBody(event.currentTarget);
    if (next === asText(current.body)) {
      cancelBodyEdit();
      return;
    }
    const taskId = current.id;
    const taskKey = activeTaskKey;
    const bodyKey = activeBodyKey;
    setBodyBusy(true);
    setBodyError(null);
    try {
      await command('task.update', { id: taskId, body: next });
      if (bodyKey) clearDraft(bodyKey);
      await props.onRefresh();
      if (activeTaskKey === taskKey) {
        setEditingBody(false);
        props.notify('Body updated');
      }
    } catch (requestError) {
      if (activeTaskKey === taskKey) {
        setBodyError(errorText(requestError));
      }
    } finally {
      if (activeTaskKey === taskKey) setBodyBusy(false);
    }
  }

  function handleBodyEditorKeyDown(event) {
    if (bodyBusy() || event.isComposing || event.keyCode === 229) return;
    if ((event.metaKey || event.ctrlKey) && event.key === 'Enter') {
      event.preventDefault();
      saveBody(event);
    } else if (event.key === 'Escape') {
      event.preventDefault();
      cancelBodyEdit();
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
    const message = comment().trim();
    if (!message) {
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
      await command('task.comment', { id: taskId, message });
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

  // Blocking requires a message because it is the record of why the task
  // stopped; unblocking accepts an optional one. Both run through the wire
  // command so the store enforces the status rules for every client.
  async function runBlock(unblock) {
    if (actionBusy() !== null) return;
    const current = task();
    if (!current) return;
    const body = comment().trim();

    if (!unblock && !body) {
      setCommentError('Explain why this task is blocked.');
      commentInput?.focus();
      return;
    }

    const taskId = current.id;
    const taskKey = activeTaskKey;
    const commentKey = activeCommentKey;
    setActionBusy(unblock ? 'unblock' : 'block');
    setCommentError(null);
    try {
      const args = { id: taskId };
      if (body) args.message = body;
      await command(unblock ? 'task.unblock' : 'task.block', args);
      if (body && commentKey) clearDraft(commentKey);
      if (body && activeTaskKey === taskKey) setComment('');
      await props.onRefresh();
      if (activeTaskKey === taskKey) {
        props.notify(unblock ? 'Task unblocked' : 'Task blocked');
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

  const editComment = (commentId, message) =>
    mutateComment('task.comment.edit', commentId, { message }, 'Comment updated');
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
                  <Show when={blocked()}>
                    <BlockedBadge />
                  </Show>
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
                <section class="task-body-block" aria-label="Task body">
                  <Show
                    when={editingBody() && loadedTaskId() === String(current().id)}
                    fallback={
                      <div
                        class="task-body-display"
                        role="button"
                        tabindex="0"
                        aria-label="Edit task body"
                        title="Click to edit body"
                        onClick={handleBodyClick}
                        onKeyDown={handleBodyKeyDown}
                      >
                        <span class={{ 'task-body-empty': !current().body }}>
                          {current().body || 'Add a body…'}
                        </span>
                      </div>
                    }
                  >
                    <div
                      ref={(editor) => {
                        const value = bodyDraft();
                        editor.textContent = value;
                        if (value.endsWith('\n')) editor.append(document.createElement('br'));
                        focusEditor(editor);
                      }}
                      class={['task-body-display task-body-textbox', { 'is-empty': bodyDraft() === '' }]}
                      role="textbox"
                      aria-label="Task body"
                      aria-multiline="true"
                      aria-readonly={bodyBusy()}
                      aria-describedby={bodyError() ? bodyErrorId : undefined}
                      contenteditable={bodyBusy() ? 'false' : 'plaintext-only'}
                      data-placeholder="Add a body…"
                      onInput={(event) => updateBody(readBody(event.currentTarget))}
                      onKeyDown={handleBodyEditorKeyDown}
                      onBlur={saveBody}
                    />
                    <Show when={bodyError()}>
                      <p id={bodyErrorId} class="field-error" role="alert">
                        {bodyError()}
                      </p>
                    </Show>
                  </Show>
                </section>

                <section
                  class={[
                    'composer-section',
                    { 'is-review': inReview(), 'is-blocked': blocked() },
                  ]}
                  aria-label={
                    blocked() ? 'Blocked task' : inReview() ? 'Review and comment' : 'Add comment'
                  }
                >
                  <Show when={blocked()}>
                    <div class="composer-review-head">
                      <span class="composer-review-title is-blocked">Blocked</span>
                      <span class="composer-review-sub">
                        {idleInProgress()
                          ? 'Unblock it to re-queue this task for dispatch, or add a comment.'
                          : 'This task is not dispatched until it is unblocked. Add a comment or unblock it.'}
                      </span>
                    </div>
                  </Show>

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
                      {blocked() ? 'Block comment' : inReview() ? 'Review comment' : 'Add a comment'}
                    </label>
                    <div
                      class={[
                        'composer-box',
                        { 'is-review': inReview(), 'is-blocked': blocked() },
                      ]}
                    >
                      <textarea
                        ref={commentInput}
                        id={commentId}
                        class="composer-textarea"
                        rows="3"
                        value={comment()}
                        onInput={(event) => updateComment(event.currentTarget.value)}
                        onKeyDown={handleCommentKeyDown}
                        placeholder={
                          blocked()
                            ? 'Explain what is blocked, or what unblocked this task…'
                            : inReview()
                              ? 'Write review notes or explain what needs to change…'
                              : 'Add a comment or instructions for the next agent run…'
                        }
                        disabled={actionBusy() !== null}
                      />
                      <div class="composer-toolbar">
                        <span class="composer-hint">
                          <Show
                            when={(inReview() || blockable()) && !hasComment()}
                            fallback="Markdown supported · ⌘↵ to comment"
                          >
                            {inReview() ? 'Add a comment to request changes' : 'Explain why it is blocked'}
                          </Show>
                        </span>
                        <div class="action-group">
                          <Show
                            when={inReview() || blocked()}
                            fallback={
                              <Show
                                when={blockable()}
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
                                  onClick={() => runBlock(false)}
                                  disabled={actionBusy() !== null || !hasComment()}
                                  title={hasComment() ? 'Block this task' : 'Explain the block first'}
                                >
                                  {actionBusy() === 'block' ? 'Blocking…' : 'Block'}
                                </button>
                              </Show>
                            }
                          >
                            <button
                              class="btn btn-ghost btn-sm"
                              type="submit"
                              disabled={actionBusy() !== null || !hasComment()}
                            >
                              {actionBusy() === 'comment' ? 'Sending…' : 'Comment'}
                            </button>
                            <Show
                              when={blocked()}
                              fallback={
                                <>
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
                                </>
                              }
                            >
                              <button
                                class="btn btn-primary btn-sm"
                                type="button"
                                onClick={() => runBlock(true)}
                                disabled={actionBusy() !== null}
                                title={
                                  idleInProgress()
                                    ? 'Clear the block and re-queue this task'
                                    : 'Clear the block'
                                }
                              >
                                {actionBusy() === 'unblock'
                                  ? 'Unblocking…'
                                  : idleInProgress()
                                    ? 'Unblock & re-queue'
                                    : 'Unblock'}
                              </button>
                            </Show>
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
