import {
  For,
  Show,
  createEffect,
  createMemo,
  createSignal,
  createStore,
  createUniqueId,
  snapshot,
  untrack,
} from 'solid-js';
import { command } from '../api';
import { PRIORITIES, PRIORITY_LABELS, STATUSES, STATUS_LABELS } from '../constants';
import { clearDraft, draftKey, loadDraft, saveDraft } from '../drafts';
import { asArray, basename, errorText, repoProjectIds } from '../utils';
import Modal from './Modal';

const EMPTY_FORM = {
  title: '',
  description: '',
  status: 'todo',
  priority: 'medium',
  repo_id: '',
  project_id: '',
};

const DRAFT_KEY = draftKey('new-task');

export default function NewTaskDialog(props) {
  const dialogTitleId = createUniqueId();
  const titleId = createUniqueId();
  const descriptionId = createUniqueId();
  const statusId = createUniqueId();
  const priorityId = createUniqueId();
  const repoId = createUniqueId();
  const projectId = createUniqueId();
  const errorId = createUniqueId();
  let titleInput;
  let lastOpen = false;

  const [form, setForm] = createStore({ ...EMPTY_FORM });
  const [busy, setBusy] = createSignal(false);
  const [error, setError] = createSignal(null);

  const selectedRepo = createMemo(() =>
    asArray(props.repos).find((repo) => String(repo.id) === String(form.repo_id)),
  );

  const linkedProjects = createMemo(() => {
    const repo = selectedRepo();
    if (!repo) return [];
    const ids = repoProjectIds(repo);
    return asArray(props.projects).filter((project) => ids.includes(String(project.id)));
  });

  const needsProjectChoice = createMemo(() => linkedProjects().length > 1);

  const inferredProject = createMemo(() => {
    const linked = linkedProjects();
    return linked.length === 1 ? linked[0] : null;
  });

  function initialize() {
    const stored = loadDraft(DRAFT_KEY);
    const defaults = untrack(() => props.defaults);
    const repos = asArray(untrack(() => props.repos));
    const next = {
      title: '',
      description: '',
      status: defaults?.status || 'todo',
      priority: defaults?.priority || 'medium',
      repo_id: defaults?.repo_id || '',
      project_id: '',
    };
    if (stored && typeof stored === 'object') {
      if (typeof stored.title === 'string') next.title = stored.title;
      if (typeof stored.description === 'string') next.description = stored.description;
      if (STATUSES.includes(stored.status)) next.status = stored.status;
      if (PRIORITIES.includes(stored.priority)) next.priority = stored.priority;
      if (stored.repo_id != null) next.repo_id = String(stored.repo_id);
      if (stored.project_id != null) next.project_id = String(stored.project_id);
    }
    if (!repos.some((repo) => String(repo.id) === String(next.repo_id))) {
      next.repo_id = repos[0] ? String(repos[0].id) : '';
    }
    setForm((draft) => {
      Object.assign(draft, next);
    });
    setError(null);
    setBusy(false);
  }

  createEffect(
    () => props.open,
    (open) => {
      if (!open) {
        lastOpen = false;
        return;
      }
      if (lastOpen) return;
      lastOpen = true;
      initialize();
    },
  );

  createEffect(
    () => props.open,
    (open) => {
      if (!open) return;
      requestAnimationFrame(() => {
        if (props.open && titleInput) titleInput.focus();
      });
    },
  );

  // Repo changes reset the project choice; changing to a repo that links the
  // chosen project keeps it, otherwise it clears so the user re-picks.
  createEffect(
    () => form.repo_id,
    () => {
      if (!form.project_id) return;
      const linked = linkedProjects();
      if (linked.some((project) => String(project.id) === String(form.project_id))) return;
      setForm((draft) => {
        draft.project_id = '';
      });
    },
  );

  // Default the project choice to the filter/repo default when 2+ links exist.
  createEffect(
    () => ({ needs: needsProjectChoice(), current: form.project_id, linked: linkedProjects() }),
    ({ needs, current, linked }) => {
      if (!needs || current) return;
      const preferred = String(untrack(() => props.defaultProjectId) ?? '');
      const match = linked.find((project) => String(project.id) === preferred) || linked[0];
      if (!match) return;
      setForm((draft) => {
        draft.project_id = String(match.id);
      });
    },
  );

  function persist() {
    saveDraft(DRAFT_KEY, snapshot(form));
  }

  function update(field, value) {
    setForm((draft) => {
      draft[field] = value;
    });
    persist();
    setError(null);
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
    const repo = selectedRepo();
    if (!repo) {
      setError('Choose a repository.');
      return;
    }
    if (needsProjectChoice() && !form.project_id) {
      setError('Choose a project for this repository.');
      return;
    }
    const args = {
      repo_id: repo.id,
      title,
      status: form.status,
      priority: form.priority,
    };
    if (needsProjectChoice()) args.project_id = Number(form.project_id);
    if (form.description.trim()) args.description = form.description;
    setBusy(true);
    setError(null);
    try {
      await command('task.create', args);
      clearDraft(DRAFT_KEY);
      setForm((draft) => {
        Object.assign(draft, EMPTY_FORM);
      });
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
                  {(repo) => (
                    <option value={String(repo.id)} title={repo.path}>
                      {basename(repo.path) || repo.path}
                    </option>
                  )}
                </For>
              </select>
            </Show>
          </div>

          <Show when={selectedRepo()}>
            <Show
              when={needsProjectChoice()}
              fallback={
                <p class="field-hint">
                  <Show
                    when={inferredProject()}
                    fallback="This repository has no linked project yet."
                  >
                    {(project) => <>Project: {project().name}</>}
                  </Show>
                </p>
              }
            >
              <div class="field">
                <label class="field-label" for={projectId}>
                  Project
                </label>
                <select
                  id={projectId}
                  class="select"
                  value={form.project_id}
                  onChange={(event) => update('project_id', event.currentTarget.value)}
                  disabled={busy()}
                  aria-required="true"
                >
                  <option value="">Select a project</option>
                  <For each={linkedProjects()}>
                    {(project) => <option value={String(project.id)}>{project.name}</option>}
                  </For>
                </select>
                <p class="field-hint">
                  This repository is linked to multiple projects. Choose which one owns the task.
                </p>
              </div>
            </Show>
          </Show>

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

          <Show when={error()}>
            <p id={errorId} class="field-error" role="alert">
              {error()}
            </p>
          </Show>

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
