/**
 * Maps Techstack Stack Identity into the embed launcher contract.
 *
 * Open-core characters carry tone and animation metadata only; emoji is a
 * product-local launcher projection aligned with the Brand catalog.
 */
import {
  getIdentityCharacter,
  type StackIdentity,
} from "#lib/components/open-core/index.js";

export interface CompanionLauncherStackIdentity {
  emoji: string;
  glowColor?: string;
  animated?: boolean;
  name?: string;
}

const CHARACTER_EMOJI: Readonly<Record<string, string>> = {
  rocket: "🚀",
  robot: "🤖",
  astronaut: "🧑‍🚀",
  dragon: "🐉",
  unicorn: "🦄",
  phoenix: "🔥",
  alien: "👽",
  ghost: "👻",
  wizard: "🧙",
  ninja: "🥷",
  satellite: "🛰️",
  hologram: "📡",
  circuit: "⚡",
  nebula: "🌌",
  quantum: "⚛️",
  cybershield: "🛡️",
};

export function companionStackIdentityFromAccount(
  identity: StackIdentity | null | undefined,
): CompanionLauncherStackIdentity | undefined {
  if (!identity?.characterId) return undefined;
  const character = getIdentityCharacter(identity.characterId);
  return {
    emoji: CHARACTER_EMOJI[identity.characterId] ?? "🚀",
    glowColor: identity.glowColorOverride ?? character?.tone,
    animated: identity.animationEnabled,
    name: identity.name,
  };
}
