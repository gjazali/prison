#!/bin/bash
# Builds the aws-firecracker guest kernel on a Linux host.
set -euo pipefail

KERNEL_VERSION="6.18.54"
KERNEL_SHA256="9df30b02dd8102bbd0be52556288ef6889ddbe7f1ddb96fbf847d0becf3eacac"
FIRECRACKER_VERSION="v1.17.0"
declare -A CONFIG_SHA256=(
  [aarch64]="35cce8e8b754a84523ca20dc068fc3f8f03a15523be36a8623bad54e11972e2a"
  [x86_64]="ba22401a0c7292a4c024ebcd10a562d4a1f1bfd2faed671406d3b159c0cf5215"
)

output="$(realpath -m "${1:?usage: build.sh <output directory>}")"
source_directory="$(cd "$(dirname "$0")" && pwd)"
architecture="$(uname -m)"
# Firecracker boots `Image` on arm64 and `vmlinux` on x86_64.
case "${architecture}" in
  aarch64) go_architecture="arm64" target="Image" ;;
  x86_64) go_architecture="amd64" target="vmlinux" ;;
  *) echo "unsupported architecture: ${architecture}" >&2; exit 1 ;;
esac
image_path="arch/arm64/boot/Image"
if [[ "${target}" == "vmlinux" ]]; then
  image_path="vmlinux"
fi

work="${KERNEL_WORK_DIRECTORY:-${HOME}/.cache/prison-kernel}"
mkdir -p "${work}" "${output}"
cd "${work}"

archive="linux-${KERNEL_VERSION}.tar.xz"
if [[ ! -f "${archive}" ]]; then
  major="${KERNEL_VERSION%%.*}"
  curl -fsSL --retry 3 -o "${archive}.partial" \
    "https://cdn.kernel.org/pub/linux/kernel/v${major}.x/${archive}"
  mv "${archive}.partial" "${archive}"
fi
echo "${KERNEL_SHA256}  ${archive}" | sha256sum -c --quiet -

config_name="microvm-kernel-ci-${architecture}-${KERNEL_VERSION%.*}.config"
config_url="https://raw.githubusercontent.com/firecracker-microvm/firecracker"
config_url+="/${FIRECRACKER_VERSION}/resources/guest_configs/${config_name}"
curl -fsSL --retry 3 -o firecracker.config "${config_url}"
echo "${CONFIG_SHA256[${architecture}]}  firecracker.config" \
  | sha256sum -c --quiet -

tree="linux-${KERNEL_VERSION}"
rm -rf "${tree}"
tar -xJf "${archive}"
cd "${tree}"
KCONFIG_CONFIG=.config scripts/kconfig/merge_config.sh -m \
  ../firecracker.config "${source_directory}/prison.config" >/dev/null
make olddefconfig >/dev/null

missing=0
while read -r option; do
  if ! grep -qx "${option}" .config; then
    echo "the kernel configuration does not have ${option}" >&2
    missing=1
  fi
done < "${source_directory}/required.config"
if [[ "${missing}" -ne 0 ]]; then
  exit 1
fi

export KBUILD_BUILD_USER="prison" KBUILD_BUILD_HOST="prison"
export KBUILD_BUILD_TIMESTAMP="${SOURCE_DATE_EPOCH:+@${SOURCE_DATE_EPOCH}}"
export KBUILD_BUILD_TIMESTAMP="${KBUILD_BUILD_TIMESTAMP:-2026-01-01}"
make -j"$(nproc)" "${target}" >/dev/null
gzip -9 -n -c "${image_path}" > "${output}/linux-${go_architecture}.gz"
echo "${KERNEL_VERSION}" > "${output}/version"
echo "wrote ${output}/linux-${go_architecture}.gz"
