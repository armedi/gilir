import { For, Show, createMemo } from 'solid-js';
import Markdown from './Markdown';
import {
  asText,
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
  'task.comment_added': 'Comment',
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

  if (type === 'task.dependency_added' || type === 'task.dependency_removed') {
    const blockerId = data.blocker_id ?? data.id;
    if (blockerId != null) {
      return <span class="activity-inline-mono">#{String(blockerId)}</span>;
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

  return null;
}

export default function ActivityFeed(props) {
  const events = createMemo(() => (Array.isArray(props.activity) ? props.activity : []));

  return (
    <Show
      when={events().length > 0}
      fallback={<p class="muted-small">No activity recorded yet.</p>}
    >
      <ol class="activity-list">
        <For each={events()}>
          {(event) => {
            const body = () => commentBody(event.data);
            const actor = () => asText(event.actor).trim() || 'system';
            const rawData = () =>
              typeof event.data === 'string' && event.data.trim() ? event.data : '';
            const summary = () => renderSummary(event);

            return (
              <Show
                when={body()}
                fallback={
                  <li class={`activity-event tone-${eventTone(event)}`}>
                    <span class="activity-event-dot" aria-hidden="true" />
                    <div class="activity-event-body">
                      <span class="activity-event-label">{eventTitle(event.type)}</span>
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
                        datetime={timeAttribute(event.timestamp)}
                        title={formatFullDateTime(event.timestamp)}
                      >
                        {formatDateTime(event.timestamp)}
                      </time>
                    </span>
                  </li>
                }
              >
                {(comment) => (
                  <li class="activity-comment-item">
                    <div class="activity-comment-head">
                      <span class="activity-comment-actor">{actor()}</span>
                      <time
                        class="activity-comment-time"
                        datetime={timeAttribute(event.timestamp)}
                        title={formatFullDateTime(event.timestamp)}
                      >
                        {formatDateTime(event.timestamp)}
                      </time>
                    </div>
                    <Markdown source={comment()} />
                  </li>
                )}
              </Show>
            );
          }}
        </For>
      </ol>
    </Show>
  );
}
