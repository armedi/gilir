export const STATUSES = ['backlog', 'todo', 'in-progress', 'in-review', 'done'];

export const STATUS_LABELS = {
  backlog: 'Backlog',
  todo: 'Todo',
  'in-progress': 'In progress',
  'in-review': 'In review',
  done: 'Done',
};

// Queue groups render most urgent first.
export const TASK_GROUPS = [
  { status: 'in-review', label: 'In Review' },
  { status: 'in-progress', label: 'In Progress' },
  { status: 'todo', label: 'Todo' },
  { status: 'backlog', label: 'Backlog' },
  { status: 'done', label: 'Done' },
];

export const PRIORITIES = ['urgent', 'high', 'medium', 'low'];

export const PRIORITY_LABELS = {
  urgent: 'Urgent',
  high: 'High',
  medium: 'Medium',
  low: 'Low',
};

export const POLL_INTERVAL = 2000;

export const KNOWN_STATUSES = new Set(STATUSES);
export const KNOWN_PRIORITIES = new Set(PRIORITIES);
