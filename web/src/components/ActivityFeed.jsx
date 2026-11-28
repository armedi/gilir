import { For, Show, createMemo, createSignal, createUniqueId } from 'solid-js';
import { ACTOR } from '../api';
import Markdown from './Markdown';
import {
  asText,
  errorText,
  formatDateTime,
  formatFullDateTime,
  humanize,
  priorityLabel,
  safeHttpUrl,
  statusLabel,
  timeAttribute,
} from '../utils';

const EVENT_TITLES = {
  'task.created': 'Created task',
  'task.updated': 'Updated task',
  'task.claimed': 'Dispatched to agent',
  'task.status_changed': 'Status',
  'task.priority_changed': 'Priority',
  'task.marking_changed': 'Marking',
  'task.worktree_recorded': 'Worktree prepared',  'task.comment_added': 'Comment',
  'task.dependency_added': 'Added blocker',
  'task.dependency_removed': 'Removed blocker',
  'task.review_approved': 'Approved',
  'task.review_changes_requested': 'Requested changes',
  'session.linked': 'OpenCode session',
  'session.outcome': 'Session finished',
};

function normalizeType(value) {
  const raw = asText(value).toLowerCase();
  return EVENT_TITLES[raw] ? raw : raw.replace(/_/g, '.');
}

function eventTitle(value) {
  const type = normalizeType(value);
  return EVENT_TITLES[type] || humanize(value || 'activity');
}

function eventTone(event) {
  const type = normalizeType(event.type);
  const data = event.data && typeof event.data === 'object' ? event.data : {};
  if (type === 'task.review_approved') return 'accent';
  if (type === 'task.review_changes_requested') return 'review';
  if (type === 'session.outcome') {
    const outcome = asText(data.outcome).toLowerCase();
    if (outcome === 'success' || outcome === 'succeeded') return 'accent';
    if (outcome === 'error' || outcome === 'failed') return 'danger';
  }
  if (type === 'task.status_changed' && data.new === 'in-review') return 'review';
  if (type === 'task.status_changed' && data.new === 'done') return 'accent';
  return 'neutral';
}

function commentBody(data) {
  if (!data || typeof data !== 'object') return '';
  for (const key of ['body', 'comment', 'message', 'content']) {
    if (typeof data[key] === 'string' && data[key].trim()) return data[key];
  }
  return '';
}

function shortId(value) {
  const text = asText(value);
  if (text.length <= 22) return text;
  return `${text.slice(0, 18)}…`;
}

function renderSummary(event) {
  const type = normalizeType(event.type);
  const data = event.data && typeof event.data === 'object' && !Array.isArray(event.data)
    ? event.data
    : null;
  if (!data) return null;

  if (type === 'task.status_changed' && (data.old || data.new)) {
    return (
      <span>
        {statusLabel(data.old)} → <strong>{statusLabel(data.new)}</strong>
      </span>
    );
  }

  if (type === 'task.priority_changed' && (data.old || data.new)) {
    return (
      <span>
        {priorityLabel(data.old)} → <strong>{priorityLabel(data.new)}</strong>
      </span>
    );
  }

  if (type === 'task.updated' && data.new && typeof data.new === 'object') {
    const fields = Object.keys(data.new).map((key) => key.replace(/_/g, ' '));
    if (fields.length > 0) return <span>Changed {fields.join(', ')}</span>;
  }

  if (type === 'task.created') {
    const parts = [];
    if (data.status) parts.push(statusLabel(data.status));
    if (data.priority) parts.push(`${priorityLabel(data.priority)} priority`);
    if (parts.length > 0) return <span>{parts.join(', ')}</span>;
  }

  if (type === 'task.marking_changed' && data.marking) {
    const verb = data.new === true ? 'Added' : data.new === false ? 'Removed' : 'Updated';
    return (
      <span>
        {verb} <span class="activity-inline-mono">{String(data.marking)}</span>
      </span>
    );
  }

  // The store emits "dependency.added"/"dependency.removed"; older dashboards
  // used "task.dependency_*". Accept both so the blocker's GILIR- ID shows.
  if (
    type === 'task.dependency_added' ||
    type === 'task.dependency_removed' ||
    type === 'dependency.added' ||
    type === 'dependency.removed'
  ) {
    const blockerId = data.blocker_id ?? data.depends_on_id ?? data.id;
    if (blockerId != null) {
      return <span class="activity-inline-mono">{String(blockerId)}</span>;
    }
  }

  if (type === 'session.linked') {
    const href = safeHttpUrl(data.url);
    const label = data.session_id ? shortId(data.session_id) : 'Open session';
    if (href) {
      return (
        <a href={href} target="_blank" rel="noopener noreferrer" class="activity-inline-mono">
          {label} ↗
        </a>
      );
    }
    if (data.session_id) return <span class="activity-inline-mono">{label}</span>;
  }

  if (type === 'session.outcome') {
    const text = asText(data.detail || data.outcome).trim();
    if (text) return <span>{text}</span>;
  }

  if (type === 'task.claimed' && data.attempt_id) {
    return <span class="activity-inline-mono">attempt {shortId(data.attempt_id)}</span>;
  }

  if (type === 'task.worktree_recorded' && data.worktree) {
    return <span class="activity-inline-mono">{asText(data.worktree)}</span>;
  }

  return null;
}

