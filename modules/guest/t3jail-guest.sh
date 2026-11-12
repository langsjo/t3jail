# Guest-side helper for the t3jail CLI. META_DIR, KEYS_SRC and MOUNTS_MANIFEST
# are prepended by module.nix.

usage() {
  echo "usage: t3jail-guest host-setup | mount <tag> <path> <ro|rw> | umount <path> [--lazy]" >&2
  exit 2
}

mount_share() {
  local tag=$1 path=$2 mode=$3
  case $mode in ro | rw) ;; *) usage ;; esac
  if mountpoint -q "$path"; then
    echo "t3jail-guest: $path is already a mount point" >&2
    return 1
  fi
  mkdir -p "$path"
  # The kernel announces a hotplugged share before it can be mounted. The
  # tag's sysfs entry appears just before mounts are accepted, so wait for
  # it, then retry briefly.
  local tries=0
  until grep -qsxF "$tag" /sys/fs/virtiofs/*/tag; do
    if ((++tries > 100)); then
      echo "t3jail-guest: virtiofs tag $tag did not appear" >&2
      return 1
    fi
    sleep 0.1
  done
  for tries in 1 2 3 4 5; do
    mount -t virtiofs -o "$mode" "$tag" "$path" 2>/dev/null && return 0
    sleep 0.2
  done
  mount -t virtiofs -o "$mode" "$tag" "$path"
}

umount_share() {
  local path=$1 lazy=${2:-}
  if [[ $lazy == --lazy ]]; then
    umount --lazy "$path"
    return
  fi
  if ! umount "$path" 2>/dev/null; then
    echo "t3jail-guest: $path is busy; processes using it:" >&2
    fuser -vm "$path" >&2 || true
    return 1
  fi
}

host_setup() {
  install -d -m 0755 /run/t3jail/ssh
  if [[ -f $KEYS_SRC ]]; then
    install -m 0644 "$KEYS_SRC" /run/t3jail/ssh/authorized_keys
  else
    echo "t3jail-guest: no SSH key found in $META_DIR" >&2
  fi

  [[ -f $MOUNTS_MANIFEST ]] || return 0
  local failed=0 tag path ro mode
  while IFS=$'\t' read -r tag path ro; do
    if [[ $ro == true ]]; then mode=ro; else mode=rw; fi
    mount_share "$tag" "$path" "$mode" || failed=1
  done < <(jq -r '.[] | [.tag, .guestPath, (.readOnly | tostring)] | @tsv' "$MOUNTS_MANIFEST")
  return "$failed"
}

case ${1:-} in
host-setup) host_setup ;;
mount)
  [[ $# -eq 4 ]] || usage
  mount_share "$2" "$3" "$4"
  ;;
umount)
  [[ $# -ge 2 ]] || usage
  umount_share "$2" "${3:-}"
  ;;
*) usage ;;
esac
