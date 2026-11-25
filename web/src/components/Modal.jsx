import { createEffect } from 'solid-js';

export default function Modal(props) {
  let dialog;
  let panel;

  createEffect(() => {
    if (!dialog) return;
    if (props.open) {
      if (!dialog.open) dialog.showModal();
    } else if (dialog.open) {
      dialog.close();
    }
  });

  function handleCancel(event) {
    event.preventDefault();
    props.onClose();
  }

  function handleClick(event) {
    if (event.target === dialog) props.onClose();
  }

  return (
    <dialog
      ref={dialog}
      class={`dialog ${props.class || ''}`}
      aria-labelledby={props.labelledby}
      onCancel={handleCancel}
      onClick={handleClick}
    >
      <div ref={panel} class="dialog-panel">
        {props.children}
      </div>
    </dialog>
  );
}
