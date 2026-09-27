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
