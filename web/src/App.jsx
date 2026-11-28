import {
  Show,
  createEffect,
  createMemo,
  createSignal,
  createStore,
  onCleanup,
  onSettled,
} from 'solid-js';
import { useMatch, useNavigate, useRouteMatches } from '@solidjs/router';
import { CommandError, command } from './api';
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
import Filters from './components/Filters';
import Header from './components/Header';
import TaskInspectorPanel from './components/TaskInspectorPanel';
import TaskListPanel from './components/TaskListPanel';
import { PATHS, ShellContext, canonicalTaskId } from './shell';

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

// App is the router's root layout: it mounts once, owns live updates and
// the panel chrome, and renders the matched page in its
// center slot. The URL is the single source of truth for which page is shown
// and which task is selected; everything that is presentation-only (panel
// widths, inspector tab, inline edit state) stays in component-local state.
export default function App(props) {
  const navigate = useNavigate();
  const matches = useRouteMatches();
  const addRepoMatch = useMatch(() => PATHS.addRepo);

  const screen = createMemo(() => matches().at(-1)?.route.info?.screen ?? 'not-found');
  const selectedTaskId = createMemo(() => canonicalTaskId(props.params.taskId));
  const onTaskRoute = createMemo(() => screen() === 'task');

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

  const [service, setService] = createSignal(null);
  const [serviceError, setServiceError] = createSignal(null);
  const [serviceReload, setServiceReload] = createSignal(0);
  const [liveReady, setLiveReady] = createSignal(false);
  const [liveError, setLiveError] = createSignal(null);

  const [detail, setDetail] = createSignal(null);
  const [detailError, setDetailError] = createSignal(null);
  // Keyed to the ID it describes so navigating from a missing task does not
  // flash "Task not found" for the next one before the effect runs.
  const [missingTaskId, setMissingTaskId] = createSignal(null);
  const [detailReload, setDetailReload] = createSignal(0);

  const detailMissing = createMemo(() => {
    const taskId = selectedTaskId();
    return taskId != null && String(missingTaskId()) === String(taskId);
  });

  // Stale snapshots are never rendered: a request that lands after navigation is
  // dropped here even if the request guard let it through.
  const currentDetail = createMemo(() => {
    const value = detail();
    if (!value) return null;
    return String(value.task?.id) === String(selectedTaskId() ?? '') ? value : null;
  });

  const blockerProject = createMemo(() => currentDetail()?.task?.project_id);
  const [blockerSource, setBlockerSource] = createSignal({
    project: null,
    tasks: [],
    loading: false,
    error: null,
  });
  const currentBlockerSource = createMemo(() => {
    const source = blockerSource();
    return source.project === blockerProject() ? source : { tasks: [], loading: true, error: null };
  });
  const [blockerReload, setBlockerReload] = createSignal(0);

  const [filters, setFilters] = createStore({ ...EMPTY_FILTERS });
  const [markingInput, setMarkingInput] = createSignal('');
  const [knownMarkings, setKnownMarkings] = createSignal([]);

  const [leftOpen, setLeftOpen] = createSignal(true);
  const [rightOpen, setRightOpen] = createSignal(true);
  const [leftWidth, setLeftWidth] = createSignal(
    readStoredNumber('gilir:panel:left', 312, LEFT_MIN, LEFT_MAX),
  );
  const [rightWidth, setRightWidth] = createSignal(
    readStoredNumber('gilir:panel:right', 300, RIGHT_MIN, RIGHT_MAX),
  );
  const [draggingSide, setDraggingSide] = createSignal(null);

  // Presentation-only mobile layer. The URL decides whether the queue or the
  // center view is the destination; the inspector is a per-task overlay for
  // the task it was opened on only.
  const [mobileInspectorFor, setMobileInspectorFor] = createSignal(null);
  const addingRepo = createMemo(() => Boolean(addRepoMatch()));
  const [notice, setNotice] = createSignal('');

  // Returning to a task (A -> B -> A, or Back from the queue) must not reopen
  // an inspector that was left open on an earlier visit.
  createEffect(
    () => [screen(), selectedTaskId()],
    () => setMobileInspectorFor(null),
    { defer: true },
  );

  let noticeTimer;
  let retryLiveUpdates;
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
    () => onTaskRoute() && selectedTaskId() != null && !detailMissing(),
  );

  const mobileLayer = createMemo(() => {
    if (screen() === 'queue') return 'queue';
    const taskId = selectedTaskId();
    if (taskId != null && String(mobileInspectorFor()) === String(taskId)) return 'inspector';
    return 'center';
  });

  const watcherWarning = createMemo(() =>
    liveError()
      ? { title: 'Live updates unavailable.', body: liveError() }
      : service()?.watcher_error
      ? { title: 'OpenCode warning.', body: service()?.watcher_error }
      : serviceError()
        ? { title: 'Queue status unavailable.', body: serviceError() }
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
    let controller;
    let after = null;
    let retry;
    let delay = 1000;

    async function watchChanges(poll) {
      try {
        const next = await command('web.changes', { after }, {
          signal: poll.signal,
          timeoutMs: after ? 35000 : undefined,
        });
        if (poll.signal.aborted) return;
        if (
          typeof next?.started_at !== 'string' ||
          !Number.isSafeInteger(next.activity) ||
          !Number.isSafeInteger(next.watcher)
        ) {
          throw new Error('The Gilir API returned an invalid change cursor.');
        }
        const reset = after?.started_at !== next.started_at;
        const activityChanged = reset || after.activity !== next.activity;
        const watcherChanged = reset || after.watcher !== next.watcher;
        after = next;
        delay = 1000;
        if (liveReady()) {
          if (activityChanged) refreshAfterTaskChange();
          if (activityChanged || watcherChanged || serviceError()) refreshService();
          if (!activityChanged && tasksError()) refreshTasks();
          if (!activityChanged && detailError()) setDetailReload((value) => value + 1);
          if (!activityChanged && blockerProject() != null && blockerSource().error) {
            setBlockerReload((value) => value + 1);
          }
        }
        setLiveError(null);
        setLiveReady(true);
        watchChanges(poll);
      } catch (error) {
        if (poll.signal.aborted) return;
        after = null;
        setLiveError(errorText(error));
        setLiveReady(true);
        retry = setTimeout(retryLiveUpdates, delay);
        delay = Math.min(delay * 2, 15000);
      }
    }

    retryLiveUpdates = () => {
      if (retry === undefined || !controller) return;
      clearTimeout(retry);
      retry = undefined;
      watchChanges(controller);
    };

    function pauseWatching() {
      clearTimeout(retry);
      retry = undefined;
      controller?.abort();
      controller = undefined;
    }

    function updateVisibility() {
      // Hidden tabs should not occupy the origin's limited HTTP/1.1 connections.
      if (document.visibilityState === 'hidden') {
        pauseWatching();
      } else if (!controller) {
        controller = new AbortController();
        watchChanges(controller);
      }
    }

    document.addEventListener('visibilitychange', updateVisibility);
    updateVisibility();
    return () => {
      document.removeEventListener('visibilitychange', updateVisibility);
      pauseWatching();
      retryLiveUpdates = undefined;
      projectAbort?.abort();
      repoAbort?.abort();
    };
  });

  // Unfiltered task counts while the Projects & Repos panel is open.
  createEffect(
    () => ({ active: screen() === 'projects', reload: taskReload() }),
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
      return { project, repo, status, priority, marking, ready: liveReady() };
    },
    ({ project, repo, status, priority, marking, ready }) => {
      if (!ready) return;
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
        setTasksLoading(true);
      }

      setTasksError(null);
      const controller = new AbortController();
      let cancelled = false;

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

      load();
      return () => {
        cancelled = true;
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

  createEffect(() => [serviceReload(), liveReady()], ([, ready]) => {
    if (!ready) return;
    const controller = new AbortController();
    let cancelled = false;

    const load = async () => {
      try {
        const data = await command('service.status', {}, { signal: controller.signal });
        if (cancelled) return;
        setService(data && typeof data === 'object' ? data : null);
        setServiceError(null);
      } catch (error) {
        if (cancelled || isAbortError(error)) return;
        setServiceError(errorText(error));
      }
    };

    load();
    return () => {
      cancelled = true;
      controller.abort();
    };
  });

  createEffect(
    () => [selectedTaskId(), detailReload(), liveReady()],
    ([taskId, , ready]) => {
      if (!ready) return;
      if (taskId == null) {
        lastDetailSelection = null;
        setDetail(null);
        setDetailError(null);
        setMissingTaskId(null);
        return;
      }

      if (String(lastDetailSelection) !== String(taskId)) {
        lastDetailSelection = taskId;
        setDetail(null);
        setDetailError(null);
        setMissingTaskId(null);
      }

      // A confirmed deletion is definitive even before the server answers.
      if (deletedTaskIds.has(String(taskId))) {
        setDetail(null);
        setDetailError(null);
        setMissingTaskId(taskId);
        return;
      }

      const controller = new AbortController();
      let cancelled = false;

      const load = async () => {
        try {
          const data = await command('task.show', { id: taskId }, { signal: controller.signal });
          if (cancelled || String(selectedTaskId() ?? '') !== String(taskId)) return;
          const normalized = normalizeDetail(data);
          if (!normalized) throw new Error('The task details were not returned.');
          setDetail(normalized);
          setMissingTaskId(null);
          setDetailError(null);
        } catch (error) {
          if (cancelled || isAbortError(error)) return;
          if (String(selectedTaskId() ?? '') !== String(taskId)) return;
          // Drop the snapshot and show the not-found state.
          if (
            error instanceof CommandError &&
            (error.code === 'not_found' || error.code === 'invalid')
          ) {
            setDetail(null);
            setDetailError(null);
            setMissingTaskId(taskId);
            return;
          }
          setDetailError(errorText(error));
        }
      };

      load();
      return () => {
        cancelled = true;
        controller.abort();
      };
    },
  );

  createEffect(
    () => [blockerProject(), blockerReload()],
    ([project]) => {
      if (project == null) return;

      const controller = new AbortController();
      let cancelled = false;
      setBlockerSource((previous) =>
        previous.project === project
          ? previous
          : { project, tasks: [], loading: true, error: null },
      );
      (async () => {
        try {
          const data = await command(
            'task.list',
            { project_id: project },
            { signal: controller.signal },
          );
          if (cancelled || blockerProject() !== project) return;
          const list = sortTasks(
            asArray(data)
              .filter((item) => !deletedTaskIds.has(String(item?.id)))
              .map((item) => normalizeTask(item))
              .filter(Boolean),
          );
          setBlockerSource((previous) => ({
            project,
            tasks: mergeTasks(previous.tasks, list),
            loading: false,
            error: null,
          }));
        } catch (error) {
          if (cancelled || blockerProject() !== project || isAbortError(error)) return;
          setBlockerSource((previous) => ({ ...previous, loading: false, error: errorText(error) }));
        }
      })();
      return () => {
        cancelled = true;
        controller.abort();
      };
    },
  );

  createEffect(
    () => [currentDetail()?.task, tasks()],
    ([current, list]) => {
      const discovered = new Set(uniqueMarkings(current ? [current] : []));
      for (const marking of uniqueMarkings(list)) discovered.add(marking);
      if (discovered.size === 0) return;
      setKnownMarkings((previous) => addMarkings(previous, [...discovered]));
    },
  );

  function refreshService() {
    setServiceReload((value) => value + 1);
  }

  function refreshTasks() {
    setTaskReload((value) => value + 1);
  }

  function refreshAfterTaskChange() {
    setTaskReload((value) => value + 1);
    setDetailReload((value) => value + 1);
    setBlockerReload((value) => value + 1);
  }

  function goToQueue(options) {
    navigate(PATHS.queue, options);
  }

  function closeTask() {
    goToQueue();
  }

  function setAddingRepo(open) {
    // Opening the form is a forward destination. Closing it goes back when it
    // was opened from the management screen, so the two URLs do not collapse
    // into duplicate history entries; a direct load of the form replaces the
    // entry instead of leaving the app.
    if (!open) {
      if (window.history.state?.fromProjects) {
        navigate(-1);
        return;
      }
      navigate(PATHS.projects, { replace: true });
      return;
    }
    navigate(PATHS.addRepo, { state: { fromProjects: true } });
  }

  const primaryHref = createMemo(() => (hasRepos() ? PATHS.newTask : PATHS.addRepo));

  async function handleRepoAdded() {
    if (addingRepo()) {
      // Pop the form entry when it was opened from the management screen;
      // a direct load replaces instead so the app is not left entirely.
      if (window.history.state?.fromProjects) navigate(-1);
      else navigate(PATHS.projects, { replace: true });
    }
    notify('Repository added');
    await Promise.all([loadRepos(), loadProjects()]);
  }

  async function handleTaskCreated(created) {
    // Replace the composer entry so Back cannot reopen a submitted form.
    if (created?.id != null) {
      navigate(`/tasks/${encodeURIComponent(String(created.id))}`, { replace: true });
    } else {
      goToQueue();
    }
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
    if (String(selectedTaskId() ?? '') === String(taskId)) goToQueue({ replace: true });
    notify('Task deleted');
  }

  async function handleLinkRepo(repoId, projectId) {
    await command('repo.link', { repo_id: repoId, project_id: projectId });
    notify('Repository linked');
    await loadRepos();
  }

  async function handleUnlinkRepo(repoId, projectId) {
    await command('repo.unlink', { repo_id: repoId, project_id: projectId });
    notify('Repository unlinked');
    await loadRepos();
  }

  async function handleRemoveRepo(repoId) {
    await command('repo.remove', { id: repoId });
    notify('Repository removed');
    await loadRepos();
  }

  async function handleRenameProject(projectId, name) {
    await command('project.rename', { id: projectId, name });
    notify('Project renamed');
    await loadProjects();
  }

  async function handleRemoveProject(projectId) {
    await command('project.remove', { id: projectId });
    notify('Project removed');
    await Promise.all([loadProjects(), loadRepos()]);
  }

  function viewProjectTasks(projectId) {
    setFilters((draft) => {
      draft.project = String(projectId);
      draft.repo = '';
    });
    goToQueue();
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

  const shell = {
    projects,
    projectsLoading,
    projectsError,
    repos,
    reposLoading,
    reposError,
    projectTaskCounts,
    tasks,
    filters,
    hasRepos,
    hasFilters,
    reviewTasks,
    newTaskDefaults,
    clearFilters,
    selectedTaskId,
    detail: currentDetail,
    detailError,
    detailMissing,
    addingRepo,
    notify,
    goToQueue,
    closeTask,
    setAddingRepo,
    openMobileInspector: () => setMobileInspectorFor(selectedTaskId()),
    retryTaskDetail: () => setDetailReload((value) => value + 1),
    handleDeleteTask,
    handleTaskCreated,
    handleRepoAdded,
    handleProjectCreated,
    handleLinkRepo,
    handleUnlinkRepo,
    handleRemoveRepo,
    handleRenameProject,
    handleRemoveProject,
    viewProjectTasks,
  };

  return (
    <ShellContext value={shell}>
      <div class="app">
        <a class="skip-link" href="#main" target="_self">
          Skip to content
        </a>

        <Header
          service={service()}
          hasRepos={hasRepos()}
          notice={notice()}
          leftOpen={leftOpen()}
          rightOpen={rightOpen()}
          canShowRight={showInspector()}
          primaryHref={primaryHref()}
          onToggleLeft={() => setLeftOpen((open) => !open)}
          onToggleRight={() => setRightOpen((open) => !open)}
          notify={notify}
        />

        <Show when={watcherWarning()}>
          {(warning) => (
            <div class="watcher-banner" role="alert">
              <span class="watcher-banner-dot" aria-hidden="true" />
              <span class="watcher-banner-text">
                <strong>{warning().title}</strong> {warning().body}
              </span>
              <button
                class="btn btn-ghost btn-sm"
                type="button"
                onClick={() => (liveError() ? retryLiveUpdates?.() : refreshService())}
              >
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
                selectedTaskId={selectedTaskId()}
                onClearFilters={clearFilters}
                onRetry={refreshTasks}
              />

              <footer class="queue-panel-foot">
                <a
                  href={screen() === 'projects' ? PATHS.queue : PATHS.projects}
                  class={['panel-foot-link', { active: screen() === 'projects' }]}
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
                </a>
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

          {props.children}

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
              detail={currentDetail()}
              repos={repos()}
              blockerSource={currentBlockerSource()}
              onCloseMobileInspector={() => setMobileInspectorFor(null)}
              notify={notify}
            />
          </Show>
        </main>
      </div>
    </ShellContext>
  );
}
