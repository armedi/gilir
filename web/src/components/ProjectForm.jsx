import { Show, createSignal, createUniqueId, onSettled } from 'solid-js';
import { command } from '../api';
import { clearDraft, draftKey, loadDraft, saveDraft } from '../drafts';
import { errorText } from '../utils';

const projectDraftKey = draftKey('new-project');

export default function ProjectForm(props) {
  const inputId = createUniqueId();
  const errorId = createUniqueId();
  let inputRef;
  const stored = loadDraft(projectDraftKey);
  const [name, setName] = createSignal(typeof stored === 'string' ? stored : '');
  const [busy, setBusy] = createSignal(false);
  const [error, setError] = createSignal(null);

  onSettled(() => {
    if (props.autofocus) {
      requestAnimationFrame(() => inputRef?.focus());
    }
  });

  function updateName(value) {
    setName(value);
    setError(null);
    if (value) {
      saveDraft(projectDraftKey, value);
    } else {
      clearDraft(projectDraftKey);
    }
  }

  async function submit(event) {
    event.preventDefault();
    if (busy()) return;
    const trimmed = name().trim();
    if (!trimmed) {
      setError('Enter a project name.');
      inputRef?.focus();
      return;
    }
    setBusy(true);
    setError(null);
    try {
      await command('project.create', { name: trimmed });
      clearDraft(projectDraftKey);
      setName('');
      await props.onCreated?.(trimmed);
    } catch (requestError) {
      setError(errorText(requestError));
    } finally {
      setBusy(false);
    }
  }

  return (
    <form class="stack" onSubmit={submit} aria-describedby={error() ? errorId : undefined}>
      <div class="field">
        <label class="field-label" for={inputId}>
          Project name
        </label>
        <input
          ref={inputRef}
          id={inputId}
          class="input"
          type="text"
          value={name()}
          onInput={(event) => updateName(event.currentTarget.value)}
          placeholder="Payments"
          autocomplete="off"
          disabled={busy()}
        />
      </div>
      <Show when={error()}>
        <p id={errorId} class="field-error" role="alert">
          {error()}
        </p>
      </Show>
      <div class="form-actions">
        <Show when={props.onCancel}>
          <button class="btn btn-secondary" type="button" onClick={props.onCancel}>
            Cancel
          </button>
        </Show>
        <button class="btn btn-primary" type="submit" disabled={busy()}>
          {busy() ? 'Creating…' : 'Create project'}
        </button>
      </div>
    </form>
  );
}
