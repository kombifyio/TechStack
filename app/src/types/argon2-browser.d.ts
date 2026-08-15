declare module "argon2-browser/dist/argon2-bundled.min.js" {
  export const ArgonType: {
    readonly Argon2d: number;
    readonly Argon2i: number;
    readonly Argon2id: number;
  };

  export interface HashParams {
    pass: string | Uint8Array;
    salt: string | Uint8Array;
    time?: number;
    mem?: number;
    hashLen?: number;
    parallelism?: number;
    secret?: Uint8Array;
    ad?: Uint8Array;
    type?: number;
  }

  export interface HashResult {
    hash: Uint8Array;
    hashHex: string;
    encoded: string;
  }

  export interface VerifyParams {
    pass: string | Uint8Array;
    encoded: string;
    secret?: Uint8Array;
    ad?: Uint8Array;
    type?: number;
  }

  export type VerifyResult = HashResult;

  const argon2: {
    ArgonType: typeof ArgonType;
    hash(params: HashParams): Promise<HashResult>;
    verify(params: VerifyParams): Promise<VerifyResult>;
  };

  export default argon2;
}
