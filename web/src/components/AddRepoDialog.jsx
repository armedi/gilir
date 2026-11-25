import { For, Show, createSignal, createUniqueId } from 'solid-js';
import { command } from '../api';
import { clearDraft, draftKey, loadDraft, saveDraft } from '../drafts';
import { errorText } from '../utils';
import Modal from './Modal';

const repoDraftKey = draftKey('add-repo');

export default function AddRepoDialog(props) {
  const dialogTitleId = createUniqueId();
  const pathId = createUniqueId();
  const projectId = createUniqueId();
  const errorId = createUniqueId();
  let pathInput;

  const stored = loadDraft(repoDraftKey);
  const storedPath = typeof stored?.path === 'string' ? stored.path : '';
  const storedProject = typeof stored?.project_id === 'string' ? stored.project_id : '';
  const [path, setPath] = createSignal(storedPath);
  const [project, setProject] = createSignal(storedProject);
  const [busy, setBusy] = createSignal(false);
  const [error, setError] = createSignal(null);

  function persist(nextPath, nextProject) {
    if (!nextPath && !nextProject) {
      clearDraft(repoDraftKey);
      return;
    }
    saveDraft(repoDraftKey, { path: nextPath, project_id: nextProject });
  }

  function updatePath(value) {
    setPath(value);
    setError(null);
    persist(value, project());
  }

  function updateProject(value) {
    setProject(value);
    setError(null);
    persist(path(), value);
  }

  async function submit(event) {
    event.preventDefault();
    if (busy()) return;
    const trimmed = path().trim();
    if (!trimmed) {
      setError('Enter a local repository path.');
      pathInput?.focus();
      return;
    }
    const args = { path: trimmed };
    if (project()) args.project_id = Number(project());
    setBusy(true);
    setError(null);
    try {
      const repo = await command('repo.add', args);
      clearDraft(repoDraftKey);
      setPath('');
      setProject('');
      await props.onAdded?.(repo);
    } catch (requestError) {
      setError(errorText(requestError));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal
      open={props.open}
      class="modal-dialog"
      labelledby={dialogTitleId}
      onClose={props.onClose}
    >
      <div class="dialog-header">
        <h2 id={dialogTitleId}>Add repository</h2>
        <button
          class="icon-button"
          type="button"
          aria-label="Close add repository dialog"
          onClick={props.onClose}
        >
          ×
        </button>
      </div>
      <div class="dialog-body">
        <form class="stack" onSubmit={submit} aria-describedby={error() ? errorId : undefined}>
          <div class="field">
            <label class="field-label" for={pathId}>
              Repository path
            </label>
            <input
              ref={pathInput}
              id={pathId}
              class="input input-mono"
              type="text"
              value={path()}
              onInput={(event) => updatePath(event.currentTarget.value)}
              placeholder="/home/you/code/project"
              autocomplete="off"
              autocapitalize="off"
              spellcheck="false"
              aria-required="true"
              disabled={busy()}
            />
            <p class="field-hint">Use an absolute path to a Git repository on this machine.</p>
          </div>

          <div class="field">
            <label class="field-label" for={projectId}>
              Project
            </label>
            <select
              id={projectId}
              class="select"
              value={project()}
              onChange={(event) => updateProject(event.currentTarget.value)}
              disabled={busy()}
            >
              <option value="">Auto-create from repository name</option>
              <For each={props.projects}>
                {(item) => <option value={String(item.id)}>{item.name}</option>}
              </For>
            </select>
            <p class="field-hint">
              Auto-create names a new project after the repository folder.
            </p>
          </div>

          <Show when={error()}>
            <p id={errorId} class="field-error" role="alert">
              {error()}
            </p>
          </Show>

          <div class="form-actions">
            <button class="btn btn-secondary" type="button" onClick={props.onClose}>
              Cancel
            </button>
            <button class="btn btn-primary" type="submit" disabled={busy()}>
              {busy() ? 'Adding…' : 'Add repository'}
            </button>
          </div>
        </form>
      </div>
    </Modal>
  );
}
