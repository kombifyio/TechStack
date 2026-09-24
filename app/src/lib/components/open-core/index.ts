/**
 * Open-core surface: identity vocabulary plus the shared UI components.
 *
 * The components come from Brand's published packages — the ecosystem
 * authority for notifications and stack identity presentation
 * (the public tree installs their unmodified release tarballs from
 * app/third_party/npm, so open-core closure never justifies a local fork). Only the identity
 * normalization vocabulary stays product-local: Techstack adapts runtime
 * data into the shared components, it renders nothing of its own.
 */
export {
  NotificationBell,
  NotificationPreferences,
} from "@kombiverselabs/ui/notifications";
export {
  StackIdentityBadge,
  StackIdentityDisplay,
  StackIdentityEditor,
} from "@kombiverselabs/ui/identity";
export * from "./identity";
