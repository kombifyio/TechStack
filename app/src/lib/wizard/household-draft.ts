export type HouseholdProfile = "solo" | "shared";

export interface HouseholdPersonDraft {
  id: string;
  name: string;
  email: string;
}

export interface HouseholdDraft {
  profile: HouseholdProfile;
  people: HouseholdPersonDraft[];
}

export function createHouseholdPersonDraft(): HouseholdPersonDraft {
  return {
    id: globalThis.crypto?.randomUUID?.() ?? `person-${Date.now()}`,
    name: "",
    email: "",
  };
}
