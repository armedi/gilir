import {
  For,
  Show,
  createEffect,
  createMemo,
  createSignal,
  createUniqueId,
} from 'solid-js';
import { asArray, basename, errorText, repoProjectIds } from '../utils';

function focusSoon(element) {
  requestAnimationFrame(() => element?.focus());
}

function taskCountFor(counts, projectId) {
  if (!counts || typeof counts !== 'object') return 0;
  const value = counts[String(projectId)];
  return Number.isFinite(value) ? value : 0;
}

function RepoRow(props) {
  const [busy, setBusy] = createSignal(null);
  const [error, setError] = createSignal(null);
  const [confirming, setConfirming] = createSignal(false);
  const [linkChoice, setLinkChoice] = createSignal('');
  let removeButton;
  let confirmButton;

  const repo = () => props.getRepo();
  const name = () => basename(repo().path) || repo().path;
  const projectIds = () => asArray(repo().project_ids);
  const soleProject = createMemo(() => repoProjectIds(repo()).length <= 1);
  const availableProjects = createMemo(() => {
    const linked = new Set(repoProjectIds(repo()));
    return asArray(props.projects).filter((project) => !linked.has(String(project.id)));
  });
  const projectName = (projectId) => {
    const found = props.projectMap.get(String(projectId));
    return found ? found.name : `Project #${projectId}`;
  };
  const unlinkHint = () =>
    soleProject() ? 'A repository must stay linked to at least one project.' : undefined;

  createEffect(
    () => confirming(),
    (open) => {
      focusSoon(open ? confirmButton : removeButton);
    },
    { defer: true },
  );

  // A selected project can disappear from the picker (deleted elsewhere);
  // drop the stale choice so the form cannot submit an unknown project.
  createEffect(
    () => ({ choice: linkChoice(), available: availableProjects() }),
    ({ choice, available }) => {
      if (!choice) return;
      if (available.some((project) => String(project.id) === choice)) return;
      setLinkChoice('');
    },
  );

  async function unlink(projectId) {
    if (busy() !== null) return;
    setBusy(`unlink:${projectId}`);
    setError(null);
    try {
      await props.onUnlink(repo().id, projectId);
    } catch (requestError) {
      setError(errorText(requestError));
    } finally {
      setBusy(null);
    }
  }

  async function submitLink(event) {
    event.preventDefault();
    const projectId = Number(linkChoice());
    if (!projectId || busy() !== null) return;
    setBusy(`link:${projectId}`);
    setError(null);
    try {
      await props.onLink(repo().id, projectId);
      setLinkChoice('');
    } catch (requestError) {
      setError(errorText(requestError));
    } finally {
      setBusy(null);
    }
  }

  async function remove() {
    if (busy() !== null) return;
    setBusy('remove');
    setError(null);
    try {
      await props.onRemove(repo().id);
      setConfirming(false);
    } catch (requestError) {
      setError(errorText(requestError));
    } finally {
      setBusy(null);
    }
  }

  return (
    <li class="manage-item">
      <div class="repo-row">
        <div class="repo-main">
          <span class="repo-name">{name()}</span>
          <span class="repo-path" title={repo().path}>
            {repo().path}
          </span>
        </div>

        <div class="repo-links">
          <ul class="repo-chips" aria-label={`Projects linked to ${name()}`}>
            <For each={projectIds()}>
              {(projectId) => (
                <li class="chip manage-chip" title={unlinkHint()}>
                  <span class="manage-chip-name">{projectName(projectId)}</span>
                  <button
                    type="button"
                    class="chip-remove"
                    aria-label={
                      soleProject()
                        ? `${projectName(projectId)} is the only project for ${name()}; a repository must stay linked to at least one project`
                        : `Unlink ${name()} from ${projectName(projectId)}`
                    }
                    title={unlinkHint() || `Unlink from ${projectName(projectId)}`}
                    disabled={soleProject() || busy() !== null}
                    onClick={() => unlink(projectId)}
                  >
                    ×
                  </button>
                </li>
              )}
            </For>
          </ul>

          <Show when={soleProject()}>
            <span class="manage-hint">
              {availableProjects().length > 0
                ? 'Only project link — link another before unlinking.'
                : 'Only project — a repository must stay linked to at least one.'}
            </span>
          </Show>

          <Show
            when={availableProjects().length > 0}
            fallback={
              <Show when={!soleProject()}>
                <span class="manage-hint">Linked to every project</span>
              </Show>
            }
          >
            <form class="link-form" onSubmit={submitLink}>
              <select
                class="select link-picker"
                aria-label={`Link ${name()} to another project`}
                value={linkChoice()}
                onChange={(event) => {
                  setLinkChoice(event.currentTarget.value);
                  setError(null);
                }}
                disabled={busy() !== null}
              >
                <option value="">Link to project…</option>
                <For each={availableProjects()}>
                  {(project) => <option value={String(project.id)}>{project.name}</option>}
                </For>
              </select>
              <button
                type="submit"
                class="btn btn-secondary btn-sm"
                disabled={!linkChoice() || busy() !== null}
              >
                {busy()?.startsWith('link:') ? 'Linking…' : 'Link'}
              </button>
            </form>
          </Show>
        </div>

        <div class="manage-row-actions">
          <Show
            when={confirming()}
            fallback={
              <button
                ref={removeButton}
                type="button"
                class="btn btn-ghost btn-sm"
                aria-label={`Remove repository ${name()}`}
                onClick={() => {
                  setError(null);
                  setConfirming(true);
                }}
                disabled={busy() !== null}
              >
                Remove
              </button>
            }
          >
            <div class="confirm-inline" role="group" aria-label={`Confirm removal of ${name()}`}>
              <span class="confirm-text">Remove?</span>
              <button
                ref={confirmButton}
                type="button"
                class="btn btn-danger btn-sm"
                aria-label={`Confirm removal of repository ${name()}`}
                onClick={remove}
                disabled={busy() !== null}
              >
                {busy() === 'remove' ? 'Removing…' : 'Confirm remove'}
              </button>
              <button
                type="button"
                class="btn btn-ghost btn-sm"
                onClick={() => {
                  setConfirming(false);
                  setError(null);
                }}
                disabled={busy() !== null}
              >
                Cancel
              </button>
            </div>
          </Show>
        </div>
      </div>

      <Show when={error()}>
        <p class="field-error manage-error" role="alert">
          {error()}
        </p>
      </Show>
    </li>
  );
}

