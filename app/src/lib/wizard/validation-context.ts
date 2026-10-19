/**
 * Step validation shared between the wizard shell and its fields.
 *
 * The wizard owns what is missing; a field only reports that it is the one
 * that is wrong (`aria-invalid`) and points at its message
 * (`aria-describedby`). Outside a wizard no context exists and every field
 * reads as valid, so shared field components stay usable elsewhere.
 */
import { getContext, setContext } from "svelte";

export type WizardFieldId =
  | "catalog"
  | "server-selection"
  | "server-host"
  | "server-wallet"
  | "hypervisor-guest"
  | "access-mode"
  | "audience"
  | "public-access"
  | "owner-email"
  | "owner-recovery"
  | "step";

export interface WizardIssue {
  field: WizardFieldId;
  message: string;
}

export interface WizardValidation {
  /** Inline errors appear only after the person tried to continue. */
  readonly attempted: boolean;
  readonly issues: readonly WizardIssue[];
}

const CONTEXT_KEY = Symbol("wizard-validation");

export function provideWizardValidation(validation: WizardValidation): void {
  setContext(CONTEXT_KEY, validation);
}

/** The id of the visible message that belongs to one field. */
export function wizardFieldErrorId(field: WizardFieldId): string {
  return `wizard-error-${field}`;
}

export interface WizardFieldState {
  readonly invalid: boolean;
  readonly message: string;
  readonly errorId: string;
  /** Joins the field's own hint ids with its error id while it is invalid. */
  describedBy(...hintIds: (string | undefined)[]): string | undefined;
}

/**
 * Read one field's validation state. Call during component initialisation;
 * the returned getters stay reactive.
 */
export function useWizardField(field: WizardFieldId): WizardFieldState {
  const validation = getContext<WizardValidation | undefined>(CONTEXT_KEY);
  const issue = () =>
    validation?.attempted
      ? validation.issues.find((entry) => entry.field === field)
      : undefined;
  const errorId = wizardFieldErrorId(field);
  return {
    get invalid() {
      return Boolean(issue());
    },
    get message() {
      return issue()?.message ?? "";
    },
    errorId,
    describedBy(...hintIds) {
      const ids = hintIds.filter((id): id is string => Boolean(id));
      if (issue()) ids.push(errorId);
      return ids.length > 0 ? ids.join(" ") : undefined;
    },
  };
}
