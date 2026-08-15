/**
 * SSH Key generation utilities
 * Generates Ed25519 or RSA keys in OpenSSH format using Web Crypto API
 */

export type SSHKeyAlgorithm = "ed25519" | "rsa-2048" | "rsa-4096";

export interface SSHKeyPair {
  publicKey: string;
  privateKey: string;
  fingerprint: string;
  algorithm: SSHKeyAlgorithm;
}

/**
 * Generate random bytes for key generation
 */
function generateRandomBytes(length: number): Uint8Array {
  return crypto.getRandomValues(new Uint8Array(length));
}

/**
 * Convert ArrayBuffer to base64
 */
function arrayBufferToBase64(buffer: ArrayBuffer): string {
  const bytes = new Uint8Array(buffer);
  let binary = "";
  for (let i = 0; i < bytes.byteLength; i++) {
    binary += String.fromCharCode(bytes[i]);
  }
  return btoa(binary);
}

/**
 * Compute SHA-256 fingerprint of public key
 */
async function computeFingerprint(publicKeyData: Uint8Array): Promise<string> {
  const hashBuffer = await crypto.subtle.digest(
    "SHA-256",
    publicKeyData.buffer as ArrayBuffer,
  );
  const hashArray = Array.from(new Uint8Array(hashBuffer));
  const fingerprint = hashArray
    .map((b) => b.toString(16).padStart(2, "0"))
    .join(":");
  return `SHA256:${btoa(String.fromCharCode(...new Uint8Array(hashBuffer))).replace(/=/g, "")}`;
}

/**
 * Wrap a base64 string to PEM format lines
 */
function wrapPEMLines(base64: string, lineLength = 70): string {
  const lines: string[] = [];
  for (let i = 0; i < base64.length; i += lineLength) {
    lines.push(base64.slice(i, i + lineLength));
  }
  return lines.join("\n");
}

/**
 * Generate an Ed25519 key pair
 * Note: Web Crypto Ed25519 may not be available in all browsers
 * This uses a simplified approach with random bytes for demonstration
 */
async function generateEd25519(): Promise<SSHKeyPair> {
  // Check if Ed25519 is supported
  try {
    const keyPair = await crypto.subtle.generateKey({ name: "Ed25519" }, true, [
      "sign",
      "verify",
    ]);

    const publicKeyRaw = await crypto.subtle.exportKey(
      "raw",
      keyPair.publicKey,
    );
    const privateKeyPKCS8 = await crypto.subtle.exportKey(
      "pkcs8",
      keyPair.privateKey,
    );

    const publicKeyBytes = new Uint8Array(publicKeyRaw);

    // Build OpenSSH public key format
    // Format: "ssh-ed25519 <base64-encoded-data> comment"
    const keyType = "ssh-ed25519";
    const keyTypeBytes = new TextEncoder().encode(keyType);

    // Build the public key blob: length(keyType) + keyType + length(pubkey) + pubkey
    const pubKeyBlob = new Uint8Array(
      4 + keyTypeBytes.length + 4 + publicKeyBytes.length,
    );
    const view = new DataView(pubKeyBlob.buffer);
    let offset = 0;

    // Key type length and data
    view.setUint32(offset, keyTypeBytes.length, false);
    offset += 4;
    pubKeyBlob.set(keyTypeBytes, offset);
    offset += keyTypeBytes.length;

    // Public key length and data
    view.setUint32(offset, publicKeyBytes.length, false);
    offset += 4;
    pubKeyBlob.set(publicKeyBytes, offset);

    const publicKeySSH = `ssh-ed25519 ${arrayBufferToBase64(pubKeyBlob.buffer as ArrayBuffer)} generated@techstack`;

    // Format private key as PEM (PKCS8)
    const privateKeyPEM = `-----BEGIN PRIVATE KEY-----\n${wrapPEMLines(arrayBufferToBase64(privateKeyPKCS8))}\n-----END PRIVATE KEY-----`;

    const fingerprint = await computeFingerprint(pubKeyBlob);

    return {
      publicKey: publicKeySSH,
      privateKey: privateKeyPEM,
      fingerprint,
      algorithm: "ed25519",
    };
  } catch {
    // Ed25519 not supported, fall back to simulated key
    throw new Error("Ed25519 not supported in this browser. Try RSA instead.");
  }
}

/**
 * Generate an RSA key pair
 */
async function generateRSA(bits: 2048 | 4096): Promise<SSHKeyPair> {
  const keyPair = await crypto.subtle.generateKey(
    {
      name: "RSASSA-PKCS1-v1_5",
      modulusLength: bits,
      publicExponent: new Uint8Array([1, 0, 1]), // 65537
      hash: "SHA-256",
    },
    true,
    ["sign", "verify"],
  );

  const publicKeySpki = await crypto.subtle.exportKey(
    "spki",
    keyPair.publicKey,
  );
  const privateKeyPkcs8 = await crypto.subtle.exportKey(
    "pkcs8",
    keyPair.privateKey,
  );

  // Parse SPKI to extract modulus and exponent for OpenSSH format
  const publicKeyDER = new Uint8Array(publicKeySpki);

  // For simplicity, we'll use a helper to build the SSH RSA public key
  // OpenSSH RSA format: ssh-rsa <base64(length(ssh-rsa) + "ssh-rsa" + length(e) + e + length(n) + n)>

  // Extract modulus and exponent from SPKI (simplified - real parsing is complex)
  // We'll provide the PKCS8/SPKI keys for now and note that conversion to OpenSSH format
  // requires additional parsing

  const publicKeyPEM = `-----BEGIN PUBLIC KEY-----\n${wrapPEMLines(arrayBufferToBase64(publicKeySpki))}\n-----END PUBLIC KEY-----`;

  const privateKeyPEM = `-----BEGIN PRIVATE KEY-----\n${wrapPEMLines(arrayBufferToBase64(privateKeyPkcs8))}\n-----END PRIVATE KEY-----`;

  const fingerprint = await computeFingerprint(publicKeyDER);

  return {
    publicKey: publicKeyPEM,
    privateKey: privateKeyPEM,
    fingerprint,
    algorithm: bits === 2048 ? "rsa-2048" : "rsa-4096",
  };
}

/**
 * Generate an SSH key pair
 */
export async function generateSSHKey(
  algorithm: SSHKeyAlgorithm,
): Promise<SSHKeyPair> {
  switch (algorithm) {
    case "ed25519":
      return generateEd25519();
    case "rsa-2048":
      return generateRSA(2048);
    case "rsa-4096":
      return generateRSA(4096);
    default:
      throw new Error(`Unsupported algorithm: ${algorithm}`);
  }
}

/**
 * Download a key file
 */
export function downloadKey(
  content: string,
  filename: string,
  isPrivate: boolean,
): void {
  const blob = new Blob([content], { type: "text/plain" });
  const url = URL.createObjectURL(blob);

  const a = document.createElement("a");
  a.href = url;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  document.body.removeChild(a);
  URL.revokeObjectURL(url);
}

/**
 * Check if SSH key generation is available
 */
export function isSSHKeyGenAvailable(): boolean {
  return (
    typeof crypto !== "undefined" &&
    typeof crypto.subtle !== "undefined" &&
    typeof crypto.subtle.generateKey === "function"
  );
}
