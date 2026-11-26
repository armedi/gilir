import { PRIORITY_LABELS, STATUS_LABELS, TASK_GROUPS } from './constants';

const dateFormatter = new Intl.DateTimeFormat(undefined, {
  month: 'short',
  day: 'numeric',
  hour: '2-digit',
  minute: '2-digit',
});

const fullDateFormatter = new Intl.DateTimeFormat(undefined, {
  dateStyle: 'medium',
  timeStyle: 'medium',
});

const relativeFormatter =
  typeof Intl.RelativeTimeFormat === 'function'
    ? new Intl.RelativeTimeFormat(undefined, { numeric: 'auto' })
    : null;

const RELATIVE_UNITS = [
  ['year', 31536000000],
  ['month', 2592000000],
  ['week', 604800000],
  ['day', 86400000],
  ['hour', 3600000],
  ['minute', 60000],
];

const PRIORITY_RANK = { urgent: 0, high: 1, medium: 2, low: 3 };

export function asArray(value) {
  return Array.isArray(value) ? value : [];
}

export function asText(value) {
  return typeof value === 'string' ? value : value == null ? '' : String(value);
}

export function isAbortError(error) {
  return error?.name === 'AbortError';
}

export function errorText(error) {
  if (error && typeof error.message === 'string' && error.message.trim()) {
    return error.message;
  }
  return 'Something went wrong.';
}

export function parseTime(value) {
  if (value == null || value === '') return NaN;
  const time = Date.parse(value);
  return Number.isNaN(time) ? NaN : time;
}

export function formatDateTime(value) {
  const time = parseTime(value);
  return Number.isNaN(time) ? '-' : dateFormatter.format(time);
}

export function formatFullDateTime(value) {
  const time = parseTime(value);
  return Number.isNaN(time) ? 'Unknown time' : fullDateFormatter.format(time);
}

export function formatRelativeTime(value, now = Date.now()) {
  const time = parseTime(value);
  if (Number.isNaN(time)) return '-';
  const delta = now - time;
  if (delta < 45000) return 'just now';
  for (const [unit, size] of RELATIVE_UNITS) {
    if (delta >= size) {
      const amount = Math.floor(delta / size);
      return relativeFormatter
        ? relativeFormatter.format(-amount, unit)
        : `${amount}${unit.charAt(0)} ago`;
    }
  }
  return 'just now';
}

export function timeAttribute(value) {
  const time = parseTime(value);
  return Number.isNaN(time) ? undefined : new Date(time).toISOString();
}

export function basename(path) {
  const text = asText(path).replace(/[\\/]+$/, '');
  if (!text) return '';
  const parts = text.split(/[\\/]/);
  return parts[parts.length - 1] || text;
}

export function humanize(value) {
  const text = asText(value).replace(/[._-]+/g, ' ').trim();
  if (!text) return 'Unknown';
  return text.charAt(0).toUpperCase() + text.slice(1);
}

export function statusLabel(value) {
  return STATUS_LABELS[value] || humanize(value || 'unknown');
}

export function priorityLabel(value) {
  return PRIORITY_LABELS[value] || humanize(value || 'unknown');
}

export function normalizeTask(task) {
  if (!task || typeof task !== 'object') return null;
  return {
    ...task,
    description: typeof task.description === 'string' ? task.description : '',
    markings: asArray(task.markings).filter((marking) => typeof marking === 'string'),
  };
}

export function normalizeDetail(data) {
  if (!data || typeof data !== 'object' || !normalizeTask(data.task)) return null;
  return {
    task: normalizeTask(data.task),
    blockers: asArray(data.blockers),
    sessions: asArray(data.sessions).filter(Boolean),
    activity: asArray(data.activity).filter(Boolean),
  };
}

export function repoProjectIds(repo) {
  return asArray(repo?.project_ids).map((id) => String(id));
}

export function repoLinkedTo(repo, projectId) {
  if (projectId == null || projectId === '') return true;
  return repoProjectIds(repo).includes(String(projectId));
}

