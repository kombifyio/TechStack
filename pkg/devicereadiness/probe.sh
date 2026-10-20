#!/bin/sh
# Kombify Techstack device readiness probe.
#
# Runs on a device that is not enrolled yet and may have no network at all, so
# it depends on nothing but a POSIX shell and the coreutils every Debian-family
# install ships. It reads; it never changes anything.
#
# It prints one JSON object on stdout. Anything it could not determine is
# omitted rather than guessed, because the evaluation treats an absent fact as
# unknown and an invented one would be indistinguishable from a measurement.
#
# The executor passes:
#   KOMBIFY_PROBE_CONTROL_PLANE  host[:port] of the Techstack origin
#   KOMBIFY_PROBE_PACKAGE_MIRROR host of the package mirror
#   KOMBIFY_PROBE_IMAGE_REGISTRY host of the image registry
# Endpoints are reported back by role, never by hostname, so the evaluation
# stays independent of the deployment's origins.

set -u

# Each probe runs in a command substitution, that is in its own subshell, so a
# shell variable cannot collect what they could not measure. The errors go to a
# file instead: a probe that loses its own error reporting produces a report
# that looks clean, which is the one outcome this design exists to prevent.
PROBE_ERROR_FILE=$(mktemp 2>/dev/null || printf '/tmp/kombify-probe-errors.%s' "$$")
: > "$PROBE_ERROR_FILE"
trap 'rm -f "$PROBE_ERROR_FILE"' EXIT INT TERM

note_error() { printf '%s\n' "$1" >> "$PROBE_ERROR_FILE"; }

# json_escape emits a JSON string body for arbitrary input. Control characters
# are dropped rather than encoded: they only ever arrive here from a mangled
# file, and a probe must not be able to produce output the parser rejects.
json_escape() {
    printf '%s' "$1" | tr -d '\000-\010\013\014\016-\037' | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g' | awk 'BEGIN{ORS=""} {print sep $0; sep="\\n"}'
}

emit_string() { printf '"%s"' "$(json_escape "$1")"; }

# emit_string_array turns a newline-separated list into a JSON array.
emit_string_array() {
    printf '['
    sep=""
    while IFS= read -r line; do
        [ -z "$line" ] && continue
        printf '%s' "$sep"
        emit_string "$line"
        sep=","
    done
    printf ']'
}

has() { command -v "$1" >/dev/null 2>&1; }

# ---------------------------------------------------------------- os + kernel

probe_os() {
    if [ -r /etc/os-release ]; then
        # shellcheck disable=SC1091
        . /etc/os-release 2>/dev/null || true
        printf '"os":{"observed":true,"id":%s,"versionId":%s,"codename":%s,"pretty":%s}' \
            "$(emit_string "${ID:-}")" "$(emit_string "${VERSION_ID:-}")" \
            "$(emit_string "${VERSION_CODENAME:-}")" "$(emit_string "${PRETTY_NAME:-}")"
    else
        printf '"os":{"observed":false}'
    fi
}

probe_kernel() {
    release=$(uname -r 2>/dev/null || printf '')
    arch=$(uname -m 2>/dev/null || printf '')
    if [ -z "$release" ]; then
        printf '"kernel":{"observed":false}'
        return
    fi
    extra=""
    if has dpkg-query; then
        if dpkg-query -W -f='${Status}' "linux-modules-extra-$release" 2>/dev/null | grep -q 'install ok installed'; then
            extra=',"extraModulesInstalled":true'
        else
            extra=',"extraModulesInstalled":false'
        fi
    fi
    printf '"kernel":{"observed":true,"release":%s,"arch":%s%s}' \
        "$(emit_string "$release")" "$(emit_string "$arch")" "$extra"
}

# ------------------------------------------------------------------ interfaces

