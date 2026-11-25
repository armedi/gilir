import { PRIORITY_LABELS, STATUS_LABELS } from './constants';

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
  return Number.isNaN(time) ? '—' : dateFormatter.format(time);
}

export function formatFullDateTime(value) {
  const time = parseTime(value);
  return Number.isNaN(time) ? 'Unknown time' : fullDateFormatter.format(time);
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
    markings: asArray(task.markings),
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

export function sortTasks(tasks) {
  return [...tasks].sort((left, right) => {
    const leftTime = parseTime(left?.updated_at ?? left?.created_at);
    const rightTime = parseTime(right?.updated_at ?? right?.created_at);
    const leftValue = Number.isNaN(leftTime) ? 0 : leftTime;
    const rightValue = Number.isNaN(rightTime) ? 0 : rightTime;
    if (leftValue !== rightValue) return rightValue - leftValue;
    return String(right?.id ?? '').localeCompare(String(left?.id ?? ''), undefined, {
      numeric: true,
    });
  });
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
