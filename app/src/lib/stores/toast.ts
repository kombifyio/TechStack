// Toast notification store for displaying errors and success messages
import { writable } from "svelte/store";

export interface Toast {
  id: string;
  type: "success" | "error" | "warning" | "info";
  message: string;
  details?: string;
  timeout?: number;
}

function createToastStore() {
  const { subscribe, update } = writable<Toast[]>([]);

  function add(toast: Omit<Toast, "id">) {
    const id = crypto.randomUUID();
    const newToast = { ...toast, id };

    update((toasts) => [...toasts, newToast]);

    // Auto-remove after timeout (default 5s for errors, 3s for others)
    const timeout = toast.timeout ?? (toast.type === "error" ? 5000 : 3000);
    if (timeout > 0) {
      setTimeout(() => remove(id), timeout);
    }

    return id;
  }

  function remove(id: string) {
    update((toasts) => toasts.filter((t) => t.id !== id));
  }

  function clear() {
    update(() => []);
  }

  // Convenience methods
  function success(message: string, details?: string) {
    return add({ type: "success", message, details });
  }

  function error(message: string, details?: string) {
    return add({ type: "error", message, details });
  }

  function warning(message: string, details?: string) {
    return add({ type: "warning", message, details });
  }

  function info(message: string, details?: string) {
    return add({ type: "info", message, details });
  }

  return {
    subscribe,
    add,
    remove,
    clear,
    success,
    error,
    warning,
    info,
  };
}

export const toasts = createToastStore();

// Backwards-compatible alias for components importing `toast`.
export const toast = toasts;