# An interface is virtual when it has no device link in sysfs (loopback,
# bridges, tunnels, container veths). Only a physical interface can be the
# device's uplink, and calling a bridge an uplink would turn a blocked device
# into a passing one.
probe_interfaces() {
    printf '"interfaces":['
    sep=""
    if [ ! -d /sys/class/net ]; then
        printf ']'
        note_error "network interfaces could not be enumerated: /sys/class/net is absent"
        return
    fi
    for path in /sys/class/net/*; do
        [ -e "$path" ] || continue
        name=$(basename "$path")
        operstate=$(cat "$path/operstate" 2>/dev/null || printf '')
        mac=$(cat "$path/address" 2>/dev/null || printf '')
        driver=""
        if [ -L "$path/device/driver" ]; then
            driver=$(basename "$(readlink -f "$path/device/driver" 2>/dev/null)" 2>/dev/null || printf '')
        fi
        virtual="true"
        [ -e "$path/device" ] && virtual="false"
        addresses=""
        if has ip; then
            addresses=$(ip -o addr show dev "$name" 2>/dev/null | awk '{for(i=1;i<=NF;i++) if($i=="inet"||$i=="inet6") print $(i+1)}')
        fi
        printf '%s{"name":%s,"operState":%s,"driver":%s,"macAddress":%s,"virtual":%s,"addresses":%s}' \
            "$sep" "$(emit_string "$name")" "$(emit_string "$operstate")" \
            "$(emit_string "$driver")" "$(emit_string "$mac")" "$virtual" \
            "$(printf '%s\n' "$addresses" | emit_string_array)"
        sep=","
    done
    printf ']'
}

# A network controller with no driver bound has no interface, so it can only be
# found on the bus. This is what separates "the cable is out" from "the module
# for this card was never installed".
probe_unclaimed() {
    printf '"unclaimedNics":['
    sep=""
    if [ ! -d /sys/bus/pci/devices ]; then
        printf ']'
        return
    fi
    for path in /sys/bus/pci/devices/*; do
        [ -e "$path" ] || continue
        class=$(cat "$path/class" 2>/dev/null || printf '')
        # PCI class 0x02 is a network controller.
        case "$class" in
            0x02*) ;;
            *) continue ;;
        esac
        [ -e "$path/driver" ] && continue
        slot=$(basename "$path")
        vendor=$(cat "$path/vendor" 2>/dev/null | sed 's/^0x//' || printf '')
        device=$(cat "$path/device" 2>/dev/null | sed 's/^0x//' || printf '')
        modalias=$(cat "$path/modalias" 2>/dev/null || printf '')
        description=""
        if has lspci; then
            description=$(lspci -s "${slot#0000:}" 2>/dev/null | cut -d' ' -f2- | head -n 1)
        fi
        printf '%s{"slot":%s,"vendorId":%s,"deviceId":%s,"modalias":%s,"description":%s}' \
            "$sep" "$(emit_string "$slot")" "$(emit_string "$vendor")" \
            "$(emit_string "$device")" "$(emit_string "$modalias")" "$(emit_string "$description")"
        sep=","
    done
    printf ']'
}

probe_routes() {
    if ! has ip; then
        printf '"routes":{"observed":false}'
        return
    fi
    line=$(ip -4 route show default 2>/dev/null | head -n 1)
    gateway=$(printf '%s' "$line" | awk '{for(i=1;i<=NF;i++) if($i=="via") print $(i+1)}')
    device=$(printf '%s' "$line" | awk '{for(i=1;i<=NF;i++) if($i=="dev") print $(i+1)}')
    printf '"routes":{"observed":true,"defaultGateway":%s,"defaultDevice":%s}' \
        "$(emit_string "$gateway")" "$(emit_string "$device")"
}

# ---------------------------------------------------------------- declared net

probe_netplan() {
    if [ ! -d /etc/netplan ]; then
        printf '"netplan":{"observed":true,"present":false}'
        return
    fi
    files=$(find /etc/netplan -maxdepth 1 -type f \( -name '*.yaml' -o -name '*.yml' \) 2>/dev/null | sort)
    if [ -z "$files" ]; then
        printf '"netplan":{"observed":true,"present":false}'
        return
    fi

    # The interface names and renderers are read with the shell rather than a
    # YAML parser, which the device may not have. Both are simple, indented
    # scalars in every generated and hand-written netplan file we care about,
    # and a name this reader misses only ever costs a check its certainty.
    renderers=$(grep -hE '^[[:space:]]*renderer:' $files 2>/dev/null | sed -e 's/.*renderer:[[:space:]]*//' -e 's/[[:space:]]*$//' | sort -u)
    interfaces=$(awk '
        /^[[:space:]]*(ethernets|bonds|bridges|vlans|wifis):[[:space:]]*$/ { in_block=1; block_indent=match($0,/[^ ]/); next }
        in_block && /^[[:space:]]*[a-zA-Z0-9_.*?-]+:[[:space:]]*$/ {
            indent=match($0,/[^ ]/)
            if (indent <= block_indent) { in_block=0; next }
            if (seen_indent == 0) seen_indent=indent
            if (indent == seen_indent) { name=$0; sub(/:[[:space:]]*$/,"",name); sub(/^[[:space:]]*/,"",name); print name }
        }
    ' $files 2>/dev/null | sort -u)

    # Asking the device to validate its own configuration needs root, and a
    # permission failure is not a configuration fault. Running it as anyone
    # else would report every machine's network as rejected and hide the real
    # finding, so it is simply not attempted.
    parse_error=""
    if has netplan && [ "$(id -u 2>/dev/null || echo 1)" = "0" ]; then
        parse_error=$(netplan generate 2>&1 >/dev/null | grep -viE 'permission|denied|too open' | head -n 3)
    fi

    renderer_available=""
    case "$renderers" in
        *NetworkManager*)
            if systemctl is-active NetworkManager >/dev/null 2>&1; then
                renderer_available=',"rendererAvailable":true'
            else
                renderer_available=',"rendererAvailable":false'
            fi
            ;;
        *networkd*|"")
            if systemctl is-active systemd-networkd >/dev/null 2>&1 || has networkctl; then
                renderer_available=',"rendererAvailable":true'
            else
                renderer_available=',"rendererAvailable":false'
            fi
            ;;
    esac

    printf '"netplan":{"observed":true,"present":true,"files":%s,"renderers":%s,"configuredInterfaces":%s,"parseError":%s%s}' \
        "$(printf '%s\n' "$files" | emit_string_array)" \
        "$(printf '%s\n' "$renderers" | emit_string_array)" \
        "$(printf '%s\n' "$interfaces" | emit_string_array)" \
        "$(emit_string "$parse_error")" "$renderer_available"
}

