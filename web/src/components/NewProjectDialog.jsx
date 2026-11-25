import { createUniqueId } from 'solid-js';
import Modal from './Modal';
import ProjectForm from './ProjectForm';

export default function NewProjectDialog(props) {
  const dialogTitleId = createUniqueId();

  return (
    <Modal
      open={props.open}
      class="modal-dialog"
      labelledby={dialogTitleId}
      onClose={props.onClose}
    >
      <div class="dialog-header">
        <h2 id={dialogTitleId}>New project</h2>
        <button
          class="icon-button"
          type="button"
          aria-label="Close new project dialog"
          onClick={props.onClose}
        >
          ×
        </button>
      </div>
      <div class="dialog-body">
        <ProjectForm autofocus onCreated={props.onCreated} />
      </div>
    </Modal>
  );
}
