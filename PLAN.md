# minibox — Go로 만든 미니 컨테이너 + 패킷 여행기

> 컨테이너를 직접 만들고, 그 위를 오가는 패킷을 애니메이션으로 재생한다.

- 트랙: 인프라·클라우드 (아이디어 8 + 4)
- 기간: 8주 (주 15~20시간 기준)
- 핵심 차별점: "도커 흉내"에서 끝나지 않는다. **내가 만든 컨테이너 네트워크(bridge, veth)에 장애를 주입**하고, TCP가 어떻게 버티는지를 패킷 단위로 보여 준다.
  컨테이너(OS 격리)와 네트워크(TCP)라는 면접 단골 두 주제를 프로젝트 하나로 증명한다.

---

## 1. 최종 모습 (데모 시나리오)

1. `minibox run --mem 64m --cpu 0.5 alpine /bin/sh` → 격리된 쉘이 뜬다. 호스트 프로세스가 보이지 않는다.
2. 대시보드에서 네임스페이스 격리 상태(호스트 대비 PID, 네트워크, 마운트)와 cgroup 사용량이 실시간 그래프로 보인다.
3. 컨테이너 안에서 fork bomb을 실행해도 `pids.max`에 막혀 호스트가 멀쩡하다. 메모리를 초과하면 OOM kill이 일어나고 대시보드에 찍힌다.
4. 컨테이너 A에서 B로 파일을 전송하는 동안 `minibox net chaos --loss 10%`를 실행한다.
5. 패킷 여행기에서 재전송, 중복 ACK, 혼잡 윈도우가 줄었다가 회복하는 과정이 애니메이션으로 재생된다.

## 2. 구조

```
┌──────────── Linux 호스트: Jetson Orin Nano (Ubuntu 24.04, arm64) ───────────┐
│                                                                            │
│  minibox (Go CLI)                                                          │
│   ├─ run     : clone(NEWPID|NEWUTS|NEWNS|NEWNET|NEWIPC) → re-exec → pivot_root │
│   ├─ cgroup  : /sys/fs/cgroup/minibox/<id>/{memory.max,cpu.max,pids.max}   │
│   ├─ image   : alpine rootfs + overlayfs (lower=이미지, upper=컨테이너별)  │
│   ├─ net     : mb0 bridge ─ veth pair ─ 컨테이너 eth0, iptables NAT        │
│   ├─ chaos   : iptables statistic(손실) + tc tbf(대역폭 제한)               │
│   └─ serve   : HTTP + SSE → 대시보드에 상태 스트리밍                       │
│                                                                            │
│  tcpdump -i mb0 -w cap.pcap                                                │
└───────────────────────────────────────────┬────────────────────────────────┘
                                            ▼
                         브라우저 (TypeScript + Vite)
                          ├─ 대시보드: 격리 상태, cgroup 그래프
                          └─ 패킷 여행기: pcap 직접 파싱 → 시퀀스 애니메이션
```

- SSE(Server-Sent Events)는 서버가 브라우저로 한 방향 스트림을 계속 흘려보내는 방식이다. WebSocket보다 단순해서 실시간 그래프에 충분하다.

## 3. 기술 스택

| 영역 | 선택 | 이유 |
|---|---|---|
| 런타임 | Go (표준 라이브러리 `syscall`, `os/exec`, `net/http`) | Docker, containerd, Kubernetes가 모두 Go로 되어 있어 직무 연관성이 가장 높다. 외부 의존성은 필요할 때만 `golang.org/x/sys/unix` 하나를 쓴다. |
| 네트워크 설정 | `ip`, `iptables`, `tc` 명령 호출 | netlink를 직접 짜는 건 8주 범위를 넘는다. 우선 명령어로 만들고, 여유가 있으면 교체한다. |
| 대시보드와 여행기 | TypeScript + Vite + SVG/Canvas | pcap 파서도 직접 만든다. 포맷이 단순하고, 파서 자체가 면접 소재가 된다. |
| 실행 환경 | Jetson Orin Nano (Ubuntu 24.04, 커널 6.8 tegra, arm64). 맥에서 `ssh orinnano`로 접속 | 진짜 리눅스라서 컨테이너 중첩 문제가 없다. 맥에서는 VS Code Remote-SSH로 편집한다. |

### Orin Nano 커널 점검 결과 (2026-09-27)

| 항목 | 상태 | 영향 |
|---|---|---|
| cgroup v2, 컨트롤러 `cpu memory pids` | ✅ | 3주차 그대로 진행 |
| PID/NET/USER 네임스페이스, overlayfs | ✅ | 1·2주차 그대로 진행 |
| veth, bridge | ✅ 모듈(`=m`) | 4주차에 `modprobe`가 필요할 수 있음 |
| `xt_statistic` (iptables 확률 드롭) | ✅ 모듈 | **손실 주입은 이것으로 한다** |
| tbf, htb (대역폭 제한) | ✅ | 대역폭 제한 주입 가능 |
| **sch_netem (지연·손실 주입)** | ❌ 커널에 없음 | 7주차 계획을 변경함(아래 참고) |
| NFQUEUE (패킷을 사용자 공간으로 넘기기) | ❌ | 사용자 공간 지연 주입 불가 |

