import ProjectsReposView from '../components/ProjectsReposView';
import { useShell } from '../shell';

// /projects and /projects/repos/new share this screen so opening the add
// repository form is a URL change inside one mounted view rather than a mode
// switch.
export default function ProjectsPage() {
  const shell = useShell();

  return (
    <ProjectsReposView
      projects={shell.projects()}
      repos={shell.repos()}
      projectTaskCounts={shell.projectTaskCounts()}
      projectsLoading={shell.projectsLoading()}
      reposLoading={shell.reposLoading()}
      projectsError={shell.projectsError()}
      reposError={shell.reposError()}
      addingRepo={shell.addingRepo()}
      onBack={() => shell.goToQueue()}
      onToggleAddRepo={shell.setAddingRepo}
      onRepoAdded={shell.handleRepoAdded}
      onProjectCreated={shell.handleProjectCreated}
      onLinkRepo={shell.handleLinkRepo}
      onUnlinkRepo={shell.handleUnlinkRepo}
      onRemoveRepo={shell.handleRemoveRepo}
      onRenameProject={shell.handleRenameProject}
      onRemoveProject={shell.handleRemoveProject}
      onViewProjectTasks={shell.viewProjectTasks}
    />
  );
}
