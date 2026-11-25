import { KNOWN_PRIORITIES, KNOWN_STATUSES } from './constants';

export function knownStatus(value) {
  return KNOWN_STATUSES.has(value) ? value : 'neutral';
}

export function knownPriority(value) {
  return KNOWN_PRIORITIES.has(value) ? value : 'neutral';
}
