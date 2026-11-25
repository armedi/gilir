import { knownPriority, knownStatus } from '../tone';
import { priorityLabel, statusLabel } from '../utils';

export function StatusBadge(props) {
  return (
    <span class={`badge status-${knownStatus(props.value)}`}>{statusLabel(props.value)}</span>
  );
}

export function PriorityBadge(props) {
  return (
    <span class={`badge priority-${knownPriority(props.value)}`}>
      {priorityLabel(props.value)}
    </span>
  );
}
