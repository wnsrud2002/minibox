// Package web은 패킷 여행기 정적 파일을 minibox 바이너리에 넣는다.
// 같은 파일을 그대로 GitHub Pages에도 올릴 수 있게 빌드 단계 없이 둔다.
package web

import "embed"

//go:embed travel.html pcap.js samples
var FS embed.FS
