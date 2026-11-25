import { For, Show } from 'solid-js';
import ProjectForm from './ProjectForm';
import RepoForm from './RepoForm';

export default function SetupPanel(props) {
  const hasProject = () => Boolean(props.project);
  const hasRepos = () => props.repos.length > 0;
  const readyForTask = () => hasProject() && hasRepos();
  const stepState = (step) => {
    if (step === 1) return hasProject() ? 'complete' : 'current';
    if (step === 2) {
      if (!hasProject()) return 'upcoming';
      return hasRepos() ? 'complete' : 'current';
    }
    return readyForTask() ? 'current' : 'upcoming';
  };

  return (
    <div class="setup-shell">
      <section class="panel setup-panel" aria-labelledby="setup-title">
        <div class="setup-header">
          <h2 id="setup-title">Set up your queue</h2>
          <p>Create a project, connect a repository, then add the first task.</p>
        </div>
        <ol class="setup-steps">
          <li class={`setup-step ${stepState(1)}`} aria-current={stepState(1) === 'current' ? 'step' : undefined}>
            <span class="step-marker" aria-hidden="true">
              {stepState(1) === 'complete' ? '✓' : '1'}
            </span>
            <div class="step-body">
              <h3>Project</h3>
              <Show
                when={props.project}
                fallback={
                  <>
                    <p>Name the workspace that will hold your repositories and tasks.</p>
                    <ProjectForm onCreated={props.onProjectCreated} />
                  </>
                }
              >
                {(project) => <p class="setup-value">{project().name}</p>}
              </Show>
            </div>
          </li>

          <li class={`setup-step ${stepState(2)}`} aria-current={stepState(2) === 'current' ? 'step' : undefined}>
            <span class="step-marker" aria-hidden="true">
              {stepState(2) === 'complete' ? '✓' : '2'}
            </span>
            <div class="step-body">
              <h3>Git repository</h3>
              <Show
                when={hasProject()}
                fallback={<p>Create a project first.</p>}
              >
                <Show when={props.reposError}>
                  <div class="inline-alert" role="alert">
                    <span>{props.reposError}</span>
                    <button class="btn btn-ghost btn-sm" type="button" onClick={props.onRetryRepos}>
                      Retry
                    </button>
                  </div>
                </Show>
                <Show
                  when={hasRepos()}
                  fallback={
                    <Show
                      when={!props.reposLoading}
                      fallback={<p>Loading repositories…</p>}
                    >
                      <p>Add a Git repository by its local path.</p>
                      <RepoForm
                        projectId={props.project?.id}
                        onAdded={props.onRepoAdded}
                      />
                    </Show>
                  }
                >
                  <ul class="setup-repo-list">
                    <For each={props.repos}>
                      {(repo) => (
                        <li>
                          <code>{repo.path}</code>
                        </li>
                      )}
                    </For>
                  </ul>
                  <p class="field-hint">Add more repositories from the sidebar after the first task exists.</p>
                </Show>
              </Show>
            </div>
          </li>

          <li class={`setup-step ${stepState(3)}`} aria-current={stepState(3) === 'current' ? 'step' : undefined}>
            <span class="step-marker" aria-hidden="true">
              3
            </span>
            <div class="step-body">
              <h3>First task</h3>
              <Show
                when={readyForTask()}
                fallback={<p>Add a repository to unlock task creation.</p>}
              >
                <p>Create a task and it will appear in the queue.</p>
                <button class="btn btn-primary" type="button" onClick={props.onNewTask}>
                  New task
                </button>
              </Show>
            </div>
          </li>
        </ol>
      </section>
    </div>
  );
}
