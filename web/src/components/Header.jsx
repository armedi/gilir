import { For, Show, createMemo, createSignal } from 'solid-js';
import { command } from '../api';
import { errorText } from '../utils';

function LeftPanelIcon() {
  return (
    <svg width="16" height="16" viewBox="0 0 16 16" fill="none" aria-hidden="true">
      <rect
        x="1.75"
        y="2.25"
        width="12.5"
        height="11.5"
        rx="2"
        stroke="currentColor"
        stroke-width="1.35"
      />
      <path d="M5.75 2.5V13.5" stroke="currentColor" stroke-width="1.35" />
    </svg>
  );
}

function RightPanelIcon() {
  return (
    <svg width="16" height="16" viewBox="0 0 16 16" fill="none" aria-hidden="true">
      <rect
        x="1.75"
        y="2.25"
        width="12.5"
        height="11.5"
        rx="2"
        stroke="currentColor"
        stroke-width="1.35"
      />
      <path d="M10.25 2.5V13.5" stroke="currentColor" stroke-width="1.35" />
    </svg>
  );
}

function stateOf(status) {
  if (!status) return 'unknown';
  if (status.watcher_error) return 'warning';
  return status.paused ? 'paused' : 'running';
}

function stateLabel(state) {
  if (state === 'warning') return 'Warning';
  if (state === 'paused') return 'Paused';
  if (state === 'running') return 'Running';
  return 'Unknown';
}

