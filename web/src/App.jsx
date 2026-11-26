import {
  For,
  Show,
  createEffect,
  createMemo,
  createSignal,
  createStore,
  onCleanup,
  onSettled,
} from 'solid-js';
import { CommandError, command } from './api';
import { POLL_INTERVAL } from './constants';
import {
  asArray,
  errorText,
  formatRelativeTime,
  isAbortError,
  mergeTasks,
  normalizeDetail,
  normalizeTask,
  repoLinkedTo,
  reposForProject,
  sortTasks,
  uniqueMarkings,
} from './utils';
import AddRepoForm from './components/AddRepoForm';
import Filters from './components/Filters';
import Header from './components/Header';
import NewTaskPane from './components/NewTaskPane';
import ProjectsReposView from './components/ProjectsReposView';
import TaskDetailPane from './components/TaskDetailPane';
import TaskInspectorPanel from './components/TaskInspectorPanel';
import TaskListPanel from './components/TaskListPanel';

function addMarkings(previous, additions) {
  const merged = new Set(previous);
  for (const marking of additions) merged.add(marking);
  if (merged.size === previous.length) return previous;
  return [...merged].sort((left, right) => left.localeCompare(right));
}

function readStoredNumber(key, fallback, min, max) {
  try {
    const raw = Number(window.localStorage.getItem(key));
    if (Number.isFinite(raw) && raw >= min && raw <= max) return raw;
  } catch {
    return fallback;
  }
  return fallback;
}

function writeStoredValue(key, value) {
  try {
    window.localStorage.setItem(key, String(value));
  } catch {
    return;
  }
}

const EMPTY_FILTERS = { project: '', repo: '', status: '', priority: '', marking: '' };
const LEFT_MIN = 250;
const LEFT_MAX = 460;
const RIGHT_MIN = 250;
const RIGHT_MAX = 420;

