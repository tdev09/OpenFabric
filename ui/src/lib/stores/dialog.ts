import { writable } from 'svelte/store';

export interface DialogOptions {
  title?: string;
  message: string;
  type: 'alert' | 'confirm';
  icon?: string;
  confirmText?: string;
  cancelText?: string;
  danger?: boolean;
  onConfirm?: () => void;
  onCancel?: () => void;
}

export const dialogStore = writable<DialogOptions | null>(null);

export const dialog = {
  alert(message: string, title = 'Notification', icon = 'ℹ️') {
    return new Promise<void>((resolve) => {
      dialogStore.set({
        title,
        message,
        type: 'alert',
        icon,
        confirmText: 'OK',
        onConfirm: () => {
          dialogStore.set(null);
          resolve();
        }
      });
    });
  },
  confirm(
    message: string,
    title = 'Are you sure?',
    icon = '❓',
    confirmText = 'Confirm',
    cancelText = 'Cancel',
    danger = false
  ) {
    return new Promise<boolean>((resolve) => {
      dialogStore.set({
        title,
        message,
        type: 'confirm',
        icon,
        confirmText,
        cancelText,
        danger,
        onConfirm: () => {
          dialogStore.set(null);
          resolve(true);
        },
        onCancel: () => {
          dialogStore.set(null);
          resolve(false);
        }
      });
    });
  },
  close() {
    dialogStore.set(null);
  }
};
