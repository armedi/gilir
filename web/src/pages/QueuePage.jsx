import { For, Show } from 'solid-js';
import { formatRelativeTime } from '../utils';
import AddRepoForm from '../components/AddRepoForm';
import { PATHS, taskPath, useShell } from '../shell';

// The dispatch queue landing view. It also owns the first-run onboarding that
// appears while no repository is connected, matching the shell's historical
// behavior of showing the add-repository form in the center panel.
export default function QueuePage() {
  const shell = useShell();

  return (
    <section class="center-panel" aria-label="Workspace overview">
      <Show
        when={shell.hasRepos() || shell.reposLoading()}
        fallback={
          <>
            <header class="center-header">
              <div class="center-header-inner">
                <div class="center-head-main">
                  <h2 class="center-title">Connect your first repository</h2>
                  <div class="center-meta">
                    <span>
                      Add a local Git repository so Gilir can dispatch ready tasks to OpenCode.
                    </span>
                  </div>
                </div>
              </div>
            </header>
            <div class="center-scroll">
              <div class="stream-column">
                <AddRepoForm autofocus projects={shell.projects()} onAdded={shell.handleRepoAdded} />
              </div>
            </div>
          </>
        }
      >
        <div class="center-scroll">
          <div class="overview-shell">
            <div class="overview-head">
              <h2>
                {shell.hasFilters() && shell.tasks().length === 0
                  ? 'No matching tasks'
                  : 'Dispatch queue'}
              </h2>
              <p>
                {shell.hasFilters() && shell.tasks().length === 0
                  ? 'Adjust or reset your filters in the queue panel to inspect other tasks.'
                  : 'Select a task from the queue panel to inspect its body, OpenCode sessions, and activity, or queue new work.'}
              </p>
              <div class="overview-actions">
                <Show
                  when={shell.hasFilters() && shell.tasks().length === 0}
                  fallback={
                    <a class="btn btn-primary" href={PATHS.newTask}>
                      New task
                    </a>
                  }
                >
                  <button class="btn btn-secondary" type="button" onClick={shell.clearFilters}>
                    Clear filters
                  </button>
                </Show>
              </div>
            </div>

            <Show when={shell.reviewTasks().length > 0}>
              <div class="overview-block">
                <div class="overview-block-head">
                  <h3>Ready for review</h3>
                  <span class="muted-small">{shell.reviewTasks().length}</span>
                </div>
                <ul class="overview-list">
                  <For each={shell.reviewTasks()}>
                    {(item) => (
                      <li class="overview-row">
                        <a href={taskPath(item.id)} class="overview-row-btn">
                          <span class="task-group-dot status-in-review" aria-hidden="true" />
                          <span class="task-id">{item.id}</span>
                          <span class="overview-row-title">{item.title || 'Untitled task'}</span>
                          <span class="overview-row-meta">
                            {formatRelativeTime(item.updated_at)}
                          </span>
                        </a>
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
  );
}