> 준비물: Go와 tcpdump를 Orin Nano에 설치해야 한다(`sudo apt install golang-go tcpdump` 또는 go.dev의 arm64 바이너리). iperf3와 docker는 이미 설치되어 있다.

> ⚠️ **원격 장비라서 주의:** SSH로 접속한 장비의 네트워크를 직접 건드리는 프로젝트다.
> - minibox 전용 서브넷(예: `10.88.0.0/24`)과 `mb0` 브리지만 만지고, `eth0`, `wlan0`, 기본 라우트, Tailscale 인터페이스는 절대 건드리지 않는다.
> - iptables 규칙은 전용 체인(`MINIBOX`)에만 추가한다. 그래야 `minibox gc`로 한 번에 지울 수 있다.
> - 실험 전에 `sudo iptables-save > ~/iptables.bak`으로 백업한다. 접속이 끊겨도 모니터와 키보드로 복구할 수 있게 한다.

## 4. 주차별 계획

각 주의 **완료 기준**을 만족해야 다음 주로 넘어간다.

### 0주차: 환경 세팅 (2~3일)
- Orin Nano에 Go와 tcpdump를 설치한다. 맥 VS Code에서 Remote-SSH로 `~/workspace/minibox`를 연다.
- alpine minirootfs(**aarch64** 버전)를 `images/alpine/`에 내려받는 스크립트를 만든다.
- 폴더 구조: `cmd/minibox/`, `internal/{container,cgroup,image,net}/`, `web/`
- ✅ `go run ./cmd/minibox` 성공, `cat /sys/fs/cgroup/cgroup.subtree_control`로 cpu·memory·pids 확인

### 1주차: 프로세스 격리
- `minibox run <cmd>`: 자기 자신을 `/proc/self/exe`로 다시 실행하면서 새 네임스페이스에 들어간다(`Cloneflags`).
- UTS(호스트네임)와 PID 네임스페이스를 적용한다.
- ✅ 컨테이너 안에서 `echo $$`가 `1`이고, 호스트네임이 바뀐다.

### 2주차: 파일시스템 격리
- mount 네임스페이스와 `pivot_root`로 alpine rootfs에 가둔다. 안에서 `/proc`을 다시 마운트한다.
- overlayfs: 이미지는 읽기 전용으로 공유하고, 변경은 컨테이너별 upper 레이어에만 쌓는다.
- ✅ 컨테이너 안에서 `ps`를 치면 자기 프로세스만 보이고, 파일을 지워도 원본 이미지는 그대로다.

### 3주차: 자원 제한 (cgroup v2) ⭐ (면접 핵심 소재 1)
- `--mem`, `--cpu`, `--pids` 옵션을 `memory.max`, `cpu.max`, `pids.max`로 적용한다.
- 실험 3종을 만들고 결과를 기록한다.
  - fork bomb → pids 제한에 막힌다.
  - 메모리 폭주 → OOM kill이 일어나고 `memory.events`로 감지한다.
  - CPU 무한 루프 → 0.5코어 제한이 실제로 지켜지는지 측정한다.
- ✅ 실험 3종의 결과 그래프와 수치

### 4주차: 네트워크
- `mb0` 브리지를 만들고, 컨테이너마다 veth 쌍을 만들어 한쪽을 컨테이너 net 네임스페이스로 옮긴다.
  - veth는 양 끝이 연결된 가상 랜선이다. 한쪽을 컨테이너에, 다른 쪽을 브리지에 꽂는다.
- IP를 할당하고(간단한 IPAM), iptables MASQUERADE로 외부와 통신하게 한다.
- ✅ 컨테이너 A↔B ping, 컨테이너 → `ping 8.8.8.8` 성공, iperf3 대역폭을 측정한다.

### 5주차: 대시보드
- `minibox serve`: 컨테이너 목록을 보여 주고, cgroup 파일을 1초마다 읽어 SSE로 보낸다.
- 격리 시각화: `/proc/<pid>/ns/*`의 inode를 호스트와 비교해 "무엇이 분리되어 있는지"를 표로 보여 준다.
- ✅ 데모 시나리오 2~3번이 대시보드에서 보인다.

### 6주차: 패킷 여행기 v1
- pcap 파서를 직접 만든다: 글로벌 헤더 → 패킷 헤더 → Ethernet → IPv4 → TCP.
- TCP 흐름(4-tuple)별로 묶고, 두 호스트 사이 시퀀스 다이어그램으로 애니메이션을 재생한다.
- 3-way 핸드셰이크, 데이터 전송, 4-way 종료를 라벨로 붙인다.
- ✅ 컨테이너 A→B HTTP 요청 한 번이 처음부터 끝까지 재생된다.

