import { createEffect, createSignal, createUniqueId } from 'solid-js';
import { command } from '../api';
import { clearDraft, draftKey, loadDraft, saveDraft } from '../drafts';
import { errorText } from '../utils';

export default function RepoForm(props) {
  const inputId = createUniqueId();
  const errorId = createUniqueId();
  const [path, setPath] = createSignal('');
  const [busy, setBusy] = createSignal(false);
  const [error, setError] = createSignal(null);
  let activeProject;

  createEffect(
    () => props.projectId,
    (projectId) => {
      if (projectId == null) {
        activeProject = null;
        setPath('');
        return;
      }
      if (String(projectId) === String(activeProject)) return;
      activeProject = projectId;
      const stored = loadDraft(draftKey('repo-path', projectId));
      setPath(typeof stored === 'string' ? stored : '');
      setError(null);
    },
  );

  function updatePath(value) {
    setPath(value);
    setError(null);
    if (props.projectId == null) return;
    const key = draftKey('repo-path', props.projectId);
    if (value) {
      saveDraft(key, value);
    } else {
      clearDraft(key);
    }
  }

  async function submit(event) {
    event.preventDefault();
    if (busy() || props.projectId == null) return;
    const trimmed = path().trim();
    if (!trimmed) {
      setError('Enter a local repository path.');
      return;
    }
    const projectId = props.projectId;
    setBusy(true);
    setError(null);
    try {
      await command('repo.add', { project_id: projectId, path: trimmed });
      clearDraft(draftKey('repo-path', projectId));
      setPath('');
      await props.onAdded?.();
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
          Repository path
        </label>
        <input
          id={inputId}
          class="input input-mono"
          type="text"
          value={path()}
          onInput={(event) => updatePath(event.currentTarget.value)}
          placeholder="/home/you/code/project"
          autocomplete="off"
          autocapitalize="off"
          spellcheck={false}
          disabled={busy()}
        />
      </div>
      <p class="field-hint">Use an absolute path to a Git repository on this machine.</p>
      {error() && (
        <p id={errorId} class="field-error" role="alert">
          {error()}
        </p>
      )}
      <div class="form-actions">
        <button class="btn btn-secondary" type="submit" disabled={busy()}>
          {busy() ? 'Adding…' : 'Add repository'}
        </button>
      </div>
    </form>
  );
}