// Only the web actor's own plain comments are editable; the server enforces the
// same rule, so other rows never offer controls that would be rejected.
function canManageComment(event) {
  return event.type === 'task.commented' && event.actor === ACTOR && event.id != null;
}

function CommentItem(props) {
  const textareaId = createUniqueId();
  const errorId = createUniqueId();
  // Rows are keyed by activity id, so this state survives detail polling and
  // resets when the comment leaves the feed or another task is selected.
  const [mode, setMode] = createSignal(null);
  const [draft, setDraft] = createSignal('');
  const [busy, setBusy] = createSignal(false);
  const [error, setError] = createSignal(null);
  let editButton;
  let removeButton;
  let textarea;
  let cancelRemoveButton;
  // Solid 2 commits signal writes on flush, so a read right after a write (an
  // input and submit in one tick, or a double submit) can see the old value.
  // Guard requests with a plain flag and submit what the textarea holds.
  let pending = false;

  const event = () => props.event();
  const manageable = () =>
    canManageComment(event()) && Boolean(props.onEdit) && Boolean(props.onRemove);
  const editing = () => mode() === 'edit';
  const confirming = () => mode() === 'remove';
  const editedAt = () => {
    const data = event().data;
    return data && typeof data.edited_at === 'string' && data.edited_at ? data.edited_at : '';
  };

  // Resolve the target after the frame: these elements mount or re-render when
  // the mode changes, so their refs only point at live nodes afterwards.
  function focusSoon(target) {
    requestAnimationFrame(() => {
      const element = target();
      if (element?.isConnected) element.focus();
    });
  }

  function startEdit() {
    setDraft(props.body());
    setError(null);
    setMode('edit');
    focusSoon(() => textarea);
  }

  function startRemove() {
    setError(null);
    setMode('remove');
    focusSoon(() => cancelRemoveButton);
  }

  // Callers guard against closing mid-request: Cancel is disabled and the key
  // handlers return early while busy.
  function close(returnTo) {
    setMode(null);
    setError(null);
    focusSoon(() => (returnTo === 'remove' ? removeButton : editButton));
  }

  async function save(submitEvent) {
    submitEvent?.preventDefault();
    if (pending || !textarea?.isConnected) return;
    const value = textarea.value;
    setDraft(value);
    const body = value.trim();
    if (!body) {
      setError('Enter a comment.');
      textarea?.focus();
      return;
    }
    if (body === props.body().trim()) {
      close('edit');
      return;
    }
    pending = true;
    setBusy(true);
    setError(null);
    try {
      await props.onEdit(event().id, body);
      close('edit');
    } catch (requestError) {
      setError(errorText(requestError));
      focusSoon(() => textarea);
    } finally {
      pending = false;
      setBusy(false);
    }
  }

  async function remove() {
    if (pending) return;
    pending = true;
    setBusy(true);
    setError(null);
    try {
      await props.onRemove(event().id);
      setMode(null);
    } catch (requestError) {
      setError(errorText(requestError));
      focusSoon(() => cancelRemoveButton);
    } finally {
      pending = false;
      setBusy(false);
    }
  }

  function handleEditorKeyDown(keyEvent) {
    if (pending || keyEvent.isComposing || keyEvent.keyCode === 229) return;
    if ((keyEvent.metaKey || keyEvent.ctrlKey) && keyEvent.key === 'Enter') {
      keyEvent.preventDefault();
      save();
    } else if (keyEvent.key === 'Escape') {
      keyEvent.preventDefault();
      close('edit');
    }
  }

  function handleConfirmKeyDown(keyEvent) {
    if (pending || keyEvent.key !== 'Escape') return;
    keyEvent.preventDefault();
    close('remove');
  }

  return (
    <li class="activity-comment-item">
      <div class="activity-comment-head">
        <span class="activity-comment-actor">{props.actor()}</span>
        <span class="activity-comment-meta">
          <time
            class="activity-comment-time"
            datetime={timeAttribute(event().timestamp)}
            title={formatFullDateTime(event().timestamp)}
          >
            {formatDateTime(event().timestamp)}
          </time>
          <Show when={editedAt()}>
            <span
              class="activity-comment-edited"
              title={`Edited ${formatFullDateTime(editedAt())}`}
            >
              · edited
            </span>
          </Show>
          <Show when={manageable() && mode() === null}>
            <span class="activity-comment-actions">
              <button
                ref={editButton}
                type="button"
                class="btn btn-ghost btn-sm"
                aria-label="Edit comment"
                onClick={startEdit}
              >
                Edit
              </button>
              <button
                ref={removeButton}
                type="button"
                class="btn btn-ghost btn-sm"
                aria-label="Remove comment"
                onClick={startRemove}
              >
                Remove
              </button>
            </span>
          </Show>
        </span>
      </div>

      <Show when={editing()} fallback={<Markdown source={props.body()} />}>
        <form class="activity-comment-edit" onSubmit={save}>
          <label class="sr-only" for={textareaId}>
            Edit comment
          </label>
          <div class="composer-box">
            <textarea
              ref={textarea}
              id={textareaId}
              class="composer-textarea"
              rows="3"
              value={draft()}
              onInput={(inputEvent) => {
                setDraft(inputEvent.currentTarget.value);
                setError(null);
              }}
              onKeyDown={handleEditorKeyDown}
              disabled={busy()}
              aria-invalid={error() ? 'true' : undefined}
              aria-describedby={error() ? errorId : undefined}
            />
            <div class="composer-toolbar">
              <span class="composer-hint">Markdown supported · ⌘↵ to save</span>
              <div class="action-group">
                <button
                  class="btn btn-ghost btn-sm"
                  type="button"
                  onClick={() => close('edit')}
                  disabled={busy()}
                >
                  Cancel
                </button>
                <button
                  class="btn btn-primary btn-sm"
                  type="submit"
                  disabled={busy() || !draft().trim()}
                >
                  {busy() ? 'Saving…' : 'Save'}
                </button>
              </div>
            </div>
          </div>
        </form>
      </Show>

      <Show when={confirming()}>
        <div
          class="confirm-inline activity-comment-confirm"
          role="group"
          aria-label="Confirm comment removal"
          onKeyDown={handleConfirmKeyDown}
        >
          <span class="confirm-text">Remove this comment?</span>
          <button
            ref={cancelRemoveButton}
            type="button"
            class="btn btn-ghost btn-sm"
            onClick={() => close('remove')}
            disabled={busy()}
          >
            Cancel
          </button>
          <button
            type="button"
            class="btn btn-danger btn-sm"
            onClick={remove}
            disabled={busy()}
          >
            {busy() ? 'Removing…' : 'Confirm remove'}
          </button>
        </div>
      </Show>

      <Show when={error()}>
        <p id={errorId} class="field-error activity-comment-error" role="alert">
          {error()}
        </p>
      </Show>
    </li>
  );
}

