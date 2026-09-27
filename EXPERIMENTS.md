# 실험 기록

환경: Jetson Orin Nano (6코어, Ubuntu 24.04, 커널 6.8 tegra), cgroup v2, alpine 3.24.2

## 3주차: cgroup 자원 제한 (2026-09-27)

| 실험 | 명령 | 결과 |
|---|---|---|
| fork bomb | `run --pids 20 alpine /bin/sh -c 'f(){ f\|f & }; f; sleep 3'` | 프로세스 최대 **20**, 막힌 fork 11회, 호스트 영향 없음 |
| 메모리 폭주 | `run --mem 64m alpine tail /dev/zero` | 메모리 최대 **64.0MiB**, oom_kill **1**, 0.2초 만에 종료 |
| CPU 무한 루프 | `run --cpu 0.5 alpine sh -c '... while :; do :; done'` | 60.7초 동안 평균 **0.50코어** (설정값과 일치) |

수치는 종료 시 cgroup 파일에서 읽는다: `cpu.stat`(usage_usec), `memory.peak`, `memory.events`(oom_kill), `pids.peak`, `pids.events`(max).

## 트러블슈팅

- **fork bomb이 `/dev/null` 없음으로 실패:** `&`로 띄운 작업은 입력을 `/dev/null`로 돌린다. rootfs의 `/dev`가 비어 있어서 폭탄이 fork 제한에 닿기 전에 죽었다. → `/dev`에 tmpfs를 올리고 null, zero 등 장치 6개만 mknod했다.
- **pids 제한이 스레드도 센다:** `tail` 하나인데 `pids.peak`가 5였다. exec 전의 minibox(Go 런타임)가 만든 스레드가 잡혔다.
- **PID 1이 SIGTERM과 Ctrl+C를 무시:** busybox `timeout`과 `sh -c '명령 하나'`는 둘 다 자기 자신을 명령으로 exec한다. 그래서 무한 루프가 PID 1이 됐다. 커널은 핸들러 없는 시그널을 PID 1에게 전달하지 않으므로 끝나지 않았다(도커 `--init`이 있는 이유). → minibox가 SIGINT와 SIGTERM을 받아 컨테이너를 SIGKILL하고 cgroup과 임시 디렉터리를 정리하게 했다.
- **swap이 있으면 OOM 대신 느려진다:** Orin Nano에는 8G swapfile이 있다. `memory.swap.max=0`을 함께 걸었다.

## 4주차: 브리지·veth 네트워크 (2026-09-27)

| 확인 | 결과 |
|---|---|
| 컨테이너 → 게이트웨이(mb0, 10.88.0.1) | ping 평균 0.19ms |
| 컨테이너 → 인터넷(8.8.8.8, MASQUERADE) | ping 평균 36.0ms, 손실 0% |
| 컨테이너 A(10.88.0.2) ↔ B(10.88.0.3) | ping 평균 0.18ms, ttl=64(같은 브리지) |
| iperf3 A ← B, 10초 | **21.1 Gbits/sec**, 재전송 0, cwnd 최대 1.61MB |

- veth와 브리지는 실제 랜선이 아니라 메모리 복사라서 대역폭이 NIC와 무관하게 수십 Gbps가 나온다. 7주차 장애 주입(손실·tbf)의 기준값으로 쓴다.

### 트러블슈팅

- **Docker 때문에 브리지 안 통신도 FORWARD를 지난다:** Docker가 `br_netfilter`를 켜고 FORWARD 정책을 DROP으로 바꿔 둔다. → 전용 `MINIBOX` 체인을 FORWARD 맨 앞에 끼우고 `-i mb0` 트래픽을 허용했다.
- **`Chain 'MINIBOX' does not exist`:** 체인을 만들기 전에 점프 규칙부터 넣었다. → 체인 생성을 맨 앞으로 옮겼다.
- **컨테이너 DNS:** 호스트 resolv.conf는 127.0.0.53(systemd-resolved)이라 컨테이너에서 닿지 않는다. → 컨테이너에 `nameserver 8.8.8.8`을 쓴다.

## 5주차: 대시보드 (2026-09-27)

`--mem 64m --cpu 0.5 --pids 20` 컨테이너 하나로 데모 시나리오 2~3번을 대시보드에서 확인했다.

| 확인 | 결과 |
|---|---|
| CPU 무한 루프 15초 | 누적 7.79초 사용, 0.5코어 한도선에서 평탄 |
| `tail /dev/zero` | oom_kill 1, tail만 죽고 쉘(PID 1)은 생존 |
| fork bomb 3초 | fork 차단 27회 |
| 네임스페이스 | pid·uts·mnt·net·ipc 분리, user·cgroup·time은 호스트와 공유 |

- 네임스페이스 분리 여부는 `/proc/<pid>/ns/*` 링크의 inode를 `minibox serve` 자신의 것과 비교해 판단한다.
- 컨테이너 PID 1은 cgroup.procs의 PID 중 `/proc/<pid>/status`의 `NSpid` 마지막 값이 1인 것으로 찾는다.

## 6주차: 패킷 여행기 v1 (2026-09-27)

