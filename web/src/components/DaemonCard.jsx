import { createEffect, createSignal, createUniqueId } from 'solid-js';
import { command } from '../api';
import { clearDraft, draftKey, loadDraft, saveDraft } from '../drafts';
import { errorText } from '../utils';
import { QueueState } from './Badges';

const concurrencyDraftKey = draftKey('concurrency');

export default function DaemonCard(props) {
  const inputId = createUniqueId();
  const storedDraft = loadDraft(concurrencyDraftKey);
  const hasStoredDraft = typeof storedDraft === 'string' && storedDraft !== '';
  const [concurrency, setConcurrency] = createSignal(hasStoredDraft ? storedDraft : '');
  const [dirty, setDirty] = createSignal(hasStoredDraft);
  const [pauseBusy, setPauseBusy] = createSignal(false);
  const [saveBusy, setSaveBusy] = createSignal(false);
  const [actionError, setActionError] = createSignal(null);

  createEffect(
    () => {
      const status = props.status;
      if (!status || dirty()) return;
      return status;
    },
    (status) => {
      if (!status) return;
      setConcurrency(status.max_concurrency == null ? '' : String(status.max_concurrency));
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
    const status = props.status;
    if (!status || pauseBusy()) return;
    const wasPaused = Boolean(status.paused);
    setPauseBusy(true);
    setActionError(null);
    try {
      await command('daemon.update', { paused: !wasPaused });
      await props.onRefresh();
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
      await props.onRefresh();
      props.notify('Concurrency updated');
    } catch (requestError) {
      setActionError(errorText(requestError));
    } finally {
      setSaveBusy(false);
    }
  }

  const active = () => {
    const value = props.status?.active_parents;
    return value == null ? '—' : String(value);
  };
  const maximum = () =>
    props.status?.max_concurrency == null ? '—' : String(props.status.max_concurrency);
  const paused = () => Boolean(props.status?.paused);

  return (
    <section class="panel queue-panel" aria-labelledby="queue-title">
      <div class="panel-inner">
        <div class="panel-header">
          <h2 id="queue-title" class="panel-title">
            Queue
          </h2>
          <QueueState paused={props.status ? paused() : null} />
        </div>

        {props.error && (
          <div class="inline-alert" role="alert">
            <span>{props.error}</span>
            <button class="btn btn-ghost btn-sm" type="button" onClick={props.onRefresh}>
              Retry
            </button>
          </div>
        )}

        <div class="queue-metric">
          <div class="queue-metric-label">Active OpenCode parent runs</div>
          <div class="queue-metric-value">
            {active()} <span>/ {maximum()}</span>
          </div>
        </div>

        <div class="queue-actions">
          <button
            class="btn btn-secondary btn-block"
            type="button"
            onClick={togglePause}
            disabled={!props.status || pauseBusy()}
          >
            {pauseBusy() ? 'Updating…' : paused() ? 'Resume queue' : 'Pause queue'}
          </button>
        </div>

        <form class="queue-concurrency" onSubmit={saveConcurrency}>
          <div class="field">
            <label class="field-label" for={inputId}>
              Max concurrency
            </label>
            <input
              id={inputId}
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
          <button class="btn btn-secondary" type="submit" disabled={saveBusy() || !dirty()}>
            {saveBusy() ? 'Saving…' : 'Save'}
          </button>
        </form>

        {actionError() && (
          <p class="field-error" role="alert">
            {actionError()}
          </p>
        )}

        {props.status?.watcher_error && (
          <div class="inline-alert warning" role="alert">
            <span>{props.status.watcher_error}</span>
          </div>
        )}
      </div>
    </section>
  );
}
