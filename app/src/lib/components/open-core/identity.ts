import { tr } from "#lib/i18n.svelte.js";
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
    get label() {
      return tr("ui.identity.rocket");
    },
    tone: "oklch(0.75 0.18 55)",
    animationStyle: "identity-float",
  },
  {
    id: "robot",
    get label() {
      return tr("ui.identity.robot");
    },
    tone: "oklch(0.75 0.2 145)",
    animationStyle: "identity-glitch",
  },
  {
    id: "astronaut",
    get label() {
      return tr("ui.identity.astronaut");
    },
    tone: "oklch(0.65 0.2 250)",
    animationStyle: "identity-drift",
  },
  {
    id: "dragon",
    get label() {
      return tr("ui.identity.dragon");
    },
    tone: "oklch(0.7 0.25 30)",
    animationStyle: "identity-flame",
  },
  {
    id: "unicorn",
    get label() {
      return tr("ui.identity.unicorn");
    },
    tone: "oklch(0.75 0.2 320)",
    animationStyle: "identity-rainbow",
  },
  {
    id: "phoenix",
    get label() {
      return tr("ui.identity.phoenix");
    },
    tone: "oklch(0.75 0.22 60)",
    animationStyle: "identity-rise",
  },
  {
    id: "alien",
    get label() {
      return tr("ui.identity.alien");
    },
    tone: "oklch(0.7 0.25 145)",
    animationStyle: "identity-pulse",
  },
  {
    id: "ghost",
    get label() {
      return tr("ui.identity.ghost");
    },
    tone: "oklch(0.8 0.05 260)",
    animationStyle: "identity-fade",
  },
  {
    id: "wizard",
    get label() {
      return tr("ui.identity.wizard");
    },
    tone: "oklch(0.65 0.25 280)",
    animationStyle: "identity-sparkle",
  },
  {
    id: "ninja",
    get label() {
      return tr("ui.identity.ninja");
    },
    tone: "oklch(0.5 0.05 0)",
    animationStyle: "identity-stealth",
  },
  {
    id: "satellite",
    get label() {
      return tr("ui.identity.satellite");
    },
    tone: "oklch(0.7 0.18 200)",
    animationStyle: "identity-orbit",
  },
  {
    id: "hologram",
    get label() {
      return tr("ui.identity.hologram");
    },
    tone: "oklch(0.7 0.15 180)",
    animationStyle: "identity-scanline",
  },
  {
    id: "circuit",
    get label() {
      return tr("ui.identity.circuit");
    },
    tone: "oklch(0.65 0.22 230)",
    animationStyle: "identity-trace",
  },
  {
    id: "nebula",
    get label() {
      return tr("ui.identity.nebula");
    },
    tone: "oklch(0.6 0.25 290)",
    animationStyle: "identity-cosmic",
  },
  {
    id: "quantum",
    get label() {
      return tr("ui.identity.quantum");
    },
    tone: "oklch(0.7 0.2 170)",
    animationStyle: "identity-quantum",
  },
  {
    id: "cybershield",
    get label() {
      return tr("ui.identity.cybershield");
    },
    tone: "oklch(0.6 0.18 200)",
    animationStyle: "identity-shield",
  },
];

export const defaultIdentity: StackIdentity = {
  get name() {
    return tr("ui.identity.myHomelab");
  },
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