export default function ActivityFeed(props) {
  const events = createMemo(() => (Array.isArray(props.activity) ? props.activity : []));

  return (
    <Show
      when={events().length > 0}
      fallback={<p class="muted-small">No activity recorded yet.</p>}
    >
      <ol class="activity-list">
        {/* Key rows by activity id: polling replaces every event object, and a
            reference-keyed row would remount and drop an open comment editor. */}
        <For each={events()} keyed={(event) => event.id ?? event}>
          {(event) => {
            // Only comment events carry a top-level body. task.created now also
            // records the task body at the top level, so gate on the type or it
            // would render every new task as a comment bubble.
            const body = () => (event().type === 'task.commented' ? commentBody(event().data) : '');
            const actor = () => asText(event().actor).trim() || 'system';
            const rawData = () =>
              typeof event().data === 'string' && event().data.trim() ? event().data : '';
            const summary = () => renderSummary(event());

            return (
              <Show
                when={body()}
                fallback={
                  <li class={`activity-event tone-${eventTone(event())}`}>
                    <span class="activity-event-dot" aria-hidden="true" />
                    <div class="activity-event-body">
                      <span class="activity-event-label">{eventTitle(event().type)}</span>
                      <Show when={summary()}>
                        <span class="activity-event-summary">{summary()}</span>
                      </Show>
                      <Show when={rawData()}>
                        <p class="activity-plain">{rawData()}</p>
                      </Show>
                    </div>
                    <span class="activity-event-meta">
                      {actor()} ·{' '}
                      <time
                        datetime={timeAttribute(event().timestamp)}
                        title={formatFullDateTime(event().timestamp)}
                      >
                        {formatDateTime(event().timestamp)}
                      </time>
                    </span>
                  </li>
                }
              >
                {(comment) => (
                  <CommentItem
                    event={event}
                    body={comment}
                    actor={actor}
                    onEdit={props.onEditComment}
                    onRemove={props.onRemoveComment}
                  />
                )}
              </Show>
            );
          }}
        </For>
      </ol>
    </Show>
  );
}
