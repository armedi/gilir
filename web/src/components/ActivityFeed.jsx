import { For, Show, createMemo } from 'solid-js';
import Markdown from './Markdown';
import { PriorityBadge, StatusBadge } from './Badges';
import {
  asText,
  formatDateTime,
  formatFullDateTime,
  humanize,
  safeHttpUrl,
  timeAttribute,
} from '../utils';

const EVENT_TITLES = {
  'task.created': 'Task created',
  'task.updated': 'Task updated',
  'task.claimed': 'Task claimed',
  'task.status_changed': 'Status changed',
  'task.priority_changed': 'Priority changed',
  'task.marking_changed': 'Markings updated',
  'task.comment_added': 'Comment added',
  'task.dependency_added': 'Blocker added',
  'task.dependency_removed': 'Blocker removed',
  'task.review_approved': 'Task approved',
  'task.review_changes_requested': 'Changes requested',
  'session.linked': 'OpenCode session linked',
  'session.outcome': 'OpenCode session result',
};

const META_KEYS = new Set([
  'id',
  'type',
  'actor',
  'task_id',
  'project_id',
  'timestamp',
  'created_at',
]);

function eventTitle(value) {
  const raw = asText(value).toLowerCase();
  if (EVENT_TITLES[raw]) return EVENT_TITLES[raw];
  const dotted = raw.replace(/_/g, '.');
  if (EVENT_TITLES[dotted]) return EVENT_TITLES[dotted];
  return humanize(raw || 'activity');
}

function isStatusKey(key) {
  return key === 'status' || key.endsWith('_status');
}

function isPriorityKey(key) {
  return key === 'priority' || key.endsWith('_priority');
}

function isUrlKey(key) {
  return key === 'url' || key.endsWith('_url');
}

function commentBody(data) {
  if (!data || typeof data !== 'object') return '';
  for (const key of ['body', 'comment', 'message', 'content']) {
    if (typeof data[key] === 'string' && data[key].trim()) return data[key];
  }
  return '';
}

function detailEntries(data, body) {
  if (!data || typeof data !== 'object' || Array.isArray(data)) return [];
  const ignored = new Set(META_KEYS);
  if (body) {
    ignored.add('body');
    ignored.add('comment');
    ignored.add('message');
    ignored.add('content');
  }
  return Object.entries(data).filter(([key]) => !ignored.has(key));
}

function plainValue(value) {
  if (value == null) return '—';
  if (typeof value === 'boolean') return value ? 'Yes' : 'No';
  if (Array.isArray(value)) {
    if (value.length === 0) return '—';
    return value.map((item) => (typeof item === 'object' ? JSON.stringify(item) : String(item))).join(', ');
  }
  if (typeof value === 'object') {
    try {
      return JSON.stringify(value);
    } catch {
      return '[unreadable]';
    }
  }
  return String(value);
}

function renderValue(key, value) {
  if (isStatusKey(key) && typeof value === 'string') {
    return <StatusBadge value={value} />;
  }
  if (isPriorityKey(key) && typeof value === 'string') {
    return <PriorityBadge value={value} />;
  }
  if (isUrlKey(key) && typeof value === 'string') {
    const href = safeHttpUrl(value);
    if (href) {
      return (
        <a href={href} target="_blank" rel="noopener noreferrer">
          {value}
        </a>
      );
    }
    return <code>{value}</code>;
  }
  return plainValue(value);
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
            const entries = () => detailEntries(event.data, body());
            const actor = () => asText(event.actor).trim() || 'system';
            const rawData = () =>
              typeof event.data === 'string' && event.data.trim() ? event.data : '';
            return (
              <li class="activity-item" classList={{ 'is-comment': Boolean(body()) }}>
                <div class="activity-head">
                  <span class="activity-title">{eventTitle(event.type)}</span>
                  <span class="activity-meta">
                    by <span class="activity-actor">{actor()}</span>
                    <span aria-hidden="true"> · </span>
                    <time
                      datetime={timeAttribute(event.timestamp)}
                      title={formatFullDateTime(event.timestamp)}
                    >
                      {formatDateTime(event.timestamp)}
                    </time>
                  </span>
                </div>
                <Show when={body()}>
                  {(comment) => (
                    <div class="activity-comment">
                      <Markdown source={comment()} />
                    </div>
                  )}
                </Show>
                <Show when={rawData()}>
                  <p class="activity-plain">{rawData()}</p>
                </Show>
                <Show when={entries().length > 0}>
                  <dl class="activity-data">
                    <For each={entries()}>
                      {([key, value]) => (
                        <div>
                          <dt>{humanize(key)}</dt>
                          <dd>{renderValue(key, value)}</dd>
                        </div>
                      )}
                    </For>
                  </dl>
                </Show>
              </li>
            );
          }}
        </For>
      </ol>
    </Show>
  );
}