### 7주차: 장애 주입과 TCP 분석 ⭐ (프로젝트의 차별점, 면접 핵심 소재 2)
- `minibox net chaos --loss 10% --rate 1mbit`
  - 손실: `iptables -A MINIBOX -i veth-xxx -m statistic --mode random --probability 0.1 -j DROP`
  - 대역폭 제한: veth에 `tc qdisc ... tbf`를 적용한다.
  - Orin Nano 커널에 netem이 없어서 이 조합으로 대신한다.
- 여행기에서 재전송(같은 seq 재등장), 중복 ACK, 혼잡 윈도우 추정 그래프를 보여 준다.
- (선택) 지연 주입: 설치된 커널 헤더(`linux-headers-6.8.12-1021-tegra`)로 `sch_netem.c`를 커널 밖 모듈로 직접 빌드해 올린다. 성공하면 "커널에 없는 기능을 모듈로 빌드해 붙였다"는 트러블슈팅 글감이 된다.
- 손실률 0/1/5/10%별로 처리량을 측정해 그래프로 그린다.
- 비교 벤치마크: `minibox run`과 `docker run`의 시작 시간(`hyperfine`)
- ✅ 데모 시나리오 4~5번 동작, 손실률-처리량 그래프

### 8주차: 마무리
- `minibox ps / stop / rm`을 정리하고, 종료 시 cgroup·veth·마운트를 청소하는 로직을 점검한다.
- README: 맨 위에 데모 GIF, 구조도, 실험 결과표, 트러블슈팅 링크
- 여행기는 샘플 pcap을 넣어 GitHub Pages로 따로 배포한다(설치 없이 체험 가능).
- 블로그 2편: "Go로 컨테이너 만들며 알게 된 도커의 정체", "패킷 10%를 버렸을 때 TCP가 한 일"

## 5. 측정할 지표 (README 수치표)

| 지표 | 측정 방법 |
|---|---|
| 컨테이너 시작 시간 (minibox vs docker) | `hyperfine` |
| CPU 제한 정확도 (설정 0.5코어 vs 실측) | `cpu.stat`의 usage_usec 변화량 |
| 메모리 제한 도달 시 동작 | `memory.events`의 oom_kill 카운트 |
| 손실률별 처리량 | iperf3, 손실률 0/1/5/10% |
| pcap 파싱 속도 | 10만 패킷 기준 `performance.now()` |

## 6. 예상 트러블슈팅 (실제로 겪을 확률 높음 → 기록해 둘 것)

1. **`pivot_root` EINVAL:** 새 루트가 마운트 포인트가 아니거나, 마운트 전파가 shared 상태다. → 자기 자신을 bind mount하고 `MS_PRIVATE`로 설정한다.
2. **컨테이너 안 `ps`가 호스트 프로세스를 보여 줌:** `/proc`을 다시 마운트하지 않았다.
3. **Go 런타임과 네임스페이스:** Go는 멀티스레드라서 `setns`가 한 스레드에만 적용된다. → re-exec 패턴과 `runtime.LockOSThread`로 해결한다.
4. **cgroup v2 "no such file" 또는 쓰기 실패:** 상위 cgroup의 `cgroup.subtree_control`에서 컨트롤러를 켜지 않았다.
5. **컨테이너에서 외부 인터넷 불가:** `ip_forward`가 꺼져 있거나 NAT 규칙이 빠졌다. → 패킷 여행기로 직접 디버깅하는 장면 자체를 데모에 넣는다.
6. **종료 후 찌꺼기:** veth, cgroup 디렉터리, 마운트가 남는다. → 정리 로직을 만들고, 비정상 종료에 대비해 `minibox gc`를 둔다.
7. **pcap 바이트 순서:** 매직 넘버로 엔디언을 판별하고, 네트워크 헤더는 빅엔디언으로 읽는다.
8. **Docker와 iptables 충돌:** Orin Nano에는 Docker가 설치되어 있다. Docker는 FORWARD 정책을 DROP으로 바꾸고 `br_netfilter`를 켠다. 그래서 minibox 컨테이너끼리, 혹은 외부로 가는 통신이 조용히 막힐 수 있다. → `DOCKER-USER` 체인에서 `mb0` 트래픽을 허용하거나, 실험 중에는 Docker를 끈다(`sudo systemctl stop docker`).
9. **`tc netem` 없음:** Orin Nano의 tegra 커널에는 `CONFIG_NET_SCH_NETEM`이 빠져 있다. → iptables statistic과 tbf로 대체하거나, netem을 커널 밖 모듈로 빌드한다(7주차).

## 7. 범위 조절

- **시간이 모자라면 버린다:** overlayfs(단순 복사로 대체), `docker run` 비교 벤치
- **절대 버리지 않는다:** 3주차 cgroup 실험, 7주차 장애 주입과 TCP 시각화(차별점)
- **여유가 있으면 추가한다:** user 네임스페이스로 rootless 실행, `ip` 명령 대신 netlink 직접 호출, Docker Hub에서 이미지 레이어 pull

## 8. 시작 명령어

```bash
ssh orinnano
cd ~/workspace/minibox
sudo apt update && sudo apt install -y golang-go tcpdump
go mod init minibox
```
