/**
 * Quick-Sync functionality for triggering browser password manager save dialogs
 * Works with 1Password, Bitwarden, LastPass, and other browser extensions
 */

import type { PBWalletItem } from "$lib/stores/wallet";

/**
 * Triggers browser password manager "Save Password" dialog using an invisible form
 * This works without API keys by leveraging standard HTML autocomplete attributes
 */
export function triggerPasswordManagerSave(
  item: PBWalletItem,
): Promise<boolean> {
  return new Promise((resolve) => {
    let resolved = false;

    // Create invisible form
    const form = document.createElement("form");
    form.style.position = "absolute";
    form.style.left = "-9999px";
    form.style.width = "1px";
    form.style.height = "1px";
    form.style.opacity = "0";
    form.style.pointerEvents = "none";

    // Never submit credentials to an external origin.
    // The submit event is only used to trigger password manager extensions.
    form.action = "#";
    form.method = "post";
    form.noValidate = true;

    const finish = (ok: boolean) => {
      if (resolved) return;
      resolved = true;
      if (document.body.contains(form)) {
        document.body.removeChild(form);
      }
      resolve(ok);
    };

    // Create username field
    const usernameInput = document.createElement("input");
    usernameInput.type = "text";
    usernameInput.name = "username";
    usernameInput.autocomplete = "username";
    usernameInput.value = item.username || item.name;
    form.appendChild(usernameInput);

    // Create password field
    const passwordInput = document.createElement("input");
    passwordInput.type = "password";
    passwordInput.name = "password";
    passwordInput.autocomplete = "new-password";
    passwordInput.value = item.secret || "";
    form.appendChild(passwordInput);

    // Create email field (some password managers prefer email)
    if (item.username && item.username.includes("@")) {
      const emailInput = document.createElement("input");
      emailInput.type = "email";
      emailInput.name = "email";
      emailInput.autocomplete = "email";
      emailInput.value = item.username;
      form.appendChild(emailInput);
    }

    // Create submit button
    const submitButton = document.createElement("button");
    submitButton.type = "submit";
    submitButton.textContent = "Submit";
    form.appendChild(submitButton);

    // Add form to DOM
    document.body.appendChild(form);

    // Prevent any real navigation/submission.
    form.addEventListener("submit", (e) => {
      e.preventDefault();
      finish(true);
    });

    // Focus the password field to ensure password managers detect it
    passwordInput.focus();

    // Trigger immediately while we're still in the user-gesture call stack.
    submitButton.click();

    // Fallback cleanup in case password managers don't react
    setTimeout(() => {
      if (document.body.contains(form)) {
        finish(false);
      }
    }, 2000);
  });
}

/**
 * Alternative approach: Copy credentials to clipboard in password manager format
 * Useful as a fallback or for password managers that support clipboard import
 */
export function copyForPasswordManager(item: PBWalletItem): string {
  const lines: string[] = [];

  lines.push(`Name: ${item.name}`);
  if (item.username) lines.push(`Username: ${item.username}`);
  if (item.secret) lines.push(`Password: ${item.secret}`);
  if (item.url) lines.push(`URL: ${item.url}`);
  if (item.notes) lines.push(`Notes: ${item.notes}`);

  return lines.join("\n");
}
