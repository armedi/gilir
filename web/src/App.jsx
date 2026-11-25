import {
  Show,
  createEffect,
  createMemo,
  createSignal,
  createStore,
  onCleanup,
  onSettled,
} from 'solid-js';
import { command } from './api';
import { POLL_INTERVAL } from './constants';
import {
  asArray,
  errorText,
  isAbortError,
  mergeTasks,
  normalizeDetail,
  normalizeTask,
  repoLinkedTo,
  reposForProject,
  sortTasks,
  uniqueMarkings,
} from './utils';
import AddRepoDialog from './components/AddRepoDialog';
import Filters from './components/Filters';
import Header from './components/Header';
import NewProjectDialog from './components/NewProjectDialog';
import NewTaskDialog from './components/NewTaskDialog';
import ProjectsReposView from './components/ProjectsReposView';
import TaskDrawer from './components/TaskDrawer';
import TaskListPanel from './components/TaskListPanel';

function addMarkings(previous, additions) {
  const merged = new Set(previous);
  for (const marking of additions) merged.add(marking);
  if (merged.size === previous.length) return previous;
  return [...merged].sort((left, right) => left.localeCompare(right));
}

const EMPTY_FILTERS = { project: '', repo: '', status: '', priority: '', marking: '' };

export default function App() {
  const [projects, setProjects] = createSignal([]);
  const [projectsLoading, setProjectsLoading] = createSignal(true);
  const [projectsError, setProjectsError] = createSignal(null);

  const [repos, setRepos] = createSignal([]);
  const [reposLoading, setReposLoading] = createSignal(true);
  const [reposError, setReposError] = createSignal(null);
  const [repoReload, setRepoReload] = createSignal(0);

  const [tasks, setTasks] = createSignal([]);
  const [tasksLoading, setTasksLoading] = createSignal(true);
  const [tasksError, setTasksError] = createSignal(null);
  const [taskReload, setTaskReload] = createSignal(0);

  const [daemon, setDaemon] = createSignal(null);
  const [daemonError, setDaemonError] = createSignal(null);
  const [daemonReload, setDaemonReload] = createSignal(0);

  const [selectedTaskId, setSelectedTaskId] = createSignal(null);
  const [detail, setDetail] = createSignal(null);
  const [detailLoading, setDetailLoading] = createSignal(false);
  const [detailError, setDetailError] = createSignal(null);
  const [detailReload, setDetailReload] = createSignal(0);

  const [blockerSource, setBlockerSource] = createSignal({
    tasks: [],
    loading: false,
    error: null,
  });
  const [blockerReload, setBlockerReload] = createSignal(0);

  const [filters, setFilters] = createStore({ ...EMPTY_FILTERS });
  const [markingInput, setMarkingInput] = createSignal('');
  const [knownMarkings, setKnownMarkings] = createSignal([]);

  const [view, setView] = createSignal('tasks');
  const [newTaskOpen, setNewTaskOpen] = createSignal(false);
  const [addRepoOpen, setAddRepoOpen] = createSignal(false);
  const [newProjectOpen, setNewProjectOpen] = createSignal(false);
  const [approvingId, setApprovingId] = createSignal(null);
  const [notice, setNotice] = createSignal('');

  let noticeTimer;
  let projectAbort = null;
  let repoAbort = null;
  let lastTaskQuery = null;
  let lastDetailSelection = null;

  const hasRepos = createMemo(() => repos().length > 0);

  const hasFilters = createMemo(() =>
    Boolean(filters.project || filters.repo || filters.status || filters.priority || filters.marking),
  );

  const watcherWarning = createMemo(() =>
    daemon()?.watcher_error
      ? { title: 'OpenCode warning.', body: daemon()?.watcher_error }
      : daemonError()
        ? { title: 'Queue status unavailable.', body: daemonError() }
        : null,
  );

  function notify(message) {
    if (!message) return;
    setNotice(message);
    clearTimeout(noticeTimer);
    noticeTimer = setTimeout(() => setNotice(''), 4000);
  }

  async function loadProjects() {
    projectAbort?.abort();
    const controller = new AbortController();
    projectAbort = controller;
    setProjectsLoading(true);
    try {
      const data = await command('project.list', {}, { signal: controller.signal });
      if (controller.signal.aborted) return;
      setProjects(
        asArray(data).filter((project) => project && typeof project === 'object'),
      );
      setProjectsError(null);
    } catch (error) {
      if (isAbortError(error)) return;
      setProjectsError(errorText(error));
    } finally {
      if (projectAbort === controller) {
        setProjectsLoading(false);
        projectAbort = null;
      }
    }
  }

  async function loadRepos() {
    repoAbort?.abort();
    const controller = new AbortController();
    repoAbort = controller;
    setReposLoading(true);
    try {
      const data = await command('repo.list', {}, { signal: controller.signal });
      if (controller.signal.aborted) return;
      setRepos(asArray(data).filter((repo) => repo && typeof repo === 'object'));
      setReposError(null);
    } catch (error) {
      if (isAbortError(error)) return;
      setReposError(errorText(error));
    } finally {
      if (repoAbort === controller) {
        setReposLoading(false);
        repoAbort = null;
      }
    }
  }

  onSettled(() => {
    loadProjects();
    loadRepos();
    return () => {
      projectAbort?.abort();
      repoAbort?.abort();
    };
  });

  createEffect(
    () => repoReload(),
    () => {
      loadRepos();
    },
  );

  // A repo filter only survives while the selected project still links it.
  createEffect(
    () => ({ project: filters.project, repo: filters.repo, list: repos() }),
    ({ project, repo, list }) => {
      if (!project || !repo) return;
      const found = list.find((item) => String(item.id) === String(repo));
      if (found && repoLinkedTo(found, project)) return;
      setFilters((draft) => {
        draft.repo = '';
      });
    },
  );

  createEffect(
    () => {
      const project = filters.project;
      const repo = filters.repo;
      const status = filters.status;
      const priority = filters.priority;
      const marking = filters.marking;
      taskReload();
      return { project, repo, status, priority, marking };
    },
    ({ project, repo, status, priority, marking }) => {
      const args = {};
      if (project) args.project_id = Number(project);
      if (repo) args.repo_id = Number(repo);
      if (status) args.status = status;
      if (priority) args.priority = priority;
      if (marking) args.marking = marking;

      const query = JSON.stringify([project, repo, status, priority, marking]);
      if (query !== lastTaskQuery) {
        lastTaskQuery = query;
        setTasks([]);
      }

      setTasksLoading(true);
      setTasksError(null);
      const controller = new AbortController();
      let cancelled = false;
      let timer = null;

      const load = async () => {
        try {
          const data = await command('task.list', args, { signal: controller.signal });
          if (cancelled) return;
          const list = sortTasks(
            asArray(data)
              .map((task) => normalizeTask(task))
              .filter(Boolean),
          );
          setTasks((previous) => mergeTasks(previous, list));
          setTasksError(null);
          const discovered = uniqueMarkings(list);
          if (discovered.length > 0) {
            setKnownMarkings((previous) => addMarkings(previous, discovered));
          }
        } catch (error) {
          if (cancelled || isAbortError(error)) return;
          setTasksError(errorText(error));
        } finally {
          if (!cancelled) setTasksLoading(false);
        }
      };

      const tick = async () => {
        await load();
        if (!cancelled) timer = setTimeout(tick, POLL_INTERVAL);
      };

      tick();
      return () => {
        cancelled = true;
        clearTimeout(timer);
        controller.abort();
      };
    },
  );

  createEffect(markingInput, (value) => {
    const markingTimer = setTimeout(() => {
      const next = value.trim();
      if (filters.marking !== next) {
        setFilters((draft) => {
          draft.marking = next;
        });
      }
    }, 300);
    return () => clearTimeout(markingTimer);
  });

  createEffect(daemonReload, () => {
    const controller = new AbortController();
    let cancelled = false;
    let timer = null;

    const load = async () => {
      try {
        const data = await command('daemon.status', {}, { signal: controller.signal });
        if (cancelled) return;
        setDaemon(data && typeof data === 'object' ? data : null);
        setDaemonError(null);
      } catch (error) {
        if (cancelled || isAbortError(error)) return;
        setDaemonError(errorText(error));
      }
    };

    const tick = async () => {
      await load();
      if (!cancelled) timer = setTimeout(tick, POLL_INTERVAL);
    };

    tick();
    return () => {
      cancelled = true;
      clearTimeout(timer);
      controller.abort();
    };
  });

  createEffect(
    () => [selectedTaskId(), detailReload()],
    ([taskId]) => {
      if (taskId == null) {
        lastDetailSelection = null;
        setDetail(null);
        setDetailError(null);
        setDetailLoading(false);
        return;
      }

      if (String(lastDetailSelection) !== String(taskId)) {
        lastDetailSelection = taskId;
        setDetail(null);
        setDetailError(null);
      }

      setDetailLoading(true);
      const controller = new AbortController();
      let cancelled = false;
      let timer = null;

      const load = async () => {
        try {
          const data = await command('task.show', { id: taskId }, { signal: controller.signal });
          if (cancelled || String(selectedTaskId() ?? '') !== String(taskId)) return;
          const normalized = normalizeDetail(data);
          if (!normalized) throw new Error('The task details were not returned.');
          setDetail(normalized);
          setDetailError(null);
        } catch (error) {
          if (cancelled || isAbortError(error)) return;
          setDetailError(errorText(error));
        } finally {
          if (!cancelled) setDetailLoading(false);
        }
      };

      const tick = async () => {
        await load();
        if (!cancelled) timer = setTimeout(tick, POLL_INTERVAL);
      };

      tick();
      return () => {
        cancelled = true;
        clearTimeout(timer);
        controller.abort();
      };
    },
  );

  // Blockers come from the task's own project, never the active filters.
  createEffect(
    () => [selectedTaskId(), detail()?.task?.project_id, blockerReload()],
    ([taskId, project]) => {
      if (taskId == null || project == null) {
        setBlockerSource({ tasks: [], loading: false, error: null });
        return;
      }

      const controller = new AbortController();
      let cancelled = false;
      setBlockerSource({ tasks: [], loading: true, error: null });
      (async () => {
        try {
          const data = await command(
            'task.list',
            { project_id: project },
            { signal: controller.signal },
          );
          if (cancelled || String(selectedTaskId() ?? '') !== String(taskId)) return;
          setBlockerSource({
            tasks: sortTasks(
              asArray(data)
                .map((item) => normalizeTask(item))
                .filter(Boolean),
            ),
            loading: false,
            error: null,
          });
        } catch (error) {
          if (cancelled || isAbortError(error)) return;
          setBlockerSource({ tasks: [], loading: false, error: errorText(error) });
        }
      })();
      return () => {
        cancelled = true;
        controller.abort();
      };
    },
  );

  createEffect(
    () => [detail()?.task, tasks()],
    ([current, list]) => {
      const discovered = new Set(uniqueMarkings(current ? [current] : []));
      for (const marking of uniqueMarkings(list)) discovered.add(marking);
      if (discovered.size === 0) return;
      setKnownMarkings((previous) => addMarkings(previous, [...discovered]));
    },
  );

  function refreshDaemon() {
    setDaemonReload((value) => value + 1);
  }

  function refreshTasks() {
    setTaskReload((value) => value + 1);
  }

  function refreshAfterTaskChange() {
    setTaskReload((value) => value + 1);
    setDetailReload((value) => value + 1);
    setBlockerReload((value) => value + 1);
  }

  function openNewTask() {
    if (!hasRepos()) {
      setAddRepoOpen(true);
      return;
    }
    setSelectedTaskId(null);
    setDetail(null);
    setNewTaskOpen(true);
  }

  function openAddRepo() {
    setAddRepoOpen(true);
  }

  // One stable handler: the button label reacts to repo count, and so must the
  // action. Passing `hasRepos() ? a : b` would snapshot the handler at mount.
  function primaryAction() {
    if (hasRepos()) openNewTask();
    else openAddRepo();
  }

  function handleRepoAdded() {
    setAddRepoOpen(false);
    setReposLoading(true);
    setRepoReload((value) => value + 1);
    loadProjects();
    notify('Repository added');
  }

  async function handleTaskCreated() {
    setNewTaskOpen(false);
    refreshTasks();
    notify('Task created');
  }

  async function handleProjectCreated() {
    setNewProjectOpen(false);
    await loadProjects();
    notify('Project created');
  }

  function openTask(id) {
    setSelectedTaskId(id);
  }

  function closeTask() {
    setSelectedTaskId(null);
    setDetail(null);
    setDetailError(null);
    setDetailLoading(false);
  }

  async function approveTask(task) {
    if (approvingId() != null) return;
    setApprovingId(task.id);
    try {
      await command('task.review', { id: task.id, approve: true });
      notify(`Task #${task.id} approved`);
      refreshAfterTaskChange();
    } catch (error) {
      notify(errorText(error));
    } finally {
      setApprovingId(null);
    }
  }

  function updateFilter(name, value) {
    setFilters((draft) => {
      draft[name] = value;
      if (name === 'project') draft.repo = '';
    });
  }

  function clearFilters() {
    setFilters((draft) => {
      Object.assign(draft, EMPTY_FILTERS);
    });
    setMarkingInput('');
  }

  const newTaskDefaults = createMemo(() => {
    const scoped = reposForProject(repos(), filters.project);
    const selectedRepo =
      scoped.find((repo) => String(repo.id) === String(filters.repo)) || scoped[0] || null;
    return {
      repo_id: selectedRepo ? String(selectedRepo.id) : '',
      status: filters.status || 'todo',
      priority: filters.priority || 'medium',
    };
  });

  onCleanup(() => {
    clearTimeout(noticeTimer);
  });

  return (
    <div class="app">
      <a class="skip-link" href="#main">
        Skip to content
      </a>
      <Header
        view={view()}
        onViewChange={setView}
        daemon={daemon()}
        daemonError={daemonError()}
        hasRepos={hasRepos()}
        onRefreshDaemon={refreshDaemon}
        onPrimaryAction={primaryAction}
        notify={notify}
      />

      <Show when={watcherWarning()}>
        {(warning) => (
          <div class="watcher-banner" role="alert">
            <span class="watcher-banner-dot" aria-hidden="true" />
            <span class="watcher-banner-text">
              <strong>{warning().title}</strong> {warning().body}
            </span>
            <button class="btn btn-ghost btn-sm" type="button" onClick={refreshDaemon}>
              Retry
            </button>
          </div>
        )}
      </Show>

      <main id="main" class="app-main">
        <Show when={projectsError()}>
          <div class="banner warning app-banner" role="alert">
            <span>Project list refresh failed. {projectsError()}</span>
            <button class="btn btn-ghost btn-sm" type="button" onClick={loadProjects}>
              Retry
            </button>
          </div>
        </Show>

        <Show when={reposError()}>
          <div class="banner warning app-banner" role="alert">
            <span>Repository list refresh failed. {reposError()}</span>
            <button class="btn btn-ghost btn-sm" type="button" onClick={loadRepos}>
              Retry
            </button>
          </div>
        </Show>

        <Show
          when={view() === 'tasks'}
          fallback={
            <ProjectsReposView
              projects={projects()}
              repos={repos()}
              loading={projectsLoading() || reposLoading()}
              error={reposError()}
              onAddRepo={openAddRepo}
              onNewProject={() => setNewProjectOpen(true)}
              onRetry={loadRepos}
            />
          }
        >
          <Filters
            projects={projects()}
            repos={repos()}
            filters={filters}
            onChange={updateFilter}
            markingInput={markingInput()}
            onMarkingInput={setMarkingInput}
            knownMarkings={knownMarkings()}
            hasFilters={hasFilters()}
            onReset={clearFilters}
          />

          <TaskListPanel
            tasks={tasks()}
            loading={tasksLoading()}
            error={tasksError()}
            repos={repos()}
            projects={projects()}
            hasFilters={hasFilters()}
            statusFilter={filters.status}
            selectedTaskId={selectedTaskId()}
            approvingId={approvingId()}
            onOpen={openTask}
            onApprove={approveTask}
            onNewTask={openNewTask}
            onAddRepo={openAddRepo}
            onClearFilters={clearFilters}
            onRetry={refreshTasks}
          />
        </Show>
      </main>

      <Show when={notice()}>
        <div class="toast" role="status" aria-live="polite">
          {notice()}
        </div>
      </Show>

      <AddRepoDialog
        open={addRepoOpen()}
        projects={projects()}
        onClose={() => setAddRepoOpen(false)}
        onAdded={handleRepoAdded}
      />

      <NewProjectDialog
        open={newProjectOpen()}
        onClose={() => setNewProjectOpen(false)}
        onCreated={handleProjectCreated}
      />

      <NewTaskDialog
        open={newTaskOpen()}
        onClose={() => setNewTaskOpen(false)}
        repos={repos()}
        projects={projects()}
        defaults={newTaskDefaults()}
        defaultProjectId={filters.project}
        onCreated={handleTaskCreated}
      />

      <TaskDrawer
        open={selectedTaskId() != null}
        taskId={selectedTaskId()}
        detail={detail()}
        loading={detailLoading()}
        error={detailError()}
        repos={repos()}
        projects={projects()}
        blockerSource={blockerSource()}
        onClose={closeTask}
        onRefresh={refreshAfterTaskChange}
        notify={notify}
      />
    </div>
  );
}
