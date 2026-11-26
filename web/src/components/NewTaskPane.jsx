import {
  For,
  Show,
  createEffect,
  createMemo,
  createSignal,
  createStore,
  createUniqueId,
  onSettled,
  snapshot,
  untrack,
} from 'solid-js';
import { command } from '../api';
import { PRIORITIES, PRIORITY_LABELS, STATUSES, STATUS_LABELS } from '../constants';
import { clearDraft, draftKey, loadDraft, saveDraft } from '../drafts';
import { asArray, basename, errorText, repoProjectIds } from '../utils';
import Markdown from './Markdown';

const EMPTY_FORM = {
  title: '',
  description: '',
  status: 'todo',
  priority: 'medium',
  repo_id: '',
  project_id: '',
};

const DRAFT_KEY = draftKey('new-task');

export default function NewTaskPane(props) {
  const titleId = createUniqueId();
  const descriptionId = createUniqueId();
  const statusId = createUniqueId();
  const priorityId = createUniqueId();
  const repoId = createUniqueId();
  const projectId = createUniqueId();
  const errorId = createUniqueId();
  let titleInput;

  const [form, setForm] = createStore({ ...EMPTY_FORM });
  const [descMode, setDescMode] = createSignal('write');
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

  onSettled(() => {
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
    requestAnimationFrame(() => titleInput?.focus());
  });

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

  function update(field, value) {
    setForm((draft) => {
      draft[field] = value;
      saveDraft(DRAFT_KEY, snapshot(draft));
    });
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
      const created = await command('task.create', args);
      clearDraft(DRAFT_KEY);
      setForm((draft) => {
        Object.assign(draft, EMPTY_FORM);
      });
      await props.onCreated?.(created);
    } catch (requestError) {
      setError(errorText(requestError));
    } finally {
      setBusy(false);
    }
  }

  return (
    <section class="center-panel" aria-label="New task composer">
      <div class="mobile-back-bar">
        <button class="btn btn-ghost btn-sm" type="button" onClick={props.onCancel}>
          ← Queue
        </button>
      </div>

      <header class="center-header">
        <div class="center-header-inner">
          <div class="center-head-main">
            <h2 class="center-title">New task</h2>
            <div class="center-meta">
              <span>Queue work for OpenCode dispatch</span>
            </div>
          </div>
          <button class="btn btn-secondary btn-sm" type="button" onClick={props.onCancel}>
            Cancel
          </button>
        </div>
      </header>

      <div class="center-scroll">
        <div class="stream-column">
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
              <div class="field-label-row">
                <label class="field-label" for={descriptionId}>
                  Description
                </label>
                <div
                  class="segmented segmented-compact"
                  role="tablist"
                  aria-label="Description mode"
                >
                  <button
                    type="button"
                    role="tab"
                    aria-selected={descMode() === 'write' ? 'true' : 'false'}
                    class={['segmented-item', { active: descMode() === 'write' }]}
                    onClick={() => setDescMode('write')}
                  >
                    Write
                  </button>
                  <button
                    type="button"
                    role="tab"
                    aria-selected={descMode() === 'preview' ? 'true' : 'false'}
                    class={['segmented-item', { active: descMode() === 'preview' }]}
                    onClick={() => setDescMode('preview')}
                  >
                    Preview
                  </button>
                </div>
              </div>
              <Show
                when={descMode() === 'write'}
                fallback={
                  <div class="markdown-preview-well">
                    <Show
                      when={form.description.trim()}
                      fallback={<p class="muted-small">Nothing to preview yet.</p>}
                    >
                      <Markdown source={form.description} />
                    </Show>
                  </div>
                }
              >
                <textarea
                  id={descriptionId}
                  class="textarea"
                  rows="6"
                  value={form.description}
                  onInput={(event) => update('description', event.currentTarget.value)}
                  placeholder="Describe the goal, constraints, and acceptance criteria…"
                  disabled={busy()}
                />
                <p class="field-hint">Markdown is supported.</p>
              </Show>
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
              <button class="btn btn-secondary" type="button" onClick={props.onCancel}>
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
      </div>
    </section>
  );
}
