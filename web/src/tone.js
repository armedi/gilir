import { KNOWN_PRIORITIES, KNOWN_STATUSES } from './constants';

export function knownStatus(value) {
  return KNOWN_STATUSES.has(value) ? value : 'neutral';
}

export function knownPriority(value) {
  return KNOWN_PRIORITIES.has(value) ? value : 'neutral';
}

export function markingTone(value) {
  if (value === 'error') return 'error';
  if (value === 'changes-requested') return 'changes';
  if (value === 'blocked') return 'blocked';
  return 'custom';
}

// blocked reports whether a task carries the blocked marking, which keeps it
// out of dispatch until someone unblocks it.
export function isBlocked(task) {
  const markings = Array.isArray(task?.markings) ? task.markings : [];
  return markings.includes('blocked');
}
