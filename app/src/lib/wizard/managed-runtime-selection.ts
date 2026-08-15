export interface ManagedRuntimeSelectionState {
  authenticated: boolean;
  featuresReady: boolean;
  featuresLoading: boolean;
  verificationFailed: boolean;
  selectable: boolean;
}

/**
 * A temporary embedded-auth or entitlement-loading state must never rewrite a
 * managed VPS choice. Downgrading is safe only after an authenticated,
 * conclusive negative entitlement result.
 */
export function shouldDowngradeManagedRuntimeSelection(
  state: ManagedRuntimeSelectionState,
): boolean {
  return (
    state.authenticated &&
    state.featuresReady &&
    !state.featuresLoading &&
    !state.verificationFailed &&
    !state.selectable
  );
}
