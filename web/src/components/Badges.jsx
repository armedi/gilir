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

export function QueueState(props) {
  const state = () =>
    props.paused == null ? 'unknown' : props.paused ? 'paused' : 'running';
  return (
    <span class={`queue-state ${state()}`}>
      <span class="queue-dot" aria-hidden="true" />
      {props.paused == null
        ? 'Queue unknown'
        : props.paused
          ? 'Queue paused'
          : 'Queue running'}
    </span>
  );
}
