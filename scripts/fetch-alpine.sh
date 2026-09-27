#!/bin/sh
# alpine minirootfs를 images/alpine/에 푼다. 컨테이너의 루트 파일시스템으로 쓴다.
set -eu
cd "$(dirname "$0")/.."

DEST=images/alpine
if [ -e "$DEST/bin/busybox" ]; then
  echo "이미 있음: $DEST"
  exit 0
fi

ARCH=$(uname -m)
BASE=https://dl-cdn.alpinelinux.org/alpine/latest-stable/releases/$ARCH
META=$(curl -fsSL "$BASE/latest-releases.yaml")
FILE=$(echo "$META" | awk '/flavor: alpine-minirootfs/{f=1} f&&/file:/{print $2; exit}')
SHA=$(echo "$META" | awk '/flavor: alpine-minirootfs/{f=1} f&&/sha256:/{print $2; exit}')

mkdir -p images
curl -fL -o "images/$FILE" "$BASE/$FILE"
# 받은 파일이 공식 체크섬과 다르면 루트 파일시스템으로 쓰면 안 되므로 여기서 멈춘다
echo "$SHA  images/$FILE" | sha256sum -c -

mkdir -p "$DEST"
tar -xzf "images/$FILE" -C "$DEST"
rm "images/$FILE"
echo "완료: $DEST ($FILE)"
