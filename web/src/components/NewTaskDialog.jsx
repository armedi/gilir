import { For, Show, createEffect, createSignal, createUniqueId, untrack } from 'solid-js';
import { createStore } from 'solid-js/store';
import { command } from '../api';
import { PRIORITIES, PRIORITY_LABELS, STATUSES, STATUS_LABELS } from '../constants';
import { clearDraft, draftKey, loadDraft, saveDraft } from '../drafts';
import { errorText } from '../utils';
import Modal from './Modal';

const EMPTY_FORM = {
  title: '',
  description: '',
  status: 'todo',
  priority: 'medium',
  repo_id: '',
};

export default function NewTaskDialog(props) {
  const dialogTitleId = createUniqueId();
  const titleId = createUniqueId();
  const descriptionId = createUniqueId();
  const statusId = createUniqueId();
  const priorityId = createUniqueId();
  const repoId = createUniqueId();
  const errorId = createUniqueId();
  let titleInput;
  let lastOpen = false;
  let lastProject = null;
  let activeKey = null;

  const [form, setForm] = createStore(EMPTY_FORM);
  const [busy, setBusy] = createSignal(false);
  const [error, setError] = createSignal(null);

  function initialize() {
    activeKey = draftKey('new-task', props.projectId);
    const stored = loadDraft(activeKey);
    const defaults = untrack(() => props.defaults);
    const repos = untrack(() => props.repos);
    const next = {
      title: '',
      description: '',
      status: defaults?.status || 'todo',
      priority: defaults?.priority || 'medium',
      repo_id: defaults?.repo_id || '',
    };
    if (stored && typeof stored === 'object') {
      if (typeof stored.title === 'string') next.title = stored.title;
      if (typeof stored.description === 'string') next.description = stored.description;
      if (STATUSES.includes(stored.status)) next.status = stored.status;
      if (PRIORITIES.includes(stored.priority)) next.priority = stored.priority;
      if (stored.repo_id != null) next.repo_id = String(stored.repo_id);
    }
    if (!repos.some((repo) => String(repo.id) === String(next.repo_id))) {
      next.repo_id = repos[0] ? String(repos[0].id) : '';
    }
    setForm(next);
    setError(null);
    setBusy(false);
  }

  createEffect(() => {
    const open = props.open;
    const projectId = props.projectId;
    if (!open) {
      lastOpen = false;
      return;
    }
    if (!lastOpen || String(projectId) !== String(lastProject)) {
      lastOpen = true;
      lastProject = projectId;
      initialize();
    }
  });

  createEffect(() => {
    if (!props.open) return;
    requestAnimationFrame(() => {
      if (props.open && titleInput) titleInput.focus();
    });
  });

  function update(field, value) {
    setForm(field, value);
    setError(null);
    if (activeKey) saveDraft(activeKey, { ...form });
  }

  async function submit(event) {
    event.preventDefault();
    if (busy()) return;
    const title = form.title.trim();
    if (!title) {
      setError('Enter a task title.');
      titleInput?.focus();
      return;
    }
    const repo = props.repos.find((item) => String(item.id) === String(form.repo_id));
    if (!repo) {
      setError('Choose a repository.');
      return;
    }
    const args = {
      project_id: props.projectId,
      repo_id: repo.id,
      title,
      status: form.status,
      priority: form.priority,
    };
    if (form.description.trim()) args.description = form.description;
    setBusy(true);
    setError(null);
    try {
      await command('task.create', args);
      if (activeKey) clearDraft(activeKey);
      activeKey = null;
      setForm(EMPTY_FORM);
      await props.onCreated?.();
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
        <h2 id={dialogTitleId}>New task</h2>
        <button
          class="icon-button"
          type="button"
          aria-label="Close new task dialog"
          onClick={props.onClose}
        >
          ×
        </button>
      </div>
      <div class="dialog-body">
        <form class="stack" onSubmit={submit} aria-describedby={error() ? errorId : undefined}>
          <div class="field">
            <label class="field-label" for={titleId}>
              Title
            </label>
            <input
              ref={titleInput}
              id={titleId}
              class="input"
              type="text"
              value={form.title}
              onInput={(event) => update('title', event.currentTarget.value)}
              placeholder="Implement retry handling"
              autocomplete="off"
              aria-required="true"
              disabled={busy()}
            />
          </div>

          <div class="field">
            <label class="field-label" for={descriptionId}>
              Description (Markdown)
            </label>
            <textarea
              id={descriptionId}
              class="textarea"
              rows="5"
              value={form.description}
              onInput={(event) => update('description', event.currentTarget.value)}
              disabled={busy()}
            />
            <p class="field-hint">Markdown is supported.</p>
          </div>

          <div class="field-row">
            <div class="field">
              <label class="field-label" for={statusId}>
                Status
              </label>
              <select
                id={statusId}
                class="select"
                value={form.status}
                onChange={(event) => update('status', event.currentTarget.value)}
                disabled={busy()}
              >
                <For each={STATUSES}>
                  {(status) => <option value={status}>{STATUS_LABELS[status]}</option>}
                </For>
              </select>
            </div>
            <div class="field">
              <label class="field-label" for={priorityId}>
                Priority
              </label>
              <select
                id={priorityId}
                class="select"
                value={form.priority}
                onChange={(event) => update('priority', event.currentTarget.value)}
                disabled={busy()}
              >
                <For each={PRIORITIES}>
                  {(priority) => <option value={priority}>{PRIORITY_LABELS[priority]}</option>}
                </For>
              </select>
            </div>
          </div>

          <div class="field">
            <label class="field-label" for={repoId}>
              Repository
            </label>
            <Show
              when={props.repos.length > 0}
              fallback={<p class="field-hint">Add a repository before creating a task.</p>}
            >
              <select
                id={repoId}
                class="select"
                value={form.repo_id}
                onChange={(event) => update('repo_id', event.currentTarget.value)}
                disabled={busy()}
                aria-required="true"
              >
                <For each={props.repos}>
                  {(repo) => <option value={String(repo.id)}>{repo.path}</option>}
                </For>
              </select>
            </Show>
          </div>

          {error() && (
            <p id={errorId} class="field-error" role="alert">
              {error()}
            </p>
          )}

          <div class="form-actions">
            <button class="btn btn-secondary" type="button" onClick={props.onClose}>
              Cancel
            </button>
            <button
              class="btn btn-primary"
              type="submit"
              disabled={busy() || props.repos.length === 0}
            >
              {busy() ? 'Creating…' : 'Create task'}
            </button>
          </div>
        </form>
      </div>
    </Modal>
  );
}
