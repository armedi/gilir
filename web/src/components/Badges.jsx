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