function ProjectRow(props) {
  const inputId = createUniqueId();
  const [busy, setBusy] = createSignal(null);
  const [error, setError] = createSignal(null);
  const [editing, setEditing] = createSignal(false);
  const [draft, setDraft] = createSignal('');
  const [confirming, setConfirming] = createSignal(false);
  let nameInput;
  let renameButton;
  let removeButton;
  let confirmButton;

  const name = () => props.getProject().name;
  const hasTasks = () => props.taskCount > 0;
  const jumpable = () => props.repoCount > 0 || props.taskCount > 0;

  // Focus moves once per transition: into the input when rename opens, back to
  // the Rename button when it closes on its own. Leaving rename to open the
  // remove confirmation hands focus to that effect instead.
  createEffect(
    () => ({ editing: editing(), confirming: confirming() }),
    (state, previous) => {
      if (state.editing) {
        requestAnimationFrame(() => {
          nameInput?.focus();
          nameInput?.select();
        });
        return;
      }
      if (!previous?.editing || state.confirming) return;
      focusSoon(renameButton);
    },
    { defer: true },
  );

  createEffect(
    () => confirming(),
    (open) => {
      focusSoon(open ? confirmButton : removeButton);
    },
    { defer: true },
  );

  function startRename() {
    setConfirming(false);
    setDraft(name());
    setError(null);
    setEditing(true);
  }

  function startRemove() {
    setEditing(false);
    setDraft('');
    setError(null);
    setConfirming(true);
  }

  function cancelRename() {
    setEditing(false);
    setDraft('');
    setError(null);
  }

  async function submitRename(event) {
    event.preventDefault();
    if (busy() !== null) return;
    const next = draft().trim();
    if (!next) {
      setError('Enter a project name.');
      nameInput?.focus();
      return;
    }
    if (next === name()) {
      cancelRename();
      return;
    }
    setBusy('rename');
    setError(null);
    try {
      await props.onRename(props.getProject().id, next);
      setEditing(false);
      setDraft('');
    } catch (requestError) {
      setError(errorText(requestError));
      nameInput?.focus();
    } finally {
      setBusy(null);
    }
  }

  async function remove() {
    if (busy() !== null) return;
    setBusy('remove');
    setError(null);
    try {
      await props.onRemove(props.getProject().id);
      setConfirming(false);
    } catch (requestError) {
      setError(errorText(requestError));
    } finally {
      setBusy(null);
    }
  }

  function handleRenameKeyDown(event) {
    if (event.key !== 'Escape') return;
    event.preventDefault();
    cancelRename();
  }

  return (
    <li class="manage-item">
      <div class="project-row">
        <div class="project-main">
          <Show when={editing()} fallback={<span class="project-name">{name()}</span>}>
            <form class="rename-form" onSubmit={submitRename} onKeyDown={handleRenameKeyDown}>
              <label class="sr-only" for={inputId}>
                Project name
              </label>
              <input
                ref={nameInput}
                id={inputId}
                class="input rename-input"
                type="text"
                value={draft()}
                onInput={(event) => {
                  setDraft(event.currentTarget.value);
                  setError(null);
                }}
                aria-label={`Rename project ${name()}`}
                autocomplete="off"
                disabled={busy() !== null}
              />
              <button
                class="btn btn-primary btn-sm"
                type="submit"
                disabled={busy() !== null || !draft().trim()}
              >
                {busy() === 'rename' ? 'Saving…' : 'Save'}
              </button>
              <button
                class="btn btn-ghost btn-sm"
                type="button"
                onClick={cancelRename}
                disabled={busy() !== null}
              >
                Cancel
              </button>
            </form>
          </Show>
        </div>

        <button
          type="button"
          class="project-repos"
          onClick={() => props.onViewTasks(props.getProject().id)}
          disabled={!jumpable()}
          aria-label={`View tasks in project ${name()}: ${props.repoCount} ${
            props.repoCount === 1 ? 'repository' : 'repositories'
          }, ${props.taskCount} ${props.taskCount === 1 ? 'task' : 'tasks'}`}
          title={
            jumpable()
              ? `View tasks in ${name()}`
              : 'No repositories or tasks in this project yet'
          }
        >
          <span class="project-repos-metric">
            {props.repoCount}
            <span class="project-repos-label">
              {props.repoCount === 1 ? 'repo' : 'repos'}
            </span>
          </span>
          <span class={['project-repos-metric', { 'is-empty': !hasTasks() }]}>
            {props.taskCount}
            <span class="project-repos-label">
              {props.taskCount === 1 ? 'task' : 'tasks'}
            </span>
          </span>
        </button>

        <div class="manage-row-actions">
          <Show when={!editing()}>
            <button
              ref={renameButton}
              type="button"
              class="btn btn-ghost btn-sm"
              aria-label={`Rename project ${name()}`}
              onClick={startRename}
              disabled={busy() !== null}
            >
              Rename
            </button>
          </Show>
          <Show
            when={confirming()}
            fallback={
              <button
                ref={removeButton}
                type="button"
                class="btn btn-ghost btn-sm"
                aria-label={`Remove project ${name()}`}
                onClick={startRemove}
                disabled={busy() !== null}
              >
                Remove
              </button>
            }
          >
            <div
              class="confirm-inline"
              role="group"
              aria-label={`Confirm removal of ${name()}`}
            >
              <span class="confirm-text">Remove?</span>
              <button
                ref={confirmButton}
                type="button"
                class="btn btn-danger btn-sm"
                aria-label={`Confirm removal of project ${name()}`}
                onClick={remove}
                disabled={busy() !== null}
              >
                {busy() === 'remove' ? 'Removing…' : 'Confirm remove'}
              </button>
              <button
                type="button"
                class="btn btn-ghost btn-sm"
                onClick={() => {
                  setConfirming(false);
                  setError(null);
                }}
                disabled={busy() !== null}
              >
                Cancel
              </button>
            </div>
          </Show>
        </div>
      </div>

      <Show when={error()}>
        <p class="field-error manage-error" role="alert">
          {error()}
        </p>
      </Show>
    </li>
  );
}