export default function App() {
  const [projects, setProjects] = createSignal([]);
  const [projectsLoading, setProjectsLoading] = createSignal(true);
  const [projectsError, setProjectsError] = createSignal(null);

  const [repos, setRepos] = createSignal([]);
  const [reposLoading, setReposLoading] = createSignal(true);
  const [reposError, setReposError] = createSignal(null);

  const [tasks, setTasks] = createSignal([]);
  const [tasksLoading, setTasksLoading] = createSignal(true);
  const [tasksError, setTasksError] = createSignal(null);
  const [taskReload, setTaskReload] = createSignal(0);
  const [projectTaskCounts, setProjectTaskCounts] = createSignal({});

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

  // Center panel modes: 'task' | 'new-task' | 'projects'
  const [centerMode, setCenterMode] = createSignal('task');
  const [leftOpen, setLeftOpen] = createSignal(true);
  const [rightOpen, setRightOpen] = createSignal(true);
  const [leftWidth, setLeftWidth] = createSignal(
    readStoredNumber('gilir:panel:left', 312, LEFT_MIN, LEFT_MAX),
  );
  const [rightWidth, setRightWidth] = createSignal(
    readStoredNumber('gilir:panel:right', 300, RIGHT_MIN, RIGHT_MAX),
  );
  const [draggingSide, setDraggingSide] = createSignal(null);

  // Mobile navigation layer: 'queue' | 'center' | 'inspector'
  const [mobileLayer, setMobileLayer] = createSignal('queue');
  const [addingRepo, setAddingRepo] = createSignal(false);
  const [notice, setNotice] = createSignal('');

  let noticeTimer;
  let projectAbort = null;
  let repoAbort = null;
  let lastTaskQuery = null;
  let lastDetailSelection = null;
  // Task IDs are never reused, so a confirmed deletion is remembered for the
  // session: payloads captured before the delete cannot replay the row in the
  // queue, project counts, or dependency picker.
  const deletedTaskIds = new Set();

  function forgetTask(taskId) {
    const key = String(taskId);
    deletedTaskIds.add(key);
    setTasks((previous) => {
      if (!previous.some((task) => String(task.id) === key)) return previous;
      return previous.filter((task) => String(task.id) !== key);
    });
  }

  const hasRepos = createMemo(() => repos().length > 0);
  const hasFilters = createMemo(() =>
    Boolean(
      filters.project || filters.repo || filters.status || filters.priority || filters.marking,
    ),
  );

  const showInspector = createMemo(
    () => centerMode() === 'task' && selectedTaskId() != null,
  );

  const watcherWarning = createMemo(() =>
    daemon()?.watcher_error
      ? { title: 'OpenCode warning.', body: daemon()?.watcher_error }
      : daemonError()
        ? { title: 'Queue status unavailable.', body: daemonError() }
        : null,
  );

  const reviewTasks = createMemo(() =>
    tasks().filter((item) => item.status === 'in-review'),
  );

  function startResize(side, event) {
    event.preventDefault();
    const startX = event.clientX;
    const startWidth = side === 'left' ? leftWidth() : rightWidth();
    setDraggingSide(side);

    const onMove = (moveEvent) => {
      const delta = moveEvent.clientX - startX;
      if (side === 'left') {
        const next = Math.max(LEFT_MIN, Math.min(LEFT_MAX, startWidth + delta));
        setLeftWidth(next);
      } else {
        const next = Math.max(RIGHT_MIN, Math.min(RIGHT_MAX, startWidth - delta));
        setRightWidth(next);
      }
    };

    const onUp = () => {
      setDraggingSide(null);
      if (side === 'left') writeStoredValue('gilir:panel:left', leftWidth());
      else writeStoredValue('gilir:panel:right', rightWidth());
      window.removeEventListener('pointermove', onMove);
      window.removeEventListener('pointerup', onUp);
    };

    window.addEventListener('pointermove', onMove);
    window.addEventListener('pointerup', onUp);
  }

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
      setProjects(asArray(data).filter((project) => project && typeof project === 'object'));
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

  // Unfiltered task counts while the Projects & Repos panel is open.
  createEffect(
    () => ({ active: centerMode() === 'projects', reload: taskReload() }),
    ({ active }) => {
      if (!active) {
        setProjectTaskCounts({});
        return;
      }

      const controller = new AbortController();
      let cancelled = false;
      (async () => {
        try {
          const data = await command('task.list', {}, { signal: controller.signal });
          if (cancelled) return;
          const counts = {};
          for (const task of asArray(data)) {
            if (!task || task.project_id == null) continue;
            if (deletedTaskIds.has(String(task.id))) continue;
            const key = String(task.project_id);
            counts[key] = (counts[key] || 0) + 1;
          }
          setProjectTaskCounts(counts);
        } catch (error) {
          if (cancelled || isAbortError(error)) return;
          setProjectTaskCounts({});
        }
      })();
      return () => {
        cancelled = true;
        controller.abort();
      };
    },
  );

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
    () => ({
      project: filters.project,
      repo: filters.repo,
      projects: projects(),
      repos: repos(),
      settling: projectsLoading() || reposLoading(),
    }),
    ({ project, repo, projects: projectList, repos: repoList, settling }) => {
      if (settling) return;
      const projectGone =
        project && !projectList.some((item) => String(item.id) === String(project));
      const repoGone = repo && !repoList.some((item) => String(item.id) === String(repo));
      if (!projectGone && !repoGone) return;
      setFilters((draft) => {
        if (projectGone) draft.project = '';
        if (repoGone) draft.repo = '';
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
              .filter((task) => !deletedTaskIds.has(String(task?.id)))
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
      let stopped = false;
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
          if (String(selectedTaskId() ?? '') !== String(taskId)) return;
          // IDs are never reused, so not_found is definitive: keep polling only
          // while the task exists, and drop a snapshot that can no longer be
          // served instead of reporting it as a paused live update.
          if (error instanceof CommandError && error.code === 'not_found') {
            stopped = true;
            setDetail(null);
            setDetailError(null);
            setDetailLoading(false);
            return;
          }
          setDetailError(errorText(error));
        } finally {
          if (!cancelled) setDetailLoading(false);
        }
      };

      const tick = async () => {
        await load();
        if (!cancelled && !stopped) timer = setTimeout(tick, POLL_INTERVAL);
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
                .filter((item) => !deletedTaskIds.has(String(item?.id)))
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
      openAddRepo();
      return;
    }
    setCenterMode('new-task');
    setMobileLayer('center');
  }

  function openAddRepo() {
    setCenterMode('projects');
    setAddingRepo(true);
    setMobileLayer('center');
  }

  function openProjectsPanel() {
    if (centerMode() === 'projects') {
      setCenterMode('task');
      setAddingRepo(false);
    } else {
      setCenterMode('projects');
      setMobileLayer('center');
    }
  }

  function primaryAction() {
    if (hasRepos()) openNewTask();
    else openAddRepo();
  }

  async function handleRepoAdded() {
    setAddingRepo(false);
    refreshTasks();
    notify('Repository added');
    await Promise.all([loadRepos(), loadProjects()]);
  }

  async function handleTaskCreated(created) {
    setCenterMode('task');
    if (created?.id != null) {
      setSelectedTaskId(created.id);
    } else {
      setMobileLayer('queue');
    }
    refreshTasks();
    notify('Task created');
  }

  async function handleProjectCreated() {
    await loadProjects();
    notify('Project created');
  }

  async function handleDeleteTask(taskId) {
    await command('task.delete', { id: taskId });
    // Evict before refetching so the deleted row disappears at once; the
    // tombstone keeps any in-flight list response from putting it back.
    forgetTask(taskId);
    if (String(selectedTaskId() ?? '') === String(taskId)) closeTask();
    refreshAfterTaskChange();
    notify('Task deleted');
  }

  async function handleLinkRepo(repoId, projectId) {
    await command('repo.link', { repo_id: repoId, project_id: projectId });
    refreshTasks();
    notify('Repository linked');
    await loadRepos();
  }

  async function handleUnlinkRepo(repoId, projectId) {
    await command('repo.unlink', { repo_id: repoId, project_id: projectId });
    refreshTasks();
    notify('Repository unlinked');
    await loadRepos();
  }

  async function handleRemoveRepo(repoId) {
    await command('repo.remove', { id: repoId });
    refreshTasks();
    notify('Repository removed');
    await loadRepos();
  }

  async function handleRenameProject(projectId, name) {
    await command('project.rename', { id: projectId, name });
    refreshTasks();
    notify('Project renamed');
    await loadProjects();
  }

  async function handleRemoveProject(projectId) {
    await command('project.remove', { id: projectId });
    refreshTasks();
    notify('Project removed');
    await Promise.all([loadProjects(), loadRepos()]);
  }

  function viewProjectTasks(projectId) {
    setFilters((draft) => {
      draft.project = String(projectId);
      draft.repo = '';
    });
    setCenterMode('task');
    setMobileLayer('queue');
  }

  function openTask(id) {
    setCenterMode('task');
    setSelectedTaskId(id);
    setMobileLayer('center');
  }

  function closeTask() {
    setSelectedTaskId(null);
    setDetail(null);
    setDetailError(null);
    setDetailLoading(false);
    setMobileLayer('queue');
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
        daemon={daemon()}
        hasRepos={hasRepos()}
        notice={notice()}
        leftOpen={leftOpen()}
        rightOpen={rightOpen()}
        canShowRight={showInspector()}
        onToggleLeft={() => setLeftOpen((open) => !open)}
        onToggleRight={() => setRightOpen((open) => !open)}
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

      <Show when={projectsError()}>
        <div class="banner warning" role="alert">
          <span>Project list refresh failed. {projectsError()}</span>
          <button class="btn btn-ghost btn-sm" type="button" onClick={loadProjects}>
            Retry
          </button>
        </div>
      </Show>

      <Show when={reposError()}>
        <div class="banner warning" role="alert">
          <span>Repository list refresh failed. {reposError()}</span>
          <button class="btn btn-ghost btn-sm" type="button" onClick={loadRepos}>
            Retry
          </button>
        </div>
      </Show>

      <main
        id="main"
        class={[
          'workbench',
          {
            'mobile-center-open': mobileLayer() === 'center',
            'mobile-inspector-open': mobileLayer() === 'inspector',
          },
        ]}
      >
        <Show when={leftOpen()}>
          <aside
            class="queue-panel"
            style={{ width: `${leftWidth()}px` }}
            aria-label="Queue sidebar"
          >
            <Filters
              projects={projects()}
              repos={repos()}
              filters={filters}
              taskCount={tasks().length}
              loading={tasksLoading()}
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
              selectedTaskId={centerMode() === 'task' ? selectedTaskId() : null}
              onOpen={openTask}
              onNewTask={openNewTask}
              onAddRepo={openAddRepo}
              onClearFilters={clearFilters}
              onRetry={refreshTasks}
            />

            <footer class="queue-panel-foot">
              <button
                type="button"
                class={['panel-foot-link', { active: centerMode() === 'projects' }]}
                onClick={openProjectsPanel}
              >
                <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                  <path
                    d="M2.25 4.25C2.25 3.42 2.92 2.75 3.75 2.75H6.4L7.9 4.5H12.25C13.08 4.5 13.75 5.17 13.75 6V11.75C13.75 12.58 13.08 13.25 12.25 13.25H3.75C2.92 13.25 2.25 12.58 2.25 11.75V4.25Z"
                    stroke="currentColor"
                    stroke-width="1.35"
                    stroke-linejoin="round"
                  />
                </svg>
                <span>Projects & Repos</span>
                <span class="panel-foot-meta">
                  {repos().length} · {projects().length}
                </span>
              </button>
            </footer>
          </aside>

          <div
            class={['panel-resizer', { dragging: draggingSide() === 'left' }]}
            role="separator"
            aria-orientation="vertical"
            aria-label="Resize queue panel"
            onPointerDown={(event) => startResize('left', event)}
          />
        </Show>

        <Show when={centerMode() === 'projects'}>
          <ProjectsReposView
            projects={projects()}
            repos={repos()}
            projectTaskCounts={projectTaskCounts()}
            projectsLoading={projectsLoading()}
            reposLoading={reposLoading()}
            projectsError={projectsError()}
            reposError={reposError()}
            addingRepo={addingRepo()}
            onBack={() => {
              setCenterMode('task');
              setMobileLayer('queue');
            }}
            onToggleAddRepo={setAddingRepo}
            onRepoAdded={handleRepoAdded}
            onProjectCreated={handleProjectCreated}
            onLinkRepo={handleLinkRepo}
            onUnlinkRepo={handleUnlinkRepo}
            onRemoveRepo={handleRemoveRepo}
            onRenameProject={handleRenameProject}
            onRemoveProject={handleRemoveProject}
            onViewProjectTasks={viewProjectTasks}
          />
        </Show>

        <Show when={centerMode() === 'new-task'}>
          <NewTaskPane
            repos={repos()}
            projects={projects()}
            defaults={newTaskDefaults()}
            defaultProjectId={filters.project}
            onCancel={() => {
              setCenterMode('task');
              setMobileLayer('queue');
            }}
            onCreated={handleTaskCreated}
          />
        </Show>

        <Show when={centerMode() === 'task'}>
          <Show
            when={selectedTaskId() != null}
            fallback={
              <section class="center-panel" aria-label="Workspace overview">
                <Show
                  when={hasRepos() || reposLoading()}
                  fallback={
                    <>
                      <header class="center-header">
                        <div class="center-header-inner">
                          <div class="center-head-main">
                            <h2 class="center-title">Connect your first repository</h2>
                            <div class="center-meta">
                              <span>
                                Add a local Git repository so Gilir can dispatch ready tasks to
                                OpenCode.
                              </span>
                            </div>
                          </div>
                        </div>
                      </header>
                      <div class="center-scroll">
                        <div class="stream-column">
                          <AddRepoForm
                            autofocus
                            projects={projects()}
                            onAdded={handleRepoAdded}
                          />
                        </div>
                      </div>
                    </>
                  }
                >
                  <div class="center-scroll">
                    <div class="overview-shell">
                      <div class="overview-head">
                        <h2>
                          {hasFilters() && tasks().length === 0
                            ? 'No matching tasks'
                            : 'Dispatch queue'}
                        </h2>
                        <p>
                          {hasFilters() && tasks().length === 0
                            ? 'Adjust or reset your filters in the queue panel to inspect other tasks.'
                            : 'Select a task from the queue panel to inspect its description, OpenCode sessions, and activity, or queue new work.'}
                        </p>
                        <div class="overview-actions">
                          <Show
                            when={hasFilters() && tasks().length === 0}
                            fallback={
                              <button class="btn btn-primary" type="button" onClick={openNewTask}>
                                New task
                              </button>
                            }
                          >
                            <button class="btn btn-secondary" type="button" onClick={clearFilters}>
                              Clear filters
                            </button>
                          </Show>
                        </div>
                      </div>

                      <Show when={reviewTasks().length > 0}>
                        <div class="overview-block">
                          <div class="overview-block-head">
                            <h3>Ready for review</h3>
                            <span class="muted-small">{reviewTasks().length}</span>
                          </div>
                          <ul class="overview-list">
                            <For each={reviewTasks()}>
                              {(item) => (
                                <li class="overview-row">
                                  <button
                                    type="button"
                                    class="overview-row-btn"
                                    onClick={() => openTask(item.id)}
                                  >
                                    <span class="task-group-dot status-in-review" aria-hidden="true" />
                                    <span class="task-id">#{item.id}</span>
                                    <span class="overview-row-title">
                                      {item.title || 'Untitled task'}
                                    </span>
                                    <span class="overview-row-meta">
                                      {formatRelativeTime(item.updated_at)}
                                    </span>
                                  </button>
                                </li>
                              )}
                            </For>
                          </ul>
                        </div>
                      </Show>
                    </div>
                  </div>
                </Show>
              </section>
            }
          >
            <TaskDetailPane
              taskId={selectedTaskId()}
              detail={detail()}
              loading={detailLoading()}
              error={detailError()}
              projects={projects()}
              onBack={() => setMobileLayer('queue')}
              onClose={closeTask}
              onOpenMobileInspector={() => setMobileLayer('inspector')}
              onRefresh={refreshAfterTaskChange}
              onDelete={handleDeleteTask}
              notify={notify}
            />
          </Show>
        </Show>

        <Show when={showInspector() && rightOpen()}>
          <div
            class={[
              'panel-resizer inspector-resizer',
              { dragging: draggingSide() === 'right' },
            ]}
            role="separator"
            aria-orientation="vertical"
            aria-label="Resize inspector panel"
            onPointerDown={(event) => startResize('right', event)}
          />

          <TaskInspectorPanel
            width={rightWidth()}
            taskId={selectedTaskId()}
            detail={detail()}
            repos={repos()}
            blockerSource={blockerSource()}
            onCloseMobileInspector={() => setMobileLayer('center')}
            onRefresh={refreshAfterTaskChange}
            notify={notify}
          />
        </Show>
      </main>
    </div>
  );
}
