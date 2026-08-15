import argon2 from "argon2-browser/dist/argon2-bundled.min.js";

export const MIN_RECOVERY_PASSPHRASE_LENGTH = 12;

export const ARGON2_PARAMS = {
  memory: 64 * 1024,
  iterations: 3,
  parallelism: 4,
  hashLength: 32,
  saltLength: 16,
} as const;

export interface PassphraseStrength {
  score: 0 | 1 | 2 | 3 | 4;
  feedback: string[];
}

export async function hashPassphrase(passphrase: string): Promise<string> {
  if (passphrase.length < MIN_RECOVERY_PASSPHRASE_LENGTH) {
    throw new Error(
      `Passphrase too short (minimum ${MIN_RECOVERY_PASSPHRASE_LENGTH} characters)`,
    );
  }

  const salt = crypto.getRandomValues(new Uint8Array(ARGON2_PARAMS.saltLength));
  const result = await argon2.hash({
    pass: passphrase,
    salt,
    time: ARGON2_PARAMS.iterations,
    mem: ARGON2_PARAMS.memory,
    parallelism: ARGON2_PARAMS.parallelism,
    hashLen: ARGON2_PARAMS.hashLength,
    type: argon2.ArgonType.Argon2id,
  });

  return result.encoded;
}

export function scoreStrength(passphrase: string): PassphraseStrength {
  const feedback: string[] = [];
  let score = 0;

  if (passphrase.length >= MIN_RECOVERY_PASSPHRASE_LENGTH) {
    score += 1;
  } else {
    feedback.push(`Use at least ${MIN_RECOVERY_PASSPHRASE_LENGTH} characters.`);
  }

  if (passphrase.length >= 20) {
    score += 1;
  } else {
    feedback.push("Longer passphrases are easier to remember and stronger.");
  }

  if (/\d/.test(passphrase)) {
    score += 1;
  } else {
    feedback.push("Add at least one number.");
  }

  if (/[^\p{L}\d]/u.test(passphrase) || /\s/.test(passphrase)) {
    score += 1;
  } else {
    feedback.push(
      "Add spaces or symbols to make the passphrase harder to guess.",
    );
  }

  return {
    score: Math.max(0, Math.min(4, score)) as 0 | 1 | 2 | 3 | 4,
    feedback,
  };
}
