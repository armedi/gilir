import { createSignal, createUniqueId } from 'solid-js';
import { command } from '../api';
import { clearDraft, draftKey, loadDraft, saveDraft } from '../drafts';
import { errorText } from '../utils';

const projectDraftKey = draftKey('new-project');

export default function ProjectForm(props) {
  const inputId = createUniqueId();
  const errorId = createUniqueId();
  const stored = loadDraft(projectDraftKey);
  const [name, setName] = createSignal(typeof stored === 'string' ? stored : '');
  const [busy, setBusy] = createSignal(false);
  const [error, setError] = createSignal(null);

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
          id={inputId}
          class="input"
          type="text"
          value={name()}
          onInput={(event) => updateName(event.currentTarget.value)}
          placeholder="Website redesign"
          autocomplete="off"
          autofocus={props.autofocus || undefined}
          disabled={busy()}
        />
      </div>
      {error() && (
        <p id={errorId} class="field-error" role="alert">
          {error()}
        </p>
      )}
      <div class="form-actions">
        <button class="btn btn-primary" type="submit" disabled={busy()}>
          {busy() ? 'Creating…' : 'Create project'}
        </button>
      </div>
    </form>
  );
}