probe_resolver() {
    servers=""
    stub=""
    target=""
    if has resolvectl; then
        servers=$(resolvectl status 2>/dev/null | awk '/DNS Servers:/{$1="";$2="";print}' | tr ' ' '\n' | grep -v '^$')
        if resolvectl status 2>/dev/null | grep -qi 'stub'; then stub=',"stubListener":true'; fi
    fi
    if [ -z "$servers" ] && [ -r /etc/resolv.conf ]; then
        servers=$(awk '/^nameserver/{print $2}' /etc/resolv.conf 2>/dev/null)
    fi
    if [ -L /etc/resolv.conf ]; then
        target=$(readlink -f /etc/resolv.conf 2>/dev/null || printf '')
    fi
    printf '"resolver":{"observed":true,"servers":%s,"resolvConfTarget":%s%s}' \
        "$(printf '%s\n' "$servers" | emit_string_array)" "$(emit_string "$target")" "$stub"
}

# -------------------------------------------------------------- reachability

# reach_one classifies a single outbound attempt. The distinction between "the
# name did not resolve", "nothing answered" and "the handshake failed" is the
# whole diagnostic value, so each is reported separately rather than collapsed
# into a failure.
reach_one() {
    role="$1"
    host="$2"
    [ -z "$host" ] && return

    hostname_only=${host%%:*}
    result="unknown"
    detail=""

    resolved=1
    if has getent; then
        getent hosts "$hostname_only" >/dev/null 2>&1 && resolved=0
    elif has host; then
        host "$hostname_only" >/dev/null 2>&1 && resolved=0
    else
        resolved=2
    fi

    if [ "$resolved" = "1" ]; then
        result="dns_failed"
    elif has curl; then
        curl_out=$(curl -sS --max-time 8 -o /dev/null -w '%{http_code}' "https://$host/" 2>&1)
        curl_rc=$?
        case "$curl_rc" in
            0) result="ok" ;;
            6) result="dns_failed" ;;
            35|60|58|77|91) result="tls_failed"; detail="$curl_out" ;;
            7|28) result="unreachable"; detail="$curl_out" ;;
            *) result="unreachable"; detail="$curl_out" ;;
        esac
    elif has wget; then
        if wget -q -T 8 -t 1 -O /dev/null "https://$host/" 2>/dev/null; then result="ok"; else result="unreachable"; fi
    fi

    printf '{"endpoint":%s,"result":%s,"detail":%s}' \
        "$(emit_string "$role")" "$(emit_string "$result")" "$(emit_string "$detail")"
}

