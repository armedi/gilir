import { For, Show, createMemo } from 'solid-js';
import { basename, repoProjectIds } from '../utils';

export default function ProjectsReposView(props) {
  const grouped = createMemo(() => {
    const seen = new Set();
    const groups = props.projects.map((project) => {
      const repos = props.repos.filter((repo) =>
        repoProjectIds(repo).includes(String(project.id)),
      );
      for (const repo of repos) seen.add(String(repo.id));
      return { project, repos };
    });
    const unlinked = props.repos.filter((repo) => !seen.has(String(repo.id)));
    return { groups, unlinked };
  });

  return (
    <section class="panel-surface projects-view" aria-labelledby="projects-view-title">
      <div class="projects-view-head">
        <div>
          <h2 id="projects-view-title" class="projects-view-title">
            Projects &amp; Repos
          </h2>
          <p class="projects-view-sub">
            Every project and the repositories linked to it.
          </p>
        </div>
        <div class="projects-view-actions">
          <button class="btn btn-secondary" type="button" onClick={props.onAddRepo}>
            Add repository
          </button>
          <button class="btn btn-secondary" type="button" onClick={props.onNewProject}>
            New project
          </button>
        </div>
      </div>

      <Show when={props.error}>
        <div class="banner warning" role="alert">
          <span>{props.error}</span>
          <button class="btn btn-ghost btn-sm" type="button" onClick={props.onRetry}>
            Retry
          </button>
        </div>
      </Show>

      <Show when={props.loading && props.projects.length === 0 && props.repos.length === 0}>
        <p class="task-loading" role="status">
          Loading projects and repositories…
        </p>
      </Show>

      <Show
        when={props.projects.length > 0 || props.repos.length > 0}
        fallback={
          <Show when={!props.loading}>
            <div class="task-empty">
              <h3>Nothing here yet</h3>
              <p>Add a repository to create your first project, or start with a project name.</p>
              <div class="empty-actions">
                <button class="btn btn-primary" type="button" onClick={props.onAddRepo}>
                  Add repository
                </button>
                <button class="btn btn-secondary" type="button" onClick={props.onNewProject}>
                  New project
                </button>
              </div>
            </div>
          </Show>
        }
      >
        <ul class="project-cards">
          <For each={grouped().groups}>
            {(entry) => (
              <li class="project-card">
                <div class="project-card-head">
                  <span class="project-card-name">{entry.project.name}</span>
                  <span class="project-card-count">
                    {entry.repos.length}
                    <span class="sr-only"> repositories</span>
                  </span>
                </div>
                <Show
                  when={entry.repos.length > 0}
                  fallback={<p class="muted-small">No repositories linked.</p>}
                >
                  <ul class="project-repo-list">
                    <For each={entry.repos}>
                      {(repo) => (
                        <li class="project-repo" title={repo.path}>
                          <span class="project-repo-name">
                            {basename(repo.path) || repo.path}
                          </span>
                          <span class="project-repo-path">{repo.path}</span>
                        </li>
                      )}
                    </For>
                  </ul>
                </Show>
              </li>
            )}
          </For>
          <Show when={grouped().unlinked.length > 0}>
            <li class="project-card">
              <div class="project-card-head">
                <span class="project-card-name">Unlinked repositories</span>
                <span class="project-card-count">{grouped().unlinked.length}</span>
              </div>
              <ul class="project-repo-list">
                <For each={grouped().unlinked}>
                  {(repo) => (
                    <li class="project-repo" title={repo.path}>
                      <span class="project-repo-name">{basename(repo.path) || repo.path}</span>
                      <span class="project-repo-path">{repo.path}</span>
                    </li>
                  )}
                </For>
              </ul>
            </li>
          </Show>
        </ul>
        <Show when={props.projects.length > 0 && props.repos.length > 0}>
          <p class="muted-small projects-view-note">
            Linking and unlinking repositories arrives in a follow-up. Projects shown:{' '}
            {props.projects.map((project) => project.name).join(', ')}.
          </p>
        </Show>
      </Show>
    </section>
  );
}
