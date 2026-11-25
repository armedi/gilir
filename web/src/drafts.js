const PREFIX = 'gilir:draft:';
export const PROJECT_STORAGE_KEY = 'gilir:project';

export function draftKey(...parts) {
  return `${PREFIX}${parts.map((part) => encodeURIComponent(String(part ?? 'none'))).join(':')}`;
}

export function loadDraft(key) {
  try {
    const raw = window.localStorage.getItem(key);
    return raw == null ? null : JSON.parse(raw);
  } catch {
    return null;
  }
}

export function saveDraft(key, value) {
  try {
    window.localStorage.setItem(key, JSON.stringify(value));
  } catch {
    return;
  }
}

export function clearDraft(key) {
  try {
    window.localStorage.removeItem(key);
  } catch {
    return;
  }
}

export function storedProjectId() {
  try {
    return window.localStorage.getItem(PROJECT_STORAGE_KEY);
  } catch {
    return null;
  }
}

export function storeProjectId(id) {
  try {
    if (id == null || id === '') {
      window.localStorage.removeItem(PROJECT_STORAGE_KEY);
    } else {
      window.localStorage.setItem(PROJECT_STORAGE_KEY, String(id));
    }
  } catch {
    return;
  }
}
