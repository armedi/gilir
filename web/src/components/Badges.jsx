import { knownPriority, knownStatus } from '../tone';
import { priorityLabel, statusLabel } from '../utils';

export function StatusBadge(props) {
  return (
    <span class={`semantic-tag semantic-status-${knownStatus(props.value)}`}>
      <span class="semantic-dot" aria-hidden="true" />
      {statusLabel(props.value)}
    </span>
  );
}

export function PriorityBadge(props) {
  return (
    <span class={`semantic-tag semantic-priority-${knownPriority(props.value)}`}>
      {priorityLabel(props.value)}
    </span>
  );
}

// BlockedBadge marks a task that is deliberately paused, distinct from a
// status badge because the task keeps its status while blocked.
export function BlockedBadge() {
  return (
    <span class="semantic-tag semantic-blocked">
      <span class="semantic-dot" aria-hidden="true" />
      Blocked
    </span>
  );
}
