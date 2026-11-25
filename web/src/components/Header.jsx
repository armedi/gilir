import { For, Show, createEffect, createSignal, createUniqueId } from 'solid-js';
import { command } from '../api';
import { clearDraft, draftKey, loadDraft, saveDraft } from '../drafts';
import { errorText } from '../utils';

const concurrencyDraftKey = draftKey('concurrency');

const VIEWS = [
  { id: 'tasks', label: 'Tasks' },
  { id: 'projects', label: 'Projects & Repos' },
];

// A watcher failure is an OpenCode problem; a failed daemon.status poll just
// means the queue state is unknown right now.
function stateOf(status) {
  if (!status) return 'unknown';
  if (status.watcher_error) return 'warning';
  return status.paused ? 'paused' : 'running';
}

function stateLabel(state) {
  if (state === 'warning') return 'OpenCode warning';
  if (state === 'paused') return 'Paused';
  if (state === 'running') return 'Running';
  return 'Unknown';
}

export default function Header(props) {
  const maxInputId = createUniqueId();
  const panelId = createUniqueId();
  let pillButton;
  let popover;

  const storedDraft = loadDraft(concurrencyDraftKey);
  const hasStoredDraft = typeof storedDraft === 'string' && storedDraft !== '';
  const [open, setOpen] = createSignal(false);
  const [concurrency, setConcurrency] = createSignal(hasStoredDraft ? storedDraft : '');
  const [dirty, setDirty] = createSignal(hasStoredDraft);
  const [pauseBusy, setPauseBusy] = createSignal(false);
  const [saveBusy, setSaveBusy] = createSignal(false);
  const [actionError, setActionError] = createSignal(null);

  const status = () => props.daemon;
  const state = () => stateOf(status());
  const activeCount = () => (status()?.active_parents == null ? '–' : String(status().active_parents));
  const maxCount = () =>
    status()?.max_concurrency == null ? '–' : String(status().max_concurrency);
  const warningText = () => props.daemonError || status()?.watcher_error || '';

  createEffect(
    () => {
      const current = status();
      if (!current || dirty()) return;
      return current;
    },
    (current) => {
      if (!current) return;
      setConcurrency(current.max_concurrency == null ? '' : String(current.max_concurrency));
    },
  );

  createEffect(
    () => open(),
    (isOpen) => {
      if (isOpen) return;
      setActionError(null);
    },
  );

  createEffect(
    () => open(),
    (isOpen) => {
      if (!isOpen) return;
      const onPointerDown = (event) => {
        if (popover?.contains(event.target) || pillButton?.contains(event.target)) return;
        setOpen(false);
      };
      const onKeyDown = (event) => {
        if (event.key !== 'Escape') return;
        setOpen(false);
        pillButton?.focus();
      };
      document.addEventListener('pointerdown', onPointerDown);
      document.addEventListener('keydown', onKeyDown);
      return () => {
        document.removeEventListener('pointerdown', onPointerDown);
        document.removeEventListener('keydown', onKeyDown);
      };
    },
  );

  function updateConcurrency(value) {
    setConcurrency(value);
    setDirty(true);
    setActionError(null);
    if (value) {
      saveDraft(concurrencyDraftKey, value);
    } else {
      clearDraft(concurrencyDraftKey);
    }
  }

  async function togglePause() {
    const current = status();
    if (!current || pauseBusy()) return;
    const wasPaused = Boolean(current.paused);
    setPauseBusy(true);
    setActionError(null);
    try {
      await command('daemon.update', { paused: !wasPaused });
      await props.onRefreshDaemon();
      props.notify(wasPaused ? 'Queue resumed' : 'Queue paused');
    } catch (requestError) {
      setActionError(errorText(requestError));
    } finally {
      setPauseBusy(false);
    }
  }

  async function saveConcurrency(event) {
    event.preventDefault();
    if (saveBusy()) return;
    const value = Number(concurrency());
    if (!Number.isInteger(value) || value < 1) {
      setActionError('Max concurrency must be a whole number of at least 1.');
      return;
    }
    setSaveBusy(true);
    setActionError(null);
    try {
      await command('daemon.update', { max_concurrency: value });
      clearDraft(concurrencyDraftKey);
      setDirty(false);
      await props.onRefreshDaemon();
      props.notify('Concurrency updated');
    } catch (requestError) {
      setActionError(errorText(requestError));
    } finally {
      setSaveBusy(false);
    }
  }

  return (
    <header class="app-header">
      <div class="header-inner">
        <div class="brand">
          <span class="brand-mark" aria-hidden="true">
            G
          </span>
          <h1 class="brand-name">Gilir</h1>
        </div>

        <nav class="view-tabs" aria-label="Views">
          <For each={VIEWS}>
            {(view) => (
              <button
                type="button"
                class={['view-tab', { active: props.view === view.id }]}
                aria-current={props.view === view.id ? 'page' : undefined}
                onClick={() => props.onViewChange(view.id)}
              >
                {view.label}
              </button>
            )}
          </For>
        </nav>

        <div class="header-actions">
          <div class="queue-pill-wrap">
            <button
              ref={pillButton}
              type="button"
              class={['queue-pill', state()]}
              aria-expanded={open()}
              aria-controls={panelId}
              aria-haspopup="dialog"
              title="Queue status"
              onClick={() => setOpen((value) => !value)}
            >
              <span class="queue-dot" aria-hidden="true" />
              <span class="queue-pill-state">{stateLabel(state())}</span>
              <span class="queue-pill-count" aria-hidden="true">
                {activeCount()}<span class="queue-pill-sep">/</span>{maxCount()}
              </span>
              <span class="sr-only">
                {`${activeCount()} of ${maxCount()} OpenCode parent runs active`}
              </span>
            </button>

            <Show when={open()}>
              <div
                ref={popover}
                id={panelId}
                class="queue-popover"
                role="dialog"
                aria-label="Queue controls"
              >
                <div class="queue-popover-head">
                  <span class="queue-popover-title">Queue</span>
                  <span class={['queue-state', state()]}>
                    <span class="queue-dot" aria-hidden="true" />
                    {stateLabel(state())}
                  </span>
                </div>

                <div class="queue-popover-metric">
                  <span class="queue-popover-metric-label">Active parent runs</span>
                  <span class="queue-popover-metric-value">
                    {activeCount()} <span>/ {maxCount()}</span>
                  </span>
                </div>

                <Show when={warningText()}>
                  <p class="queue-popover-warning">{warningText()}</p>
                </Show>

                <Show when={props.daemonError}>
                  <button class="btn btn-ghost btn-sm" type="button" onClick={props.onRefreshDaemon}>
                    Retry
                  </button>
                </Show>

                <button
                  class="btn btn-secondary btn-block"
                  type="button"
                  onClick={togglePause}
                  disabled={!status() || pauseBusy()}
                >
                  {pauseBusy()
                    ? 'Updating…'
                    : status()?.paused
                      ? 'Resume queue'
                      : 'Pause queue'}
                </button>

                <form class="queue-popover-form" onSubmit={saveConcurrency}>
                  <div class="field">
                    <label class="field-label" for={maxInputId}>
                      Max concurrency
                    </label>
                    <input
                      id={maxInputId}
                      class="input"
                      type="number"
                      min="1"
                      step="1"
                      inputmode="numeric"
                      value={concurrency()}
                      onInput={(event) => updateConcurrency(event.currentTarget.value)}
                      disabled={saveBusy()}
                    />
                  </div>
                  <button
                    class="btn btn-secondary"
                    type="submit"
                    disabled={saveBusy() || !dirty()}
                  >
                    {saveBusy() ? 'Saving…' : 'Save'}
                  </button>
                </form>

                <Show when={actionError()}>
                  <p class="field-error" role="alert">
                    {actionError()}
                  </p>
                </Show>
              </div>
            </Show>
          </div>

          <button class="btn btn-primary header-cta" type="button" onClick={props.onPrimaryAction}>
            {props.hasRepos ? '+ New task' : '+ Add repository'}
          </button>
        </div>
      </div>
    </header>
  );
}
