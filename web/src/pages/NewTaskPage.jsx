import { Show, createEffect } from 'solid-js';
import { useNavigate } from '@solidjs/router';
import NewTaskPane from '../components/NewTaskPane';
import { PATHS, useShell } from '../shell';

// NewTaskPane snapshots the repo list once when it mounts (its defaults and
// draft restore read `props.repos` untracked), so on a direct load it must not
// mount before the shell's repositories have arrived. Rendering the pane only
// when a repository exists keeps the draft/default behavior identical, and the
// redirect below matches the old "no repos means add one first" rule.
export default function NewTaskPage() {
  const shell = useShell();
  const navigate = useNavigate();

  createEffect(
    () => !shell.reposLoading() && !shell.reposError() && !shell.hasRepos(),
    (shouldAddRepo) => {
      if (shouldAddRepo) navigate(PATHS.addRepo, { replace: true });
    },
  );

  return (
    <Show
      when={shell.hasRepos()}
      fallback={
        <section class="center-panel" aria-label="New task composer">
          <div class="pane-empty" role="status">
            <Show
              when={shell.reposError()}
              fallback={<p>{shell.reposLoading() ? 'Loading repositories…' : 'Loading…'}</p>}
            >
              <h2>Repositories unavailable</h2>
              <p>Tasks need a repository. Check the connection and retry from the banner above.</p>
              <a class="btn btn-secondary" href={PATHS.addRepo}>
                Add repository
              </a>
            </Show>
          </div>
        </section>
      }
    >
      <NewTaskPane
        repos={shell.repos()}
        projects={shell.projects()}
        defaults={shell.newTaskDefaults()}
        defaultProjectId={shell.filters.project}
        onCancel={() => shell.goToQueue()}
        onCreated={shell.handleTaskCreated}
      />
    </Show>
  );
}