export default function Header(props) {
  const [pauseBusy, setPauseBusy] = createSignal(false);
  const [concurrencyBusy, setConcurrencyBusy] = createSignal(false);

  const status = () => props.daemon;
  const state = () => stateOf(status());
  const activeNum = () =>
    typeof status()?.active_parents === 'number' ? status().active_parents : null;
  const maxNum = () =>
    typeof status()?.max_concurrency === 'number' ? status().max_concurrency : null;
  const activeCount = () => (activeNum() == null ? '-' : String(activeNum()));
  const maxCount = () => (maxNum() == null ? '-' : String(maxNum()));

  const slotIndicators = createMemo(() => {
    const max = maxNum();
    const active = activeNum();
    if (max == null || max < 1 || max > 8) return [];
    const filled = active == null ? 0 : Math.min(active, max);
    return Array.from({ length: max }, (_, index) => index < filled);
  });

  async function togglePause() {
    const current = status();
    if (!current || pauseBusy()) return;
    const wasPaused = Boolean(current.paused);
    setPauseBusy(true);
    try {
      await command('daemon.update', { paused: !wasPaused });
      await props.onRefreshDaemon();
      props.notify(wasPaused ? 'Queue resumed' : 'Queue paused');
    } catch (requestError) {
      props.notify(errorText(requestError));
    } finally {
      setPauseBusy(false);
    }
  }

  async function stepConcurrency(delta) {
    const current = maxNum();
    if (current == null || concurrencyBusy()) return;
    const next = current + delta;
    if (!Number.isInteger(next) || next < 1) return;
    setConcurrencyBusy(true);
    try {
      await command('daemon.update', { max_concurrency: next });
      await props.onRefreshDaemon();
      props.notify(`Max concurrency set to ${next}`);
    } catch (requestError) {
      props.notify(errorText(requestError));
    } finally {
      setConcurrencyBusy(false);
    }
  }

  return (
    <header class="app-header">
      <div class="header-inner">
        <div class="header-left">
          <button
            type="button"
            class={['panel-icon-btn desktop-panel-toggle', { active: props.leftOpen }]}
            aria-label={props.leftOpen ? 'Hide queue panel' : 'Show queue panel'}
            aria-pressed={props.leftOpen ? 'true' : 'false'}
            title={props.leftOpen ? 'Hide queue panel' : 'Show queue panel'}
            onClick={props.onToggleLeft}
          >
            <LeftPanelIcon />
          </button>

          <span class="header-divider" aria-hidden="true" />

          <div class="brand">
            <span class="brand-mark" aria-hidden="true">
              <svg width="14" height="14" viewBox="0 0 16 16" fill="none">
                <path
                  d="M2.5 5.25H11.5L9 2.75M13.5 10.75H4.5L7 13.25"
                  stroke="currentColor"
                  stroke-width="1.7"
                  stroke-linecap="round"
                  stroke-linejoin="round"
                />
              </svg>
            </span>
            <h1 class="brand-name">Gilir</h1>
          </div>
        </div>

        <div class="header-actions">
          <Show when={props.notice}>
            <div class="header-notice" role="status" aria-live="polite">
              <span class="header-notice-dot" aria-hidden="true" />
              <span>{props.notice}</span>
            </div>
          </Show>

          <div
            class={['daemon-strip', state()]}
            role="group"
            aria-label="Dispatch daemon controls"
          >
            <span class="daemon-segment daemon-state">
              <span class="daemon-dot" aria-hidden="true" />
              <span>{stateLabel(state())}</span>
            </span>

            <span
              class="daemon-segment daemon-meter"
              title="Active OpenCode parent runs / max concurrency"
            >
              <Show when={slotIndicators().length > 0}>
                <span class="daemon-slots" aria-hidden="true">
                  <For each={slotIndicators()}>
                    {(busy) => <span class={['daemon-slot', { busy }]} />}
                  </For>
                </span>
              </Show>
              <span>
                {activeCount()}/{maxCount()}
              </span>
              <span class="sr-only">
                {`${activeCount()} of ${maxCount()} OpenCode parent runs active`}
              </span>
            </span>

            <div class="stepper-group" role="group" aria-label="Max concurrency">
              <button
                type="button"
                class="stepper-btn"
                aria-label="Decrease max concurrency"
                title="Decrease max concurrency"
                onClick={() => stepConcurrency(-1)}
                disabled={maxNum() == null || maxNum() <= 1 || concurrencyBusy()}
              >
                <svg width="11" height="11" viewBox="0 0 12 12" fill="none" aria-hidden="true">
                  <path
                    d="M2.5 6H9.5"
                    stroke="currentColor"
                    stroke-width="1.5"
                    stroke-linecap="round"
                  />
                </svg>
              </button>
              <button
                type="button"
                class="stepper-btn"
                aria-label="Increase max concurrency"
                title="Increase max concurrency"
                onClick={() => stepConcurrency(1)}
                disabled={maxNum() == null || concurrencyBusy()}
              >
                <svg width="11" height="11" viewBox="0 0 12 12" fill="none" aria-hidden="true">
                  <path
                    d="M6 2.5V9.5M2.5 6H9.5"
                    stroke="currentColor"
                    stroke-width="1.5"
                    stroke-linecap="round"
                  />
                </svg>
              </button>
            </div>

            <button
              type="button"
              class="daemon-toggle-btn"
              onClick={togglePause}
              disabled={!status() || pauseBusy()}
            >
              {pauseBusy() ? '…' : status()?.paused ? 'Resume' : 'Pause'}
            </button>
          </div>

          <a class="btn btn-primary header-cta" href={props.primaryHref}>
            {props.hasRepos ? 'New task' : 'Add repository'}
          </a>

          <Show when={props.canShowRight}>
            <span class="header-divider" aria-hidden="true" />
            <button
              type="button"
              class={['panel-icon-btn desktop-panel-toggle', { active: props.rightOpen }]}
              aria-label={props.rightOpen ? 'Hide inspector panel' : 'Show inspector panel'}
              aria-pressed={props.rightOpen ? 'true' : 'false'}
              title={props.rightOpen ? 'Hide inspector panel' : 'Show inspector panel'}
              onClick={props.onToggleRight}
            >
              <RightPanelIcon />
            </button>
          </Show>
        </div>
      </div>
    </header>
  );
}