export default function ProjectsReposView(props) {
  const reposTitleId = createUniqueId();
  const projectsTitleId = createUniqueId();

  const projectMap = createMemo(() => {
    const map = new Map();
    for (const project of asArray(props.projects)) map.set(String(project.id), project);
    return map;
  });

  const reposPerProject = createMemo(() => {
    const counts = new Map();
    for (const repo of asArray(props.repos)) {
      for (const id of repoProjectIds(repo)) counts.set(id, (counts.get(id) || 0) + 1);
    }
    return counts;
  });

  return (
    <div class="manage-view">
      <section class="panel-surface manage-section" aria-labelledby={reposTitleId}>
        <header class="manage-head">
          <div class="manage-head-main">
            <div class="manage-title-row">
              <h2 id={reposTitleId} class="manage-title">
                Repositories
              </h2>
              <span class="task-count" aria-live="polite">
                {asArray(props.repos).length}
                <span class="sr-only"> repositories</span>
              </span>
            </div>
            <p class="manage-sub">
              Git repositories on this machine, linked to the projects whose tasks run in them.
            </p>
          </div>
          <button class="btn btn-secondary btn-sm" type="button" onClick={props.onAddRepo}>
            + Add repository
          </button>
        </header>

        <Show when={props.reposLoading && asArray(props.repos).length === 0}>
          <p class="manage-status" role="status">
            Loading repositories…
          </p>
        </Show>

        <Show when={!props.reposLoading && !props.reposError && asArray(props.repos).length === 0}>
          <div class="manage-empty">
            <p>
              <strong>No repositories yet.</strong> Add a Git repository to start queuing work.
            </p>
            <button class="btn btn-primary btn-sm" type="button" onClick={props.onAddRepo}>
              Add repository
            </button>
          </div>
        </Show>

        <Show when={asArray(props.repos).length > 0}>
          <ul class="manage-rows">
            <For each={props.repos} keyed={(repo) => String(repo.id)}>
              {(repo) => (
                <RepoRow
                  getRepo={repo}
                  projects={props.projects}
                  projectMap={projectMap()}
                  onLink={props.onLinkRepo}
                  onUnlink={props.onUnlinkRepo}
                  onRemove={props.onRemoveRepo}
                />
              )}
            </For>
          </ul>
        </Show>
      </section>

      <section class="panel-surface manage-section" aria-labelledby={projectsTitleId}>
        <header class="manage-head">
          <div class="manage-head-main">
            <div class="manage-title-row">
              <h2 id={projectsTitleId} class="manage-title">
                Projects
              </h2>
              <span class="task-count" aria-live="polite">
                {asArray(props.projects).length}
                <span class="sr-only"> projects</span>
              </span>
            </div>
            <p class="manage-sub">Projects group tasks and share repositories many-to-many.</p>
          </div>
          <button class="btn btn-secondary btn-sm" type="button" onClick={props.onNewProject}>
            + New project
          </button>
        </header>

        <Show when={props.projectsLoading && asArray(props.projects).length === 0}>
          <p class="manage-status" role="status">
            Loading projects…
          </p>
        </Show>

        <Show
          when={!props.projectsLoading && !props.projectsError && asArray(props.projects).length === 0}
        >
          <div class="manage-empty">
            <p>
              <strong>No projects yet.</strong> Create one by name or add a repository.
            </p>
            <button class="btn btn-primary btn-sm" type="button" onClick={props.onNewProject}>
              New project
            </button>
          </div>
        </Show>

        <Show when={asArray(props.projects).length > 0}>
          <ul class="manage-rows">
            <For each={props.projects} keyed={(project) => String(project.id)}>
              {(project) => (
                <ProjectRow
                  getProject={project}
                  repoCount={reposPerProject().get(String(project().id)) || 0}
                  taskCount={taskCountFor(props.projectTaskCounts, project().id)}
                  onRename={props.onRenameProject}
                  onRemove={props.onRemoveProject}
                  onViewTasks={props.onViewProjectTasks}
                />
              )}
            </For>
          </ul>
        </Show>
      </section>
    </div>
  );
}