export function reposForProject(repos, projectId) {
  if (projectId == null || projectId === '') return asArray(repos);
  return asArray(repos).filter((repo) => repoLinkedTo(repo, projectId));
}

// Builds the Repository filter options. A selected project filters to its
// linked repos; with no project selected, repos are grouped by project so a
// multi-project repo appears once under each project it is linked to.
export function repoGroups(projects, repos, projectId) {
  if (projectId != null && projectId !== '') {
    return {
      grouped: false,
      groups: [{ key: String(projectId), label: '', repos: reposForProject(repos, projectId) }],
    };
  }
  const groups = [];
  const seen = new Set();
  for (const project of asArray(projects)) {
    const linked = asArray(repos).filter((repo) => repoLinkedTo(repo, project.id));
    if (linked.length === 0) continue;
    for (const repo of linked) seen.add(String(repo.id));
    groups.push({ key: `project-${project.id}`, label: project.name, repos: linked });
  }
  const orphans = asArray(repos).filter((repo) => !seen.has(String(repo.id)));
  if (orphans.length > 0) {
    groups.push({ key: 'unlinked', label: 'Unlinked repositories', repos: orphans });
  }
  return { grouped: true, groups };
}

function markingsKey(task) {
  return asArray(task?.markings).join('\u0000');
}

// Keeps row identity stable across polls: unchanged tasks reuse the previous
// object so the list does not tear down focused rows every refresh.
export function mergeTasks(previous, next) {
  if (!previous.length) return next;
  const known = new Map(previous.map((task) => [String(task.id), task]));
  return next.map((task) => {
    const existing = known.get(String(task.id));
    if (existing === task) return task;
    if (
      existing &&
      existing.title === task.title &&
      existing.status === task.status &&
      existing.priority === task.priority &&
      existing.repo_id === task.repo_id &&
      existing.project_id === task.project_id &&
      existing.updated_at === task.updated_at &&
      markingsKey(existing) === markingsKey(task)
    ) {
      return existing;
    }
    return task;
  });
}

function compareGroupTasks(left, right) {
  const leftRank = PRIORITY_RANK[left.priority] ?? 4;
  const rightRank = PRIORITY_RANK[right.priority] ?? 4;
  if (leftRank !== rightRank) return leftRank - rightRank;
  const leftTime = parseTime(left.updated_at);
  const rightTime = parseTime(right.updated_at);
  const leftValue = Number.isNaN(leftTime) ? 0 : leftTime;
  const rightValue = Number.isNaN(rightTime) ? 0 : rightTime;
  if (leftValue !== rightValue) return rightValue - leftValue;
  return String(right.id).localeCompare(String(left.id), undefined, { numeric: true });
}

// Buckets tasks into urgency-ordered status sections, dropping empty ones.
export function groupTasks(tasks) {
  const buckets = new Map();
  for (const task of asArray(tasks)) {
    const bucket = buckets.get(task.status);
    if (bucket) {
      bucket.push(task);
    } else if (TASK_GROUPS.some((group) => group.status === task.status)) {
      buckets.set(task.status, [task]);
    }
  }
  return TASK_GROUPS.filter((group) => buckets.has(group.status)).map((group) => ({
    status: group.status,
    label: group.label,
    tasks: buckets.get(group.status).sort(compareGroupTasks),
  }));
}

export function sortTasks(tasks) {
  return [...tasks].sort(compareGroupTasks);
}

export function safeHttpUrl(value) {
  if (typeof value !== 'string' || !value.trim()) return null;
  try {
    const url = new URL(value, window.location.href);
    if (url.protocol === 'http:' || url.protocol === 'https:') return url.href;
  } catch {
    return null;
  }
  return null;
}

export function uniqueMarkings(tasks) {
  const markings = new Set();
  for (const task of tasks) {
    for (const marking of asArray(task?.markings)) {
      const text = asText(marking).trim();
      if (text) markings.add(text);
    }
  }
  return [...markings].sort((left, right) => left.localeCompare(right));
}
