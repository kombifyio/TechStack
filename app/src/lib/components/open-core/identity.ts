export type IconStyle = "filled" | "glass" | "gradient" | "outlined";

export interface StackIdentity {
  name: string;
  characterId: string;
  animationStyle: string;
  savedAt: string | null;
  animationEnabled: boolean;
  iconStyle: IconStyle;
  glowColorOverride: string | null;
}

export type IdentityCharacter = {
  id: string;
  label: string;
  tone: string;
  animationStyle: string;
};

// This is deliberately product-local rather than an import from the private
// Brand package. Identity values remain portable in a self-hosted export.
export const identityCharacters: readonly IdentityCharacter[] = [
  {
    id: "rocket",
    label: "Rocket",
    tone: "oklch(0.75 0.18 55)",
    animationStyle: "identity-float",
  },
  {
    id: "robot",
    label: "Robot",
    tone: "oklch(0.75 0.2 145)",
    animationStyle: "identity-glitch",
  },
  {
    id: "astronaut",
    label: "Astronaut",
    tone: "oklch(0.65 0.2 250)",
    animationStyle: "identity-drift",
  },
  {
    id: "dragon",
    label: "Dragon",
    tone: "oklch(0.7 0.25 30)",
    animationStyle: "identity-flame",
  },
  {
    id: "unicorn",
    label: "Unicorn",
    tone: "oklch(0.75 0.2 320)",
    animationStyle: "identity-rainbow",
  },
  {
    id: "phoenix",
    label: "Phoenix",
    tone: "oklch(0.75 0.22 60)",
    animationStyle: "identity-rise",
  },
  {
    id: "alien",
    label: "Alien",
    tone: "oklch(0.7 0.25 145)",
    animationStyle: "identity-pulse",
  },
  {
    id: "ghost",
    label: "Ghost",
    tone: "oklch(0.8 0.05 260)",
    animationStyle: "identity-fade",
  },
  {
    id: "wizard",
    label: "Wizard",
    tone: "oklch(0.65 0.25 280)",
    animationStyle: "identity-sparkle",
  },
  {
    id: "ninja",
    label: "Ninja",
    tone: "oklch(0.5 0.05 0)",
    animationStyle: "identity-stealth",
  },
  {
    id: "satellite",
    label: "Satellite",
    tone: "oklch(0.7 0.18 200)",
    animationStyle: "identity-orbit",
  },
  {
    id: "hologram",
    label: "Hologram",
    tone: "oklch(0.7 0.15 180)",
    animationStyle: "identity-scanline",
  },
  {
    id: "circuit",
    label: "Circuit",
    tone: "oklch(0.65 0.22 230)",
    animationStyle: "identity-trace",
  },
  {
    id: "nebula",
    label: "Nebula",
    tone: "oklch(0.6 0.25 290)",
    animationStyle: "identity-cosmic",
  },
  {
    id: "quantum",
    label: "Quantum",
    tone: "oklch(0.7 0.2 170)",
    animationStyle: "identity-quantum",
  },
  {
    id: "cybershield",
    label: "Cybershield",
    tone: "oklch(0.6 0.18 200)",
    animationStyle: "identity-shield",
  },
];

export const defaultIdentity: StackIdentity = {
  name: "My Stack",
  characterId: "rocket",
  animationStyle: "identity-float",
  savedAt: null,
  animationEnabled: true,
  iconStyle: "filled",
  glowColorOverride: null,
};

export function getIdentityCharacter(
  characterId: string | null | undefined,
): IdentityCharacter | undefined {
  return identityCharacters.find((character) => character.id === characterId);
}

export function normalizeStackIdentity(
  identity: Partial<StackIdentity> | null | undefined,
): StackIdentity {
  const characterId =
    getIdentityCharacter(identity?.characterId)?.id ??
    defaultIdentity.characterId;
  const character = getIdentityCharacter(characterId) ?? identityCharacters[0];
  return {
    name: identity?.name?.trim() || defaultIdentity.name,
    characterId,
    animationStyle: character.animationStyle,
    savedAt: identity?.savedAt ?? null,
    animationEnabled:
      identity?.animationEnabled ?? defaultIdentity.animationEnabled,
    iconStyle: identity?.iconStyle ?? defaultIdentity.iconStyle,
    glowColorOverride: identity?.glowColorOverride ?? null,
  };
}
