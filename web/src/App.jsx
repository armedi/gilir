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
import { storedProjectId, storeProjectId } from './drafts';
import {
  asArray,
  errorText,
  isAbortError,
  normalizeDetail,
  normalizeTask,
  sortTasks,
  uniqueMarkings,
} from './utils';
import DaemonCard from './components/DaemonCard';
import Filters from './components/Filters';
import Header from './components/Header';
import NewProjectDialog from './components/NewProjectDialog';
import NewTaskDialog from './components/NewTaskDialog';
import ReposPanel from './components/ReposPanel';
import SetupPanel from './components/SetupPanel';
import TaskDrawer from './components/TaskDrawer';
import TaskListPanel from './components/TaskListPanel';

function addMarkings(previous, additions) {
  const merged = new Set(previous);
  for (const marking of additions) merged.add(marking);
  if (merged.size === previous.length) return previous;
  return [...merged].sort((left, right) => left.localeCompare(right));
}

export default function App() {
  const initialStoredProject = storedProjectId();

  const [projects, setProjects] = createSignal([]);
  const [projectsLoading, setProjectsLoading] = createSignal(true);
  const [projectsError, setProjectsError] = createSignal(null);
  const [projectId, setProjectId] = createSignal(null);

  const [repos, setRepos] = createSignal([]);
  const [reposLoading, setReposLoading] = createSignal(false);
  const [reposError, setReposError] = createSignal(null);
  const [repoReload, setRepoReload] = createSignal(0);

  const [tasks, setTasks] = createSignal([]);
  const [tasksLoading, setTasksLoading] = createSignal(false);
  const [tasksError, setTasksError] = createSignal(null);
  const [tasksInitialized, setTasksInitialized] = createSignal(false);
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

  const [filters, setFilters] = createStore({
    repo: '',
    status: '',
    priority: '',
    marking: '',
  });
  const [markingInput, setMarkingInput] = createSignal('');
  const [knownMarkings, setKnownMarkings] = createSignal([]);

  const [newTaskOpen, setNewTaskOpen] = createSignal(false);
  const [newProjectOpen, setNewProjectOpen] = createSignal(false);
  const [notice, setNotice] = createSignal('');

  let noticeTimer;
  let projectRequest = 0;
  let projectAbort = null;
  let lastTaskQuery = null;
  let lastDetailSelection = null;

  const currentProject = createMemo(() =>
    projects().find((project) => String(project.id) === String(projectId())),
  );

  const hasFilters = createMemo(() =>
    Boolean(filters.repo || filters.status || filters.priority || filters.marking),
  );

  const showSetup = createMemo(
    () =>
      Boolean(currentProject()) &&
      tasksInitialized() &&
      !tasksLoading() &&
      !tasksError() &&
      tasks().length === 0 &&
      !hasFilters(),
  );

  function notify(message) {
    if (!message) return;
    setNotice(message);
    clearTimeout(noticeTimer);
    noticeTimer = setTimeout(() => setNotice(''), 4000);
  }

  function changeProject(nextId) {
    const current = projectId();
    const same =
      (current == null && nextId == null) ||
      (current != null &&
        nextId != null &&
        String(current) === String(nextId));
    if (same) return;
    setProjectId(nextId);
    setSelectedTaskId(null);
    setDetail(null);
    setDetailError(null);
    setDetailLoading(false);
    setRepos([]);
    setReposError(null);
    setTasks([]);
    setTasksError(null);
    setTasksInitialized(false);
    setFilters((draft) => {
      Object.assign(draft, { repo: '', status: '', priority: '', marking: '' });
    });
    setMarkingInput('');
    setKnownMarkings([]);
    setBlockerSource({ tasks: [], loading: false, error: null });
    setNewTaskOpen(false);
    storeProjectId(nextId);
  }

  async function loadProjects(options = {}) {
    projectAbort?.abort();
    const controller = new AbortController();
    projectAbort = controller;
    const request = ++projectRequest;
    setProjectsLoading(true);
    try {
      const data = await command('project.list', {}, { signal: controller.signal });
      if (request !== projectRequest || controller.signal.aborted) return;
      const list = asArray(data).filter((project) => project && typeof project === 'object');
      setProjects(list);
      setProjectsError(null);

      let next = null;
      if (options.selectName) {
        for (let index = list.length - 1; index >= 0; index -= 1) {
          if (list[index].name === options.selectName) {
            next = list[index];
            break;
          }
        }
      }
      if (!next) {
        const current = projectId();
        next = list.find((project) => String(project.id) === String(current)) || null;
      }
      if (!next) {
        next =
          list.find((project) => String(project.id) === String(initialStoredProject)) || null;
      }
      if (!next) next = list[0] || null;
      changeProject(next ? next.id : null);
    } catch (error) {
      if (request !== projectRequest || isAbortError(error)) return;
      setProjectsError(errorText(error));
    } finally {
      if (request === projectRequest) {
        setProjectsLoading(false);
        if (projectAbort === controller) projectAbort = null;
      }
    }
  }

  onSettled(() => {
    loadProjects();
    return () => projectAbort?.abort();
  });

  createEffect(
    () => [projectId(), repoReload()],
    ([project]) => {
      if (project == null) {
        setRepos([]);
        setReposLoading(false);
        setReposError(null);
        return;
      }
      const controller = new AbortController();
      let cancelled = false;
      setReposLoading(true);
      (async () => {
        try {
          const data = await command(
            'repo.list',
            { project_id: project },
            { signal: controller.signal },
          );
          if (cancelled) return;
          setRepos(asArray(data).filter((repo) => repo && typeof repo === 'object'));
          setReposError(null);
        } catch (error) {
          if (cancelled || isAbortError(error)) return;
          setReposError(errorText(error));
        } finally {
          if (!cancelled) setReposLoading(false);
        }
      })();
      return () => {
        cancelled = true;
        controller.abort();
      };
    },
  );

  createEffect(
    () => {
      const project = projectId();
      const repoFilter = filters.repo;
      const status = filters.status;
      const priority = filters.priority;
      const marking = filters.marking;
      taskReload();
      const repo =
        project != null && repoFilter
          ? repos().find((item) => String(item.id) === String(repoFilter))
          : null;
      return { project, repo, status, priority, marking };
    },
    ({ project, repo, status, priority, marking }) => {
      if (project == null) {
        setTasks([]);
        setTasksInitialized(false);
        setTasksLoading(false);
        setTasksError(null);
        return;
      }

      const args = { project_id: project };
      if (repo) args.repo_id = repo.id;
      if (status) args.status = status;
      if (priority) args.priority = priority;
      if (marking) args.marking = marking;

      const query = JSON.stringify([
        project,
        repo ? repo.id : '',
        status,
        priority,
        marking,
      ]);
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
          if (cancelled || String(projectId() ?? '') !== String(project)) return;
          const list = sortTasks(
            asArray(data)
              .map((task) => normalizeTask(task))
              .filter(Boolean),
          );
          setTasks(list);
          setTasksError(null);
          setTasksInitialized(true);
          const discovered = uniqueMarkings(list);
          if (discovered.length > 0) {
            setKnownMarkings((previous) => addMarkings(previous, discovered));
          }
        } catch (error) {
          if (cancelled || isAbortError(error)) return;
          setTasksError(errorText(error));
          setTasksInitialized(true);
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

  createEffect(markingInput, (previous) => {
    const markingTimer = setTimeout(() => {
      const next = previous.trim();
      if (filters.marking !== next) {
        setFilters((draft) => {
          draft.marking = next;
        });
      }
    }, 300);
    return () => clearTimeout(markingTimer);
  });

  onCleanup(() => {
    clearTimeout(noticeTimer);
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
    () => [selectedTaskId(), projectId(), detailReload()],
    ([taskId, project]) => {
      if (taskId == null || project == null) {
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

  createEffect(
    () => [selectedTaskId(), projectId(), blockerReload()],
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
    () => detail()?.task,
    (current) => {
      if (!current) return;
      const discovered = uniqueMarkings([current]);
      if (discovered.length > 0) {
        setKnownMarkings((previous) => addMarkings(previous, discovered));
      }
    },
  );

  function refreshRepos() {
    setRepoReload((value) => value + 1);
  }

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

  async function handleProjectCreated(name) {
    setNewProjectOpen(false);
    setSelectedTaskId(null);
    setDetail(null);
    await loadProjects({ selectName: name });
    notify('Project created');
  }

  function handleRepoAdded() {
    refreshRepos();
    notify('Repository added');
  }

  function handleTaskCreated() {
    setNewTaskOpen(false);
    refreshTasks();
    notify('Task created');
  }

  function openNewTask() {
    if (repos().length === 0) {
      notify('Add a repository first.');
      return;
    }
    setSelectedTaskId(null);
    setDetail(null);
    setNewTaskOpen(true);
  }

  function openNewProject() {
    setSelectedTaskId(null);
    setDetail(null);
    setNewProjectOpen(true);
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

  function selectProject(value) {
    const project = projects().find((item) => String(item.id) === String(value));
    if (project) changeProject(project.id);
  }

  function updateFilter(name, value) {
    setFilters((draft) => {
      draft[name] = value;
    });
  }

  function clearFilters() {
    setFilters((draft) => {
      Object.assign(draft, { repo: '', status: '', priority: '', marking: '' });
    });
    setMarkingInput('');
  }

  function newTaskDefaults() {
    const repoFilter = filters.repo;
    const selectedRepo =
      repos().find((repo) => String(repo.id) === String(repoFilter)) || repos()[0] || null;
    return {
      repo_id: selectedRepo ? String(selectedRepo.id) : '',
      status: filters.status || 'todo',
      priority: filters.priority || 'medium',
    };
  }

  const taskCanStart = () => repos().length > 0;

  return (
    <div class="app">
      <a class="skip-link" href="#main">
        Skip to content
      </a>
      <Header
        projects={projects()}
        projectId={projectId()}
        onProjectChange={selectProject}
        onNewProject={openNewProject}
      />

      <main id="main" class="app-main">
        <Show when={projects().length > 0}>
          <Show when={projectsError()}>
            <div class="banner warning app-banner" role="alert">
              <span>Project list refresh failed. {projectsError()}</span>
              <button class="btn btn-ghost btn-sm" type="button" onClick={() => loadProjects()}>
                Retry
              </button>
            </div>
          </Show>
        </Show>

        <Show when={projects().length === 0}>
          <Show
            when={!projectsLoading()}
            fallback={
              <div class="setup-shell">
                <section class="panel setup-panel" aria-busy="true" role="status">
                  <div class="setup-header">
                    <h2>Loading workspace</h2>
                    <p>Connecting to the Gilir API.</p>
                  </div>
                </section>
              </div>
            }
          >
            <Show
              when={projectsError()}
              fallback={
                <SetupPanel
                  project={null}
                  repos={[]}
                  reposLoading={false}
                  reposError={null}
                  onProjectCreated={handleProjectCreated}
                  onRepoAdded={handleRepoAdded}
                  onRetryRepos={refreshRepos}
                  onNewTask={openNewTask}
                />
              }
            >
              <div class="setup-shell">
                <section class="panel setup-panel" role="alert">
                  <div class="setup-header">
                    <h2>Cannot load projects</h2>
                    <p>{projectsError()}</p>
                  </div>
                  <div class="setup-actions">
                    <button class="btn btn-primary" type="button" onClick={() => loadProjects()}>
                      Retry
                    </button>
                  </div>
                </section>
              </div>
            </Show>
          </Show>
        </Show>

        <Show when={projects().length > 0}>
          <Show
            when={currentProject()}
            fallback={
              <div class="setup-shell">
                <section class="panel setup-panel" aria-busy="true" role="status">
                  <div class="setup-header">
                    <h2>Loading project</h2>
                    <p>Selecting the active project.</p>
                  </div>
                </section>
              </div>
            }
          >
            {(project) => (
              <>
                <Show when={!tasksInitialized()}>
                  <Show
                    when={tasksError()}
                    fallback={
                      <div class="setup-shell">
                        <section class="panel setup-panel" aria-busy="true" role="status">
                          <div class="setup-header">
                            <h2>Loading queue</h2>
                            <p>Fetching tasks for {project().name}.</p>
                          </div>
                        </section>
                      </div>
                    }
                  >
                    <div class="setup-shell">
                      <section class="panel setup-panel" role="alert">
                        <div class="setup-header">
                          <h2>Cannot load tasks</h2>
                          <p>{tasksError()}</p>
                        </div>
                        <div class="setup-actions">
                          <button class="btn btn-primary" type="button" onClick={refreshTasks}>
                            Retry
                          </button>
                        </div>
                      </section>
                    </div>
                  </Show>
                </Show>

                <Show when={tasksInitialized()}>
                  <Show
                    when={!showSetup()}
                    fallback={
                      <SetupPanel
                        project={project()}
                        repos={repos()}
                        reposLoading={reposLoading()}
                        reposError={reposError()}
                        onProjectCreated={handleProjectCreated}
                        onRepoAdded={handleRepoAdded}
                        onRetryRepos={refreshRepos}
                        onNewTask={openNewTask}
                      />
                    }
                  >
                    <div class="workspace">
                      <aside class="sidebar" aria-label="Queue controls">
                        <DaemonCard
                          status={daemon()}
                          error={daemonError()}
                          onRefresh={refreshDaemon}
                          notify={notify}
                        />
                        <Filters
                          repos={repos()}
                          filters={filters}
                          onChange={updateFilter}
                          markingInput={markingInput()}
                          onMarkingInput={setMarkingInput}
                          knownMarkings={knownMarkings()}
                          hasFilters={hasFilters()}
                          onReset={clearFilters}
                        />
                        <ReposPanel
                          repos={repos()}
                          loading={reposLoading()}
                          error={reposError()}
                          projectId={projectId()}
                          onAdded={handleRepoAdded}
                          onRetry={refreshRepos}
                        />
                      </aside>
                      <div class="main-panel">
                        <TaskListPanel
                          tasks={tasks()}
                          loading={tasksLoading()}
                          error={tasksError()}
                          repos={repos()}
                          hasFilters={hasFilters()}
                          onOpen={openTask}
                          onNewTask={openNewTask}
                          onClearFilters={clearFilters}
                          onRetry={refreshTasks}
                          selectedTaskId={selectedTaskId()}
                          canCreate={taskCanStart()}
                        />
                      </div>
                    </div>
                  </Show>
                </Show>
              </>
            )}
          </Show>
        </Show>
      </main>

      <Show when={notice()}>
        <div class="toast" role="status" aria-live="polite">
          {notice()}
        </div>
      </Show>

      <Show when={projects().length > 0}>
        <NewProjectDialog
          open={newProjectOpen()}
          onClose={() => setNewProjectOpen(false)}
          onCreated={handleProjectCreated}
        />
      </Show>

      <Show when={currentProject()}>
        <NewTaskDialog
          open={newTaskOpen()}
          onClose={() => setNewTaskOpen(false)}
          projectId={projectId()}
          repos={repos()}
          defaults={newTaskDefaults()}
          onCreated={handleTaskCreated}
        />
        <TaskDrawer
          open={selectedTaskId() != null}
          taskId={selectedTaskId()}
          detail={detail()}
          loading={detailLoading()}
          error={detailError()}
          projectId={projectId()}
          repos={repos()}
          blockerSource={blockerSource()}
          onClose={closeTask}
          onRefresh={refreshAfterTaskChange}
          notify={notify}
        />
      </Show>
    </div>
  );
}
