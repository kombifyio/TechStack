/**
 * How hands-on the signed-in operator wants to be. The person answers it once
 * in the kombify Cloud account setup; Techstack only reads it and shows more
 * or fewer guided setup steps by it (`experienceLevel` in `@kombiverselabs/ui/usecase`). A Techstack
 * without Cloud falls back to the experience of its own self-description
 * (KOMBIFY-DESIGN-SYSTEM-STANDARD.md §9.4).
 */
import { readCloudExperienceLevel } from "#lib/api/cloudProfile.js";
import {
  getOperatorSelfDisclosure,
  type SelfDisclosureExperience,
} from "#lib/api/operatorProfile.js";

class OperatorExperienceStore {
  experience = $state<SelfDisclosureExperience | null>(null);
  loaded = $state(false);

  private loading: Promise<void> | null = null;

  /** Reads the answer once; nothing reachable reads as unanswered. */
  load(): Promise<void> {
    this.loading ??= readCloudExperienceLevel()
      .then(
        async (level) =>
          level ??
          (await getOperatorSelfDisclosure()
            .then((record) => record.disclosure?.experience ?? null)
            .catch(() => null)),
      )
      .then((level) => {
        this.experience = level;
      })
      .finally(() => {
        this.loaded = true;
      });
    return this.loading;
  }
}

export const operatorExperience = new OperatorExperienceStore();
