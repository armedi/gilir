import { For, Show, createUniqueId } from 'solid-js';

export default function Header(props) {
  const projectSelectId = createUniqueId();

  return (
    <header class="app-header">
      <div class="header-inner">
        <div class="brand">
          <span class="brand-mark" aria-hidden="true">
            G
          </span>
          <div>
            <h1 class="brand-name">Gilir</h1>
            <div class="brand-sub">Local task queue</div>
          </div>
        </div>
        <Show when={props.projects.length > 0}>
          <div class="header-actions">
            <div class="header-project">
              <label class="header-label" for={projectSelectId}>
                Project
              </label>
              <select
                id={projectSelectId}
                class="select select-compact"
                value={props.projectId == null ? '' : String(props.projectId)}
                onChange={(event) => props.onProjectChange(event.currentTarget.value)}
              >
                <For each={props.projects}>
                  {(project) => <option value={String(project.id)}>{project.name}</option>}
                </For>
              </select>
            </div>
            <button class="btn btn-secondary" type="button" onClick={props.onNewProject}>
              New project
            </button>
          </div>
        </Show>
      </div>
    </header>
  );
}
