import { createContext, useContext } from 'solid-js';

// The shell owns the dashboard's live state (queue, daemon, detail, panels).
// Route pages read it here so the polling effects live once, in App, and are
// not torn down and recreated as the URL changes.
export const ShellContext = createContext();

export function useShell() {
  return useContext(ShellContext);
}

// Client-side destinations. Keep in sync with staticPagePath in
// internal/localhttp/localhttp.go, which serves the entry point for these
// paths on a direct load or refresh.
export const PATHS = {
  queue: '/',
  newTask: '/tasks/new',
  projects: '/projects',
  addRepo: '/projects/repos/new',
};

const TASK_ID_PATTERN = /^GILIR-([1-9][0-9]{0,18})$/i;
const MAX_TASK_SEQ = BigInt('9223372036854775807');

// Mirrors internal/taskid.Parse: a case-insensitive GILIR- prefix followed by
// a positive decimal without leading zeros that fits in an int64.
export function isTaskId(value) {
  if (typeof value !== 'string') return false;
  const match = TASK_ID_PATTERN.exec(value);
  if (!match) return false;
  try {
    return BigInt(match[1]) <= MAX_TASK_SEQ;
  } catch {
    return false;
  }
}

export function canonicalTaskId(value) {
  return isTaskId(value) ? value.toUpperCase() : null;
}

export function taskPath(taskId) {
  return `/tasks/${encodeURIComponent(String(taskId))}`;
}
