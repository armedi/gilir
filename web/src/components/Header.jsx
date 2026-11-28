import { For, Show, createMemo, createSignal } from 'solid-js';
import { command } from '../api';
import { errorText } from '../utils';

function BrandMark() {
  return (
    <svg width="24" height="24" viewBox="0 0 64 64" fill="none" aria-hidden="true">
      <defs>
        <linearGradient
          id="gilir-header-mark-bg"
          x1="8"
          y1="2"
          x2="56"
          y2="62"
          gradientUnits="userSpaceOnUse"
        >
          <stop offset="0%" stop-color="#14805e" />
          <stop offset="55%" stop-color="#0f6b4e" />
          <stop offset="100%" stop-color="#094c37" />
        </linearGradient>
      </defs>
      <rect width="64" height="64" rx="15" fill="url(#gilir-header-mark-bg)" />
      <rect
        x="0.75"
        y="0.75"
        width="62.5"
        height="62.5"
        rx="14.25"
        fill="none"
        stroke="#ffffff"
        stroke-opacity="0.12"
        stroke-width="1.5"
      />
      <path
        d="M24 13H49C50.1046 13 51 13.8954 51 15V19C51 20.1046 50.1046 21 49 21H24C22.3431 21 21 22.3431 21 24V40C21 41.6569 22.3431 43 24 43H32.6L30.2 51H24C17.9249 51 13 46.0751 13 40V24C13 17.9249 17.9249 13 24 13Z"
        fill="#ffffff"
      />
      <path
        d="M31.4 28H47C49.2091 28 51 29.7909 51 32V40C51 46.0751 46.0751 51 40 51H35.2L37.6 43H40C41.6569 43 43 41.6569 43 40V37.5C43 36.6716 42.3284 36 41.5 36H29L31.4 28Z"
        fill="#86efc7"
      />
    </svg>
  );
}

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

  const status = () => props.service;
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
      await command('service.update', { paused: !wasPaused });
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
      await command('service.update', { max_concurrency: next });
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
              <BrandMark />
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
            class={['service-strip', state()]}
            role="group"
            aria-label="Dispatch service controls"
          >
            <span class="service-segment service-state">
              <span class="service-dot" aria-hidden="true" />
              <span>{stateLabel(state())}</span>
            </span>

            <span
              class="service-segment service-meter"
              title="Active OpenCode parent runs / max concurrency"
            >
              <Show when={slotIndicators().length > 0}>
                <span class="service-slots" aria-hidden="true">
                  <For each={slotIndicators()}>
                    {(busy) => <span class={['service-slot', { busy }]} />}
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
              class="service-toggle-btn"
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