컨테이너 B(10.88.0.3)가 A(10.88.0.2)에 `wget http://10.88.0.2/`를 보내고, `tcpdump -i mb0`로 캡처했다. A는 `nc -l -p 80`으로 응답하는 가짜 웹서버다.

| # | 시간 | 방향 | 내용 | 상대 seq / ack |
|---|---|---|---|---|
| 1 | 0.000ms | B → A | SYN | 0 / – |
| 2 | 0.044ms | A → B | SYN-ACK | 0 / 1 |
| 3 | 0.190ms | B → A | ACK | 1 / 1 |
| 4 | 0.275ms | B → A | HTTP 요청 85B | 1 / 1 |
| 5 | 0.292ms | A → B | ACK | 1 / 86 |
| 6 | 0.399ms | A → B | HTTP 응답 44B | 1 / 86 |
| 7 | 0.434ms | B → A | ACK | 86 / 45 |
| 8 | 0.465ms | A → B | FIN | 45 / 86 |
| 9 | 0.499ms | B → A | FIN | 86 / 46 |
| 10 | 0.528ms | A → B | ACK | 46 / 87 |

- **종료가 4-way가 아니라 3개였다:** 교과서의 FIN → ACK → FIN → ACK 중 가운데 ACK와 FIN이 한 패킷에 합쳐졌다(9번). 받은 FIN에 대한 ACK를 자기 FIN에 실어 보낸 것이다.
- SYN과 FIN은 데이터가 없어도 seq를 1 소비한다. 그래서 응답 44B 뒤의 FIN이 seq 45이고, 그 FIN에 대한 ack가 46이다.
- 파서는 의존성 없는 ES 모듈(`web/pcap.js`)이고 `node --test web/`로 검증한다. 파일 헤더는 매직 넘버로 엔디언을 판별하고, 네트워크 헤더는 빅엔디언으로 읽는다.
- **트러블슈팅:** pcap 타임스탬프를 `초 + 마이크로초/1e6`로 합치면 1.7e9 근처의 실수라 마이크로초 정밀도가 사라진다. 첫 패킷의 초를 먼저 빼고 더한다.

## 7주차: 장애 주입과 TCP 분석 (2026-09-27)

`minibox net chaos --loss N%`는 `MINIBOX-CHAOS` 체인에 `-i mb0 -m statistic --mode random --probability p -j DROP`을 건다. Orin Nano 커널에는 netem이 없어서 이 방법을 쓴다.

### 손실률별 처리량 (컨테이너 B → A, iperf3 10초)

| 손실률 | 처리량 (receiver) | 재전송 | 0% 대비 |
|---|---|---|---|
| 0% | 21.3 Gbps | 0 | 1 |
| 1% | 2.07 Gbps | 17,739 | 1/10 |
| 5% | 59.1 Mbps | 2,815 | 1/360 |
| 10% | 7.86 Mbps | 606 | 1/2,700 |

![손실률-처리량](docs/loss-throughput.svg)

- Mathis 공식(처리량 ∝ 1/√p)대로라면 1%→5%는 약 2.2배만 떨어져야 한다. 실제로는 35배 떨어졌다. 손실이 커지면 SACK 기반 빠른 재전송으로 복구하지 못하는 경우가 늘어나고, 최소 200ms의 RTO와 지수 백오프가 시간을 지배하기 때문이다.

### 10% 손실 중 300KB 전송 한 번 (패킷 여행기로 분석)

`captures/loss10.pcap`, 202패킷, 416.8ms, goodput 5.76 Mbps

| 항목 | 값 |
|---|---|
| 도중에 사라진 패킷(재전송된 원본) | 9 |
| 재전송 | 14 (빠른 10 · 타임아웃 2 · 타임아웃 후 복구 2) |
| 중복 ACK (SACK 포함) | 21 |

- 빠른 재전송은 SACK 한 번 만에 0.05ms 안에 일어났다. 리눅스(RACK)는 SACK가 있으면 중복 ACK 3번을 기다리지 않는다.
- 타임아웃 2번이 각각 약 206ms를 차지했고, 둘을 합치면 전체 416ms 중 412ms다. **손실 자체보다 RTO 대기가 성능을 결정했다.**
- 캡처에 MSS(1448B)보다 큰 8192B 세그먼트가 보인다. veth의 GSO/TSO 때문에 쪼개지기 전의 덩어리가 브리지를 지나고, 손실 규칙도 이 덩어리 단위로 버린다.

### 트러블슈팅

- **중복 ACK를 놓침:** 교과서 정의(ack와 윈도우가 같음)를 그대로 썼더니, 리눅스가 ACK마다 윈도우를 바꿔서 거의 잡히지 않았다. → SACK 옵션을 파싱하고, SACK 블록이 있으면 윈도우가 달라도 중복 ACK로 봤다.
- **빠른 재전송을 타임아웃으로 오분류:** "중복 ACK 3번 = 빠른 재전송"이라는 규칙은 SACK와 RACK가 있는 리눅스에 맞지 않는다. → 직전 무응답 시간(150ms 이상이면 타임아웃), 타임아웃 뒤 복구 중인지, 중복 ACK 유무로 나눴다.
