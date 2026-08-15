#!/usr/bin/env node

import { execFileSync } from "node:child_process";
import { copyFileSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { basename, join, resolve } from "node:path";

const UBUNTU_ISO_NAME = "ubuntu-24.04.4-live-server-amd64.iso";

function parseArgs(argv) {
  const options = {};
  for (let index = 0; index < argv.length; index += 1) {
    const argument = argv[index];
    if (!argument.startsWith("--"))
      throw new Error(`unknown argument: ${argument}`);
    options[argument.slice(2)] = argv[++index] ?? "";
  }
  for (const required of [
    "netboot-dir",
    "run-dir",
    "run-id",
    "controller-url",
    "ssh-public-key",
    "out",
  ]) {
    if (!options[required]) throw new Error(`--${required} is required`);
  }
  if (!/^[a-z0-9][a-z0-9-]{7,63}$/.test(options["run-id"])) {
    throw new Error("--run-id must be a lowercase, URL-safe identifier");
  }
  const controllerUrl = new URL(options["controller-url"]);
  if (!["http:", "https:"].includes(controllerUrl.protocol)) {
    throw new Error("--controller-url must use HTTP or HTTPS");
  }
  return {
    netbootDir: resolve(options["netboot-dir"]),
    runDir: resolve(options["run-dir"]),
    runId: options["run-id"],
    controllerUrl: controllerUrl.toString().replace(/\/$/, ""),
    sshPublicKeyPath: resolve(options["ssh-public-key"]),
    outPath: resolve(options.out),
  };
}

function yamlBlock(value, indentation) {
  const prefix = " ".repeat(indentation);
  return value
    .trimEnd()
    .split("\n")
    .map((line) => `${prefix}${line}`)
    .join("\n");
}

export function buildUserData({ runId, controllerUrl, sshPublicKey }) {
  const callbackScript = `#!/usr/bin/env python3
import json
import pathlib
import socket
import subprocess
import time
import urllib.request

run_id = ${JSON.stringify(runId)}
callback_url = ${JSON.stringify(`${controllerUrl}/runs/${runId}/callback`)}

def command(*args):
    return subprocess.run(args, check=False, capture_output=True, text=True).stdout.strip()

payload = {
    "runId": run_id,
    "hostname": socket.gethostname(),
    "addresses": command("hostname", "-I").split(),
    "interfaces": [],
}
for address_file in pathlib.Path("/sys/class/net").glob("*/address"):
    if address_file.parent.name == "lo":
        continue
    payload["interfaces"].append({
        "name": address_file.parent.name,
        "mac": address_file.read_text().strip(),
    })

body = json.dumps(payload).encode()
request = urllib.request.Request(
    callback_url,
    data=body,
    method="POST",
    headers={"Content-Type": "application/json", "X-Kombify-Run-Token": run_id},
)
for attempt in range(120):
    try:
        with urllib.request.urlopen(request, timeout=10) as response:
            if 200 <= response.status < 300:
                break
    except Exception:
        time.sleep(5)
`;

  return `#cloud-config
ssh_authorized_keys:
  - ${JSON.stringify(sshPublicKey.trim())}
autoinstall:
  version: 1
  reporting:
    kombify:
      type: webhook
      endpoint: ${controllerUrl}/runs/${runId}/events
  refresh-installer:
    update: false
  early-commands:
    - [sh, -c, "swapoff -a || true"]
    - [sh, -c, "mdadm --stop --scan || true"]
    - |
      target="$(lsblk -bndo PATH,SIZE,TYPE | awk '$3 == "disk" { print $1, $2 }' | sort -k2,2nr | head -n1 | awk '{ print $1 }')"
      pvs --noheadings --separator : -o pv_name,vg_name 2>/dev/null | while IFS=: read -r pv vg; do
        pv="$(echo "$pv" | xargs)"
        vg="$(echo "$vg" | xargs)"
        case "$pv" in
          "$target"*)
            lvremove --force --yes "$vg" || true
            vgremove --force --yes "$vg" || true
            pvremove --force --force --yes "$pv" || true
            ;;
        esac
      done
      udevadm settle
  error-commands:
    - [sh, -c, "tail -n 500 /var/log/installer/curtin-install.log | curl --max-time 20 --data-binary @- ${controllerUrl}/runs/${runId}/diagnostics || true"]
  locale: en_US.UTF-8
  keyboard:
    layout: us
  identity:
    hostname: kombify-${runId.slice(0, 24)}
    username: kombify
    password: "!"
  ssh:
    install-server: true
    allow-pw: false
    authorized-keys:
      - ${JSON.stringify(sshPublicKey.trim())}
  storage:
    swap:
      size: 0
    config:
      - type: disk
        id: system-disk
        match:
          size: largest
        ptable: gpt
        wipe: superblock-recursive
        preserve: false
        grub_device: true
      - type: partition
        id: efi-partition
        device: system-disk
        number: 1
        size: 1G
        flag: boot
        grub_device: true
      - type: format
        id: efi-format
        volume: efi-partition
        fstype: fat32
      - type: mount
        id: efi-mount
        device: efi-format
        path: /boot/efi
      - type: partition
        id: root-partition
        device: system-disk
        number: 2
        size: -1
      - type: format
        id: root-format
        volume: root-partition
        fstype: ext4
      - type: mount
        id: root-mount
        device: root-format
        path: /
  user-data:
    write_files:
      - path: /usr/local/sbin/kombify-provision-callback.py
        owner: root:root
        permissions: "0755"
        content: |
${yamlBlock(callbackScript, 10)}
    runcmd:
      - [python3, /usr/local/sbin/kombify-provision-callback.py]
`;
}

export function buildGrubConfig({ runId, controllerUrl }) {
  const seedUrl = `${controllerUrl}/seed/${runId}/`;
  const isoUrl = `${controllerUrl}/images/${UBUNTU_ISO_NAME}`;
  return `set default=0
set timeout=0

menuentry "Kombify unattended Ubuntu Server 24.04" {
    linux /linux autoinstall ip=dhcp url=${isoUrl} cloud-config-url=/dev/null 'ds=nocloud-net;s=${seedUrl}' ---
    initrd /initrd
}
`;
}

export function prepareUbuntuNetboot(options) {
  const sshPublicKey = readFileSync(options.sshPublicKeyPath, "utf8").trim();
  if (!/^ssh-(ed25519|rsa)\s+\S+/.test(sshPublicKey)) {
    throw new Error("SSH public key is not an OpenSSH public key");
  }

  const seedDir = join(options.runDir, "seed", options.runId);
  const artifactDir = join(options.runDir, "artifact");
  const bootDir = join(artifactDir, "boot");
  mkdirSync(seedDir, { recursive: true });
  mkdirSync(bootDir, { recursive: true });

  writeFileSync(
    join(seedDir, "meta-data"),
    `instance-id: ${options.runId}\n`,
    "utf8",
  );
  writeFileSync(
    join(seedDir, "user-data"),
    buildUserData({
      runId: options.runId,
      controllerUrl: options.controllerUrl,
      sshPublicKey,
    }),
    "utf8",
  );
  writeFileSync(join(bootDir, "grub.cfg"), buildGrubConfig(options), "utf8");

  const amd64Dir = join(options.netbootDir, "amd64");
  for (const [source, destination] of [
    ["bootx64.efi", "BOOTX64.EFI"],
    ["grubx64.efi", "grubx64.efi"],
    ["linux", "linux"],
    ["initrd", "initrd"],
  ]) {
    copyFileSync(join(amd64Dir, source), join(bootDir, destination));
  }

  const imageName = basename(options.outPath);
  const containerScript = [
    "set -eu",
    "apk add --no-cache mtools >/dev/null",
    `dd if=/dev/zero of=/work/${imageName} bs=1M count=192 status=none`,
    `mformat -i /work/${imageName} -F ::`,
    `mmd -i /work/${imageName} ::/EFI ::/EFI/BOOT ::/grub`,
    `mcopy -i /work/${imageName} /work/boot/BOOTX64.EFI ::/EFI/BOOT/BOOTX64.EFI`,
    `mcopy -i /work/${imageName} /work/boot/grubx64.efi ::/EFI/BOOT/grubx64.efi`,
    `mcopy -i /work/${imageName} /work/boot/grub.cfg ::/grub/grub.cfg`,
    `mcopy -i /work/${imageName} /work/boot/linux ::/linux`,
    `mcopy -i /work/${imageName} /work/boot/initrd ::/initrd`,
  ].join("; ");
  execFileSync(
    "docker",
    [
      "run",
      "--rm",
      "-v",
      `${artifactDir}:/work`,
      "alpine:3.22",
      "sh",
      "-lc",
      containerScript,
    ],
    { stdio: "inherit" },
  );

  return {
    runId: options.runId,
    imagePath: options.outPath,
    seedUrl: `${options.controllerUrl}/seed/${options.runId}/`,
    callbackUrl: `${options.controllerUrl}/runs/${options.runId}/callback`,
  };
}

function main() {
  const result = prepareUbuntuNetboot(parseArgs(process.argv.slice(2)));
  process.stdout.write(`${JSON.stringify(result)}\n`);
}

if (
  process.argv[1] &&
  import.meta.url.endsWith(process.argv[1].replaceAll("\\", "/"))
) {
  main();
}