probe_reachability() {
    printf '"reachability":['
    sep=""
    for pair in "control-plane:${KOMBIFY_PROBE_CONTROL_PLANE:-}" \
                "package-mirror:${KOMBIFY_PROBE_PACKAGE_MIRROR:-archive.ubuntu.com}" \
                "image-registry:${KOMBIFY_PROBE_IMAGE_REGISTRY:-ghcr.io}"; do
        role=${pair%%:*}
        host=${pair#*:}
        [ -z "$host" ] && continue
        entry=$(reach_one "$role" "$host")
        [ -z "$entry" ] && continue
        printf '%s%s' "$sep" "$entry"
        sep=","
    done
    printf ']'
}

probe_proxy() {
    env_proxy="false"
    if [ -n "${https_proxy:-}${HTTPS_PROXY:-}${http_proxy:-}${HTTP_PROXY:-}" ]; then env_proxy="true"; fi
    apt_proxy="false"
    if [ -d /etc/apt/apt.conf.d ] && grep -rqiE '^[^/]*Acquire::(http|https)::Proxy' /etc/apt/apt.conf.d /etc/apt/apt.conf 2>/dev/null; then
        apt_proxy="true"
    fi
    printf '"proxy":{"observed":true,"environment":%s,"apt":%s}' "$env_proxy" "$apt_proxy"
}

probe_clock() {
    epoch=$(date -u +%s 2>/dev/null || printf '')
    if [ -z "$epoch" ]; then
        printf '"clock":{"observed":false}'
        return
    fi
    sync=""
    if has timedatectl; then
        if timedatectl show -p NTPSynchronized --value 2>/dev/null | grep -qi 'yes'; then
            sync=',"synchronized":true'
        else
            sync=',"synchronized":false'
        fi
    fi
    # The executor computes the skew from this epoch against its own clock; a
    # device with no time source cannot measure its own error.
    printf '"clock":{"observed":true,"deviceEpoch":%s%s}' "$epoch" "$sync"
}

probe_packages() {
    if ! has apt-get; then
        printf '"packages":{"observed":false}'
        return
    fi
    locked="false"
    for lock in /var/lib/dpkg/lock-frontend /var/lib/apt/lists/lock; do
        if [ -e "$lock" ] && has fuser && fuser "$lock" >/dev/null 2>&1; then locked="true"; fi
    done
    sources="false"
    if grep -rqE '^[[:space:]]*(deb|Types:)' /etc/apt/sources.list /etc/apt/sources.list.d 2>/dev/null; then sources="true"; fi
    printf '"packages":{"observed":true,"locked":%s,"sourcesConfigured":%s}' "$locked" "$sources"
}

probe_cloudinit() {
    if ! has cloud-init; then
        printf '"cloudInit":{"observed":true,"present":false}'
        return
    fi
    status=$(cloud-init status 2>/dev/null | awk -F': ' '/status:/{print $2}' | head -n 1)
    printf '"cloudInit":{"observed":true,"present":true,"status":%s}' "$(emit_string "$status")"
}

# A hypervisor node is not repaired into a host; it is where hosts come from.
# The marker directory is the reliable signal: such a node identifies itself as
# plain Debian in every standard place.
probe_hypervisor() {
    if [ ! -d /etc/pve ]; then
        printf '"hypervisor":{"observed":true}'
        return
    fi
    version=""
    if has pveversion; then
        version=$(pveversion 2>/dev/null | head -n 1 | sed -e 's/^pve-manager\///' -e 's/[[:space:]].*$//')
    fi
    bridges=""
    if [ -d /sys/class/net ]; then
        for path in /sys/class/net/*; do
            [ -d "$path/bridge" ] || continue
            bridges="$bridges$(basename "$path")
"
        done
    fi
    printf '"hypervisor":{"observed":true,"kind":"proxmox","version":%s,"bridges":%s}' \
        "$(emit_string "$version")" "$(printf '%s' "$bridges" | emit_string_array)"
}

# ---------------------------------------------------------------- accelerators

# pci_function prints one PCI function as JSON. The driver is read from the
# bus, so a function with nothing bound reports an empty driver.
pci_function() {
    dev="/sys/bus/pci/devices/$1"
    driver=""
    [ -L "$dev/driver" ] && driver=$(basename "$(readlink "$dev/driver")")
    printf '{"slot":%s,"class":%s,"vendorId":%s,"deviceId":%s,"driver":%s}' \
        "$(emit_string "$1")" "$(emit_string "$(cat "$dev/class" 2>/dev/null)")" \
        "$(emit_string "$(sed 's/^0x//' "$dev/vendor" 2>/dev/null)")" \
        "$(emit_string "$(sed 's/^0x//' "$dev/device" 2>/dev/null)")" "$(emit_string "$driver")"
}

# A GPU is found by its PCI class (0x03), never by its driver: the device that
# matters most here is the one with no driver at all. Thunderbolt devices under
# user security are reported unauthorized, because a GPU behind one is not on
# the bus until someone approves it.
probe_accelerators() {
    if [ ! -d /sys/bus/pci/devices ]; then
        printf '"accelerators":{"observed":false}'
        return
    fi
    iommu=""
    groups=0
    if [ -d /sys/kernel/iommu_groups ]; then
        groups=$(find /sys/kernel/iommu_groups -mindepth 1 -maxdepth 1 -type d 2>/dev/null | wc -l | tr -d ' ')
    fi
    if [ "$groups" -gt 0 ] 2>/dev/null; then
        iommu=',"iommuEnabled":true'
    elif [ -d /sys/class/iommu ] && [ -z "$(ls -A /sys/class/iommu 2>/dev/null)" ]; then
        iommu=',"iommuEnabled":false'
    fi

    security=""
    for domain in /sys/bus/thunderbolt/devices/domain*; do
        [ -r "$domain/security" ] || continue
        security=$(cat "$domain/security" 2>/dev/null)
        break
    done
    tb=""
    tsep=""
    for path in /sys/bus/thunderbolt/devices/*; do
        [ -r "$path/unique_id" ] && [ -r "$path/authorized" ] || continue
        # Host routers are named N-0 and are always authorized; only attached
        # devices can wait for approval.
        case "$(basename "$path")" in *-0) continue ;; esac
        authorized=false
        [ "$(cat "$path/authorized" 2>/dev/null)" = 0 ] || authorized=true
        tb="$tb$tsep$(printf '{"id":%s,"uniqueId":%s,"name":%s,"vendor":%s,"authorized":%s}' \
            "$(emit_string "$(basename "$path")")" "$(emit_string "$(cat "$path/unique_id" 2>/dev/null)")" \
            "$(emit_string "$(cat "$path/device_name" 2>/dev/null)")" "$(emit_string "$(cat "$path/vendor_name" 2>/dev/null)")" "$authorized")"
        tsep=","
    done

    gpus=""
    gsep=""
    for path in /sys/bus/pci/devices/*; do
        [ -e "$path" ] || continue
        case "$(cat "$path/class" 2>/dev/null)" in 0x03*) ;; *) continue ;; esac
        slot=$(basename "$path")
        boot=false
        [ "$(cat "$path/boot_vga" 2>/dev/null)" = 1 ] && boot=true
        removable=false
        [ "$(cat "$path/removable" 2>/dev/null)" = removable ] && removable=true
        group=""
        members=""
        if [ -L "$path/iommu_group" ]; then
            group=$(basename "$(readlink "$path/iommu_group")")
            msep=""
            for member in "/sys/kernel/iommu_groups/$group/devices/"*; do
                [ -e "$member" ] || continue
                members="$members$msep$(pci_function "$(basename "$member")")"
                msep=","
            done
        fi
        # The resolved sysfs path lists every bridge from the root port down,
        # which is what a late enclosure is rescanned from.
        upstream=$(readlink -f "$path" 2>/dev/null | tr '/' '\n' | grep -E '^[0-9a-f]{4}:[0-9a-f]{2}:[0-9a-f]{2}\.[0-7]$' | grep -v "^$slot\$")
        description=""
        has lspci && description=$(lspci -s "${slot#0000:}" 2>/dev/null | cut -d' ' -f2- | head -n 1)
        fn=$(pci_function "$slot")
        gpus="$gpus$gsep${fn%?},\"description\":$(emit_string "$description"),\"bootVga\":$boot,\"removable\":$removable,\"iommuGroup\":$(emit_string "$group"),\"groupMembers\":[$members],\"upstream\":$(printf '%s\n' "$upstream" | emit_string_array)}"
        gsep=","
    done
    printf '"accelerators":{"observed":true%s,"iommuGroups":%s,"thunderboltSecurity":%s,"thunderbolt":[%s],"gpus":[%s]}' \
        "$iommu" "$groups" "$(emit_string "$security")" "$tb" "$gpus"
}

# -------------------------------------------------------------------- hardware

# whole_disk prints the sysfs path of the disk behind a major:minor block device:
# it follows device-mapper and RAID members down to a real disk and steps from a
# partition to the disk that holds it. It prints nothing when the device is not
# a disk the kernel describes (a network or overlay root, a btrfs subvolume).
whole_disk() {
    real=$(readlink -f "/sys/dev/block/$1" 2>/dev/null) || return 1
    depth=0
    while [ "$depth" -lt 4 ]; do
        [ -d "$real" ] || return 1
        member=""
        [ -d "$real/slaves" ] && member=$(ls "$real/slaves" 2>/dev/null | head -n 1)
        if [ -n "$member" ]; then
            real=$(readlink -f "$real/slaves/$member" 2>/dev/null) || return 1
            depth=$((depth + 1))
            continue
        fi
        [ -e "$real/partition" ] && real=$(dirname "$real")
        [ -r "$real/queue/rotational" ] || return 1
        printf '%s' "$real"
        return 0
    done
    return 1
}

# probe_hardware reads what the machine is, from procfs and sysfs only: the CPU
# flags behind the x86-64 microarchitecture level, the GPUs on the bus and the
# drivers bound to them, render nodes, the disk under the root filesystem and
# the DMI chassis type. It reports raw observations and classifies nothing: the
# rules live in Go, where they are tested. A fact the machine does not expose is
# omitted, never reported as zero, so an absent member means "not observed".
probe_hardware() {
    if [ ! -r /proc/cpuinfo ] && [ ! -d /sys/bus/pci/devices ]; then
        printf '"hardware":{"observed":false}'
        return
    fi
    arch=$(uname -m 2>/dev/null || printf '')
    cores=$(grep -c '^processor' /proc/cpuinfo 2>/dev/null)
    case "$cores" in '' | *[!0-9]*) cores=0 ;; esac
    cpu="\"arch\":$(emit_string "$arch"),\"cores\":$cores"
    if [ "$arch" = x86_64 ] && [ -r /proc/cpuinfo ]; then
        flags=$(awk -F: '/^flags/ {print $2; exit}' /proc/cpuinfo 2>/dev/null | tr ' ' '\n')
        cpu="$cpu,\"flags\":$(printf '%s\n' "$flags" | emit_string_array)"
    fi

    gpus=""
    gsep=""
    seen=0
    if [ -d /sys/bus/pci/devices ]; then
        for path in /sys/bus/pci/devices/*; do
            [ -e "$path" ] || continue
            case "$(cat "$path/class" 2>/dev/null)" in 0x03*) ;; *) continue ;; esac
            seen=$((seen + 1))
            [ "$seen" -le 8 ] || break
            driver=""
            [ -L "$path/driver" ] && driver=$(basename "$(readlink "$path/driver")")
            gpus="$gpus$gsep$(printf '{"slot":%s,"vendorId":%s,"driver":%s}' \
                "$(emit_string "$(basename "$path")")" \
                "$(emit_string "$(sed 's/^0x//' "$path/vendor" 2>/dev/null)")" "$(emit_string "$driver")")"
            gsep=","
        done
    fi

    nvidia=""
    [ -r /sys/module/nvidia/version ] && nvidia=$(cat /sys/module/nvidia/version 2>/dev/null)

    nodes=""
    nsep=""
    seen=0
    for path in /sys/class/drm/renderD*; do
        [ -e "$path" ] || continue
        seen=$((seen + 1))
        [ "$seen" -le 8 ] || break
        driver=""
        [ -L "$path/device/driver" ] && driver=$(basename "$(readlink -f "$path/device/driver")")
        nodes="$nodes$nsep$(printf '{"name":%s,"vendorId":%s,"driver":%s}' \
            "$(emit_string "$(basename "$path")")" \
            "$(emit_string "$(sed 's/^0x//' "$path/device/vendor" 2>/dev/null)")" "$(emit_string "$driver")")"
        nsep=","
    done

    disk=""
    mm=$(awk '$5 == "/" {m = $3} END {print m}' /proc/self/mountinfo 2>/dev/null)
    if [ -n "$mm" ] && real=$(whole_disk "$mm"); then
        case "$(cat "$real/queue/rotational" 2>/dev/null)" in
            0) disk=",\"systemDisk\":{\"device\":$(emit_string "$(basename "$real")"),\"rotational\":false}" ;;
            1) disk=",\"systemDisk\":{\"device\":$(emit_string "$(basename "$real")"),\"rotational\":true}" ;;
        esac
    fi

    chassis=""
    chassis_type=$(cat /sys/class/dmi/id/chassis_type 2>/dev/null)
    case "$chassis_type" in '' | *[!0-9]*) ;; *) chassis=",\"chassisType\":$chassis_type" ;; esac

    printf '"hardware":{"observed":true,"cpu":{%s},"gpus":[%s],"nvidiaDriverVersion":%s,"renderNodes":[%s]%s%s}' \
        "$cpu" "$gpus" "$(emit_string "$nvidia")" "$nodes" "$disk" "$chassis"
}

# ------------------------------------------------------------------------ main

# KOMBIFY_PROBE_SCOPE=accelerators is the node-local Guard asking only which
# GPU a guest could receive. It reads sysfs and nothing else, so it is cheap
# enough to repeat with the inventory.
if [ "${KOMBIFY_PROBE_SCOPE:-}" = accelerators ]; then
    printf '{"schemaVersion":"techstack.device-readiness/v1"'
    printf ',%s' "$(probe_hypervisor)"
    printf ',%s' "$(probe_accelerators)"
    printf ',"probeErrors":%s' "$(emit_string_array < "$PROBE_ERROR_FILE")"
    printf '}\n'
    exit 0
fi

# KOMBIFY_PROBE_SCOPE=hardware is the node-local Guard asking only what the
# machine is. It reads procfs and sysfs and nothing else, so it runs without
# privileges and is cheap enough to repeat every few minutes.
if [ "${KOMBIFY_PROBE_SCOPE:-}" = hardware ]; then
    printf '{"schemaVersion":"techstack.device-readiness/v1"'
    printf ',%s' "$(probe_hardware)"
    printf ',"probeErrors":%s' "$(emit_string_array < "$PROBE_ERROR_FILE")"
    printf '}\n'
    exit 0
fi

printf '{"schemaVersion":"techstack.device-readiness/v1"'
printf ',%s' "$(probe_os)"
printf ',%s' "$(probe_kernel)"
printf ',%s' "$(probe_interfaces)"
printf ',%s' "$(probe_unclaimed)"
printf ',%s' "$(probe_routes)"
printf ',%s' "$(probe_netplan)"
printf ',%s' "$(probe_resolver)"
printf ',%s' "$(probe_reachability)"
printf ',%s' "$(probe_proxy)"
printf ',%s' "$(probe_clock)"
printf ',%s' "$(probe_packages)"
printf ',%s' "$(probe_cloudinit)"
printf ',%s' "$(probe_hypervisor)"
printf ',%s' "$(probe_accelerators)"
printf ',%s' "$(probe_hardware)"
printf ',"probeErrors":%s' "$(emit_string_array < "$PROBE_ERROR_FILE")"
printf '}\n'
