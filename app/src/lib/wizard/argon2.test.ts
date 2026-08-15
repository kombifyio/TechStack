import { beforeEach, describe, expect, it, vi } from "vitest";

const { hashMock } = vi.hoisted(() => ({
  hashMock: vi.fn(
    async (params: {
      mem: number;
      time: number;
      parallelism: number;
      salt: Uint8Array;
    }) => ({
      hash: new Uint8Array([1, 2, 3]),
      hashHex: "010203",
      encoded: `$argon2id$v=19$m=${params.mem},t=${params.time},p=${params.parallelism}$mockSalt$mockHash`,
    }),
  ),
}));

vi.mock("argon2-browser/dist/argon2-bundled.min.js", () => ({
  default: {
    ArgonType: {
      Argon2id: 2,
    },
    hash: hashMock,
  },
}));

import { ARGON2_PARAMS, hashPassphrase, scoreStrength } from "./argon2";

describe("wizard recovery argon2", () => {
  beforeEach(() => {
    hashMock.mockClear();
  });

  it("creates a PHC argon2id string with the StackKits parameters", async () => {
    const encoded = await hashPassphrase("correct horse battery staple 12!");

    expect(hashMock).toHaveBeenCalledTimes(1);
    expect(hashMock).toHaveBeenCalledWith(
      expect.objectContaining({
        pass: "correct horse battery staple 12!",
        mem: ARGON2_PARAMS.memory,
        time: ARGON2_PARAMS.iterations,
        parallelism: ARGON2_PARAMS.parallelism,
        hashLen: ARGON2_PARAMS.hashLength,
        type: 2,
      }),
    );
    expect(hashMock.mock.calls[0]?.[0].salt).toBeInstanceOf(Uint8Array);
    expect(hashMock.mock.calls[0]?.[0].salt).toHaveLength(
      ARGON2_PARAMS.saltLength,
    );
    expect(encoded).toContain("$argon2id$");
    expect(
      encoded.startsWith(
        `$argon2id$v=19$m=${ARGON2_PARAMS.memory},t=${ARGON2_PARAMS.iterations},p=${ARGON2_PARAMS.parallelism}$`,
      ),
    ).toBe(true);
  });

  it("scores very weak passphrases as zero", () => {
    const strength = scoreStrength("a");

    expect(strength.score).toBe(0);
    expect(strength.feedback.length).toBeGreaterThan(0);
  });

  it("scores long recovery passphrases with numbers and symbols as strong", () => {
    const strength = scoreStrength("correct horse battery staple 12!");

    expect(strength.score).toBe(4);
  });
});
